package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

type cmdTest struct {
	name            string
	args            []string
	stdoutRegexPath string // Regex match against file contents under test folder
	stdoutEqualPath string // Exact match with file contents under test folder
	stderrRegex     string // Regex
	stderrEqual     string // Exact
	wantErr         string // Contains
}

// createBadNode creates a synthetic Node (no real kubelet backs it) that's cordoned, tainted,
// and reporting NotReady/MemoryPressure -- everything pod_node_problems/pod_node_problem_flags
// are meant to surface. It registers cleanup and returns the Node's name.
func createBadNode(t *testing.T, clientset *kubernetes.Clientset) string {
	t.Helper()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "kubectl-status-test-bad-node-",
		},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}
	node, err := clientset.CoreV1().Nodes().Create(context.TODO(), node, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		clientset.CoreV1().Nodes().Delete(context.TODO(), node.Name, metav1.DeleteOptions{})
	})
	// The real node-lifecycle-controller starts reconciling this Node as soon as it's created
	// (e.g. adding its own NotReady taint), racing our status update -- retry on conflict.
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest, err := clientset.CoreV1().Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		latest.Status.Conditions = []corev1.NodeCondition{
			{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionFalse,
				Reason:             "KubeletNotReady",
				Message:            "kubelet is not ready",
				LastTransitionTime: metav1.Now(),
			},
			{
				Type:               corev1.NodeMemoryPressure,
				Status:             corev1.ConditionTrue,
				Reason:             "KubeletHasInsufficientMemory",
				Message:            "kubelet has insufficient memory available",
				LastTransitionTime: metav1.Now(),
			},
		}
		_, err = clientset.CoreV1().Nodes().UpdateStatus(context.TODO(), latest, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err)
	return node.Name
}

func nodeNameModifier(stdout string) string {
	return string(regexp.MustCompile(`Node/[a-z0-9-]+`).ReplaceAll([]byte(stdout), []byte(`Node/minikube`)))
}

func (c cmdTest) assert(t *testing.T, stdoutModifier func(string) string) {
	t.Helper()
	t.Logf("running cmdTest assert: %s", c)
	stdout, stderr, err := executeCMD(t, c.args)
	if stdoutModifier != nil {
		stdout = nodeNameModifier(stdout)
	}
	switch {
	case c.stdoutRegexPath == "" && c.stdoutEqualPath == "":
		assert.Empty(t, stdout)
	case c.stdoutEqualPath != "":
		outFile := path.Join("..", "tests", c.stdoutEqualPath)
		out, err := os.ReadFile(outFile)
		assert.NoErrorf(t, err, "failed to read test artifact file: %s", outFile)
		assert.Equal(t, string(out), stdout)
	case c.stdoutRegexPath != "":
		outFile := path.Join("..", "tests", c.stdoutRegexPath)
		regexBytes, err := os.ReadFile(outFile)
		assert.NoErrorf(t, err, "failed to read test artifact file: %s", outFile)
		regex := `(?ms)` + string(regexBytes)
		assert.Regexp(t, regex, stdout)
	}
	switch {
	case c.stderrRegex == "" && c.stderrEqual == "":
		assert.Empty(t, stderr)
	case c.stderrRegex != "":
		assert.Regexp(t, c.stderrRegex, stderr)
	case c.stderrEqual != "":
		assert.Equal(t, c.stderrEqual, stderr)
	}
	if c.wantErr != "" {
		assert.ErrorContains(t, err, c.wantErr)
	}
}

func TestRootCmdWithoutACluster(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	defer plugin.SetDurationRound(func(_ interface{}) string { return "1m" })()
	tests := []cmdTest{
		{
			name:        "empty call should print an error and simple usage",
			stderrRegex: `You must provide one or more resources by argument or filename.\nExample resource specifications`,
		},
		{
			name:        "pods against a non-configured client should print an error",
			args:        []string{"pods"},
			stderrRegex: `the server.*(rejected|refused)`,
		},
		{
			name:        "missing file should fail",
			args:        []string{"-f", "non-existing.yaml"},
			stderrRegex: `error: the path \"non-existing.yaml\" does not exist\n$`,
		},
		{
			name:        "file without local should fail",
			args:        []string{"-f", "../tests/artifacts/deployment-healthy.yaml"},
			stderrRegex: `the server rejected our request for an unknown reason|connect: connection refused`,
		},
		{
			name:            "file with local should succeed",
			args:            []string{"-f", "../tests/artifacts/deployment-healthy.yaml", "--local"},
			stdoutRegexPath: "artifacts/deployment-healthy.local.regex",
		},
		{
			name:            "cr file with local with status should render ready conditions",
			args:            []string{"-f", "../tests/artifacts/cr-dbconn-mymysql.yaml", "--local"},
			stdoutRegexPath: "artifacts/cr-dbconn-mymysql.local.regex",
		},
		{
			name:            "file with 'kind: List' should list all resources",
			args:            []string{"-f", "../tests/artifacts/multiple-2-pods-list.yaml", "--local"},
			stdoutRegexPath: "artifacts/multiple-2-pods-list.local.regex",
			// TODO: logs `couldn't get current server API group list: Get "http://localhost:8080/api?timeout=32s": dial tcp [::1]:8080: connect: connection refused`
		},
		{
			name:            "file with multiple yaml documents should list all resources",
			args:            []string{"-f", "../tests/artifacts/multiple-2-pods-docs.yaml", "--local"},
			stdoutRegexPath: "artifacts/multiple-2-pods-docs.local.regex",
			// TODO: logs `couldn't get current server API group list: Get "http://localhost:8080/api?timeout=32s": dial tcp [::1]:8080: connect: connection refused`
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t, nil)
		})
	}
}

func TestE2EAgainstVanillaMinikube(t *testing.T) {
	e2eMinikubeTest(t)
	testHack(t)
	klog.InitFlags(nil)
	t.Log("starting tests...")
	tests := []cmdTest{
		{
			name:        "empty call should print an error and usage",
			stderrRegex: `You must provide one or more resources by argument or filename.\nExample resource specifications`,
		},
		{
			name:        "pods on non-existent ns should fail",
			args:        []string{"pods", "-n", "non-existent"},
			stderrRegex: `error: no resources found\n$`,
		},
		{
			name:            "pods on kube-system ns should return storage-provisioner",
			args:            []string{"pods", "-n", "kube-system"},
			stdoutRegexPath: "e2e-artifacts/pods-kube-system.regex",
		},
		{
			name:            "node query should return at least a node",
			args:            []string{"node"},
			stdoutRegexPath: "e2e-artifacts/node-query.regex",
		},
		{
			name:        "cr file without a crd should fail",
			args:        []string{"-f", "../tests/artifacts/cr-dbconn-mymysql.yaml"},
			stderrRegex: `no matches for kind "DatabaseConnection" in version "example.com/v1alpha1"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viperTestHack(t)
			tt.assert(t, nil)
		})
	}
}

func testHack(t *testing.T) {
	t.Helper()
	durationRevert := plugin.SetDurationRound(func(_ interface{}) string { return "1m" })
	fixedNow := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	nowRevert := plugin.SetNowFunc(func() time.Time { return fixedNow })
	t.Cleanup(func() {
		durationRevert()
		nowRevert()
	})
}

func viperTestHack(t *testing.T) {
	t.Helper()
	viper.Reset()
	viper.Set("test-hack", true)
	t.Cleanup(func() {
		viper.Reset()
	})
}

func TestAllArtifactsLocal(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	testHack(t)
	viperTestHack(t)
	artifacts, err := filepath.Glob("../tests/artifacts/*.yaml")
	assert.NoError(t, err)
	for _, artifact := range artifacts {
		name := strings.Replace(artifact, "../tests/", "", 1)
		name = strings.Replace(name, ".yaml", "", 1)
		t.Run(name, func(t *testing.T) {
			test := cmdTest{
				args:            []string{"-f", artifact, "--local", "--shallow", "--v", "255"},
				stdoutEqualPath: name + ".out",
			}
			test.assert(t, nil) // to update the out files check /tests/artifacts/README.md
		})
	}
}

func TestAllArtifactsLocalWithIncludeAllVolumes(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	testHack(t)
	viperTestHack(t)
	artifacts := []string{
		"../tests/artifacts/pod-standalone.yaml",
		"../tests/artifacts/pod-missing-pvc.yaml",
	}
	for _, artifact := range artifacts {
		artifact := artifact
		name := strings.Replace(artifact, "../tests/", "", 1)
		name = strings.Replace(name, ".yaml", "", 1)
		t.Run(name, func(t *testing.T) {
			test := cmdTest{
				args:            []string{"-f", artifact, "--local", "--shallow", "--v", "255", "--include-all-volumes"},
				stdoutEqualPath: name + ".include-all-volumes.out",
			}
			test.assert(t, nil)
		})
	}
}

func TestAllArtifactsLocalWithAbsoluteTime(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	viperTestHack(t)
	artifacts := []string{
		"../tests/artifacts/pod-standalone.yaml",
	}
	for _, artifact := range artifacts {
		artifact := artifact
		name := strings.Replace(artifact, "../tests/", "", 1)
		name = strings.Replace(name, ".yaml", "", 1)
		t.Run(name, func(t *testing.T) {
			test := cmdTest{
				args:            []string{"-f", artifact, "--local", "--shallow", "--v", "255", "--absolute-time"},
				stdoutEqualPath: name + ".absolute-time.out",
			}
			test.assert(t, nil)
		})
	}
}

func executeCMD(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	cmd := RootCmd()
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	cmd.SetArgs(args)
	t.Logf("running command with: %s", strings.Join(args, " "))
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func startMinikube(t *testing.T) {
	t.Helper()
	// `make test-e2e` computes an isolated profile name (branch + Claude Code session
	// hash, see Makefile) and passes ASSUME_MINIKUBE_IS_CONFIGURED=true, so it never
	// reaches this function. This fallback only matters for ad hoc
	// `go test -run TestE2E...` invocations that bypass the Makefile: set E2E_PROFILE
	// yourself (`make print-e2e-profile` prints the same name the Makefile would use)
	// to avoid colliding with other worktrees/sessions on a shared t.Name() profile.
	// TODO: derive branch+session identity here directly instead of relying on the
	// caller to export E2E_PROFILE, so ad hoc runs are isolated automatically too.
	clusterName := os.Getenv("E2E_PROFILE")
	if clusterName == "" {
		clusterName = t.Name()
	}
	t.Logf("Creating temp folder for minikube.kubeconfig for minikube %s ...", clusterName)
	dir, err := os.MkdirTemp("", clusterName)
	require.NoError(t, err)
	kubeconfig := path.Join(dir, "minikube.kubeconfig")
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Logf("Starting Minikube cluster %s with %s ...", clusterName, kubeconfig)
	startMinikube := exec.Command("minikube", "start", "-p", clusterName, "--addons=metrics-server")
	require.NoError(t, startMinikube.Run())
	require.NoError(t, exec.Command("kubectl", "-n", "kube-system", "rollout", "status",
		"deployment/metrics-server", "--timeout=120s").Run())
	t.Cleanup(func() {
		cmd := exec.Command("minikube", "delete", "-p", clusterName)
		t.Logf("Deleting Minikube cluster %s...", clusterName)
		if err := cmd.Run(); err != nil {
			t.Log("Error deleting Minikube cluster:", err)
		}
		t.Logf("Deleting temp folder for minikube %s: %s ...", clusterName, dir)
		if err := os.RemoveAll(dir); err != nil {
			t.Log("Error deleting temp folder of minikube.kubeconfig:", err)
		}
	})
}

func e2eMinikubeTest(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping e2e test as RUN_E2E_TESTS is not set to true")
	}
	if os.Getenv("ASSUME_MINIKUBE_IS_CONFIGURED") == "true" {
		t.Logf("assuming current kubeconfig context is pointng a minikube to run e2e tests")
	} else {
		startMinikube(t)
	}
}

func TestE2EDynamicManifests(t *testing.T) {
	e2eMinikubeTest(t)
	testHack(t)
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		homeDir := os.Getenv("HOME")
		kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		t.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("owners should be included with deep", func(t *testing.T) {
		viperTestHack(t)
		owner := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner",
				Namespace: "default",
			},
		}
		owner, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), owner, metav1.CreateOptions{})
		defer clientset.CoreV1().Secrets("default").Delete(context.TODO(), "owner", metav1.DeleteOptions{})
		require.NoError(t, err)
		uid := owner.GetUID()
		t.Logf("owner secret is created, uid is %s", uid)
		// Create the child secret with owner reference
		child := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "child",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "Secret",
						Name:       "owner",
						UID:        uid,
					},
				},
			},
		}
		_, err = clientset.CoreV1().Secrets("default").Create(context.TODO(), child, metav1.CreateOptions{})
		t.Log("child secret is created")
		defer clientset.CoreV1().Secrets("default").Delete(context.TODO(), "child", metav1.DeleteOptions{})
		require.NoError(t, err)

		test := cmdTest{
			args: []string{"secret/child", "--deep", "--v", "7"},
			// Secret.tmpl intentionally omits kstatus_summary (Secret is always reported
			// "Resource is always ready" by kstatus, so the "Current:" line is redundant
			// noise) -- see tests/artifacts/secret-tls-healthy.out for the same committed
			// expectation.
			stdoutRegexPath: "e2e-artifacts/secret-child-with-owner.regex",
		}
		test.assert(t, nil) // to update the out files check /tests/artifacts/README.md
	})
	t.Run("ownerReference pointing at a deleted owner is flagged as orphan", func(t *testing.T) {
		viperTestHack(t)
		// The child is rendered with --local straight from a manifest rather than created on
		// the cluster: a live Secret with a dangling ownerReference gets swept up by the
		// built-in garbage collector almost immediately (it treats a missing owner as a signal
		// to cascade-delete the dependent), which would make this test flaky. --local still
		// resolves the ownerReference against the real API server (only the child object itself
		// is local), so the orphan check is exercised the same way, without the race.
		cmdTest{
			args:            []string{"-f", "../tests/e2e-artifacts/secret-orphan-owner-reference.yaml", "--local", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/secret-orphan-owner-reference.regex",
		}.assert(t, nil)
	})
	t.Run("pod on a cordoned node with an untolerated taint and a bad condition", func(t *testing.T) {
		viperTestHack(t)
		nodeName := createBadNode(t, clientset)

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-on-bad-node",
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err := clientset.CoreV1().Pods("default").Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods("default").Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"pod/pod-on-bad-node", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-on-bad-node.regex",
		}.assert(t, nil)
	})
	t.Run("workload's matching pod on a cordoned node surfaces a compact node-problem flag", func(t *testing.T) {
		viperTestHack(t)
		nodeName := createBadNode(t, clientset)

		// The Pod's spec.nodeName is set directly at creation, bypassing the scheduler, so it
		// never actually runs -- ReplicaSet.tmpl's selector-based pod lookup only needs matching
		// labels, not real ownership, to include it in the health summary.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-on-bad-node-for-rs",
				Namespace: "default",
				Labels:    map[string]string{"app": "kubectl-status-test-bad-rs"},
			},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err := clientset.CoreV1().Pods("default").Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods("default").Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		one := int32(1)
		rs := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bad-rs",
				Namespace: "default",
			},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "kubectl-status-test-bad-rs"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "kubectl-status-test-bad-rs"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
				},
			},
		}
		_, err = clientset.AppsV1().ReplicaSets("default").Create(context.TODO(), rs, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.AppsV1().ReplicaSets("default").Delete(context.TODO(), rs.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"rs/bad-rs", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-on-bad-node-for-rs.regex",
		}.assert(t, nil)
	})
	t.Run("pod selected by a NetworkPolicy surfaces the compact isolation signal", func(t *testing.T) {
		// A dedicated namespace keeps this test in control of exactly which NetworkPolicy
		// objects exist -- an empty podSelector elsewhere in a shared namespace (e.g. "default")
		// would also match this Pod and make the asserted policy name/count non-deterministic.
		viperTestHack(t)
		ns := "e2e-netpol-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netpol-selected-pod",
				Namespace: ns,
				Labels:    map[string]string{"app": "kubectl-status-test-netpol-target"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "busybox", Command: []string{"sleep", "infinity"}}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		netpol := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "deny-ingress-to-app", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "kubectl-status-test-netpol-target"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			},
		}
		_, err = clientset.NetworkingV1().NetworkPolicies(ns).Create(context.TODO(), netpol, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.NetworkingV1().NetworkPolicies(ns).Delete(context.TODO(), netpol.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"pod/netpol-selected-pod", "-n", ns, "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-selected-by-network-policy.regex",
		}.assert(t, nil)
	})
	t.Run("deployment rollout with --include-rollout-diffs shows the diff between revisions", func(t *testing.T) {
		viperTestHack(t)
		name := "rollout-diff-test"
		one := int32(1)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}}},
				},
			},
		}
		_, err := clientset.AppsV1().Deployments("default").Create(context.TODO(), dep, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.AppsV1().Deployments("default").Delete(context.TODO(), name, metav1.DeleteOptions{})
		waitFor(t, "deployment/"+name, "condition=Available")

		// Update the image so a second ReplicaSet revision is created, giving --include-rollout-diffs
		// something to diff.
		dep, err = clientset.AppsV1().Deployments("default").Get(context.TODO(), name, metav1.GetOptions{})
		require.NoError(t, err)
		dep.Spec.Template.Spec.Containers[0].Image = "nginx:1.26"
		_, err = clientset.AppsV1().Deployments("default").Update(context.TODO(), dep, metav1.UpdateOptions{})
		require.NoError(t, err)
		rolloutCmd := exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", "default", "--timeout=2m")
		output, err := rolloutCmd.CombinedOutput()
		t.Logf("rollout status for %s: %s", name, output)
		require.NoError(t, err)

		// The order in which the two ReplicaSet revisions are diffed (and so which side
		// gets "-" vs "+") isn't guaranteed, so the fixture alternates both directions.
		cmdTest{
			args:            []string{"deployment/" + name, "--include-rollout-diffs", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/rollout-diff.regex",
		}.assert(t, nil)
	})
	t.Run("Rollouts section shows a single blocked rollout even without a second one to compare against", func(t *testing.T) {
		// #213: the Rollouts list used to be suppressed unless there were 2+ rollouts to
		// compare, hiding a stuck or unhealthy first/only rollout. It should now also show up
		// for a single rollout that isn't done yet.
		badImage := "kubectl-status-e2e-this-image-does-not-exist"

		t.Run("deployment", func(t *testing.T) {
			viperTestHack(t)
			name := "e2e-rollouts-blocked-deployment"
			one := int32(1)
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: appsv1.DeploymentSpec{
					Replicas: &one,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: badImage}}},
					},
				},
			}
			_, err := clientset.AppsV1().Deployments("default").Create(context.TODO(), dep, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().Deployments("default").Delete(context.TODO(), name, metav1.DeleteOptions{})
			podName := waitForPodByLabel(t, "default", "app="+name)
			waitForContainerWaitingReason(t, "pod/"+podName, "app", "ImagePullBackOff")

			cmdTest{
				args:            []string{"deployment/" + name, "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-deployment.regex",
			}.assert(t, nil)
		})
		t.Run("statefulset", func(t *testing.T) {
			viperTestHack(t)
			name := "e2e-rollouts-blocked-statefulset"
			one := int32(1)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    &one,
					ServiceName: name,
					Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: badImage}}},
					},
				},
			}
			_, err := clientset.AppsV1().StatefulSets("default").Create(context.TODO(), sts, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().StatefulSets("default").Delete(context.TODO(), name, metav1.DeleteOptions{})
			waitForContainerWaitingReason(t, "pod/"+name+"-0", "app", "ImagePullBackOff")

			cmdTest{
				args:            []string{"statefulset/" + name, "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-statefulset.regex",
			}.assert(t, nil)
		})
		t.Run("daemonset", func(t *testing.T) {
			viperTestHack(t)
			name := "e2e-rollouts-blocked-daemonset"
			ds := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: appsv1.DaemonSetSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: badImage}}},
					},
				},
			}
			_, err := clientset.AppsV1().DaemonSets("default").Create(context.TODO(), ds, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().DaemonSets("default").Delete(context.TODO(), name, metav1.DeleteOptions{})
			podName := waitForPodByLabel(t, "default", "app="+name)
			waitForContainerWaitingReason(t, "pod/"+podName, "app", "ImagePullBackOff")

			cmdTest{
				args:            []string{"daemonset/" + name, "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-daemonset.regex",
			}.assert(t, nil)
		})
		t.Run("healthy single rollout stays suppressed", func(t *testing.T) {
			viperTestHack(t)
			name := "e2e-rollouts-healthy-deployment"
			one := int32(1)
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec: appsv1.DeploymentSpec{
					Replicas: &one,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}}},
					},
				},
			}
			_, err := clientset.AppsV1().Deployments("default").Create(context.TODO(), dep, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().Deployments("default").Delete(context.TODO(), name, metav1.DeleteOptions{})
			waitFor(t, "deployment/"+name, "condition=Available")

			cmdTest{
				args:            []string{"deployment/" + name, "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-healthy-deployment.regex",
			}.assert(t, nil)
		})
		t.Run("three healthy revisions with --include-rollout-diffs shows both consecutive diffs", func(t *testing.T) {
			// Needs two distinct spec changes (three revisions total) before the check, so
			// there are two consecutive pairs to diff, not just the one covered by the
			// "--include-rollout-diffs shows the diff between revisions" test above.
			viperTestHack(t)
			name := "e2e-rollouts-three-revisions"
			applyManifest(t, "e2e-artifacts/rollouts-three-revisions.yaml")
			waitFor(t, "deployment/"+name, "condition=Available")

			require.NoError(t, exec.Command("kubectl", "set", "image", "deployment/"+name, "nginx=nginx:1.26", "-n", "default").Run())
			rolloutCmd := exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", "default", "--timeout=2m")
			output, err := rolloutCmd.CombinedOutput()
			t.Logf("rollout status for %s (nginx:1.26): %s", name, output)
			require.NoError(t, err)
			waitForSinglePod(t, "default", "app="+name)

			require.NoError(t, exec.Command("kubectl", "set", "image", "deployment/"+name, "nginx=nginx:1.27", "-n", "default").Run())
			rolloutCmd = exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", "default", "--timeout=2m")
			output, err = rolloutCmd.CombinedOutput()
			t.Logf("rollout status for %s (nginx:1.27): %s", name, output)
			require.NoError(t, err)
			waitForSinglePod(t, "default", "app="+name)

			cmdTest{
				args:            []string{"deployment/" + name, "--include-events=false", "--include-rollout-diffs", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-three-revisions-with-diffs.regex",
			}.assert(t, nil)
		})
	})
	t.Run("sts-with-ingress", func(t *testing.T) {
		viperTestHack(t)
		// using sts here as the pod name is predictable in that case, not true for deployments and ds
		applyManifest(t, "e2e-artifacts/sts-with-ingress.yaml")
		waitFor(t, "sts/sts-with-ingress", "jsonpath={.status.readyReplicas}=1")
		cmdTest{
			// Log/volume usage bytes come from live kubelet stats and aren't reproducible
			// across runs, so this is matched as a regex rather than exact text.
			args:            []string{"pod/sts-with-ingress-0", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-ingress.pod.regex",
		}.assert(t, nodeNameModifier)
		cmdTest{
			args:            []string{"service/sts-with-ingress", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-ingress.service.regex",
		}.assert(t, nil)
		cmdTest{
			args:            []string{"service/sts-with-ingress", "--include-events=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-ingress.service-deep.regex",
		}.assert(t, nil)
	})
	t.Run("svc-with-httproute", func(t *testing.T) {
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/svc-with-httproute.yaml")
		cmdTest{
			args:            []string{"service/svc-with-httproute", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/svc-with-httproute.regex",
		}.assert(t, nil)
		cmdTest{
			args:            []string{"service/svc-with-httproute", "--include-events=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/svc-with-httproute.deep.regex",
		}.assert(t, nil)
	})
	t.Run("sts-with-nodeport", func(t *testing.T) {
		viperTestHack(t)
		// using sts here as the pod name is predictable in that case, not true for deployments and ds
		applyManifest(t, "e2e-artifacts/sts-with-nodeport.yaml")
		waitFor(t, "sts/sts-with-nodeport", "jsonpath={.status.readyReplicas}=1")
		waitFor(t, "pdb/sts-with-nodeport", "jsonpath={.status.currentHealthy}=1")
		cmdTest{
			// Log/volume usage bytes come from live kubelet stats and aren't reproducible
			// across runs, so this is matched as a regex rather than exact text.
			args:            []string{"pod/sts-with-nodeport-0", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-nodeport.pod.regex",
		}.assert(t, nodeNameModifier)
		cmdTest{
			args:            []string{"pdb/sts-with-nodeport", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-nodeport.pdb.regex",
		}.assert(t, nodeNameModifier)
		cmdTest{
			args:            []string{"sts/sts-with-nodeport", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-nodeport.sts.regex",
		}.assert(t, nil)
	})
	t.Run("pdb-empty-selector-conflict", func(t *testing.T) {
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/pdb-empty-selector-conflict.yaml")
		waitFor(t, "sts/pdb-conflict-test", "jsonpath={.status.readyReplicas}=1")
		// Kubernetes' disruption controller picks one of the two overlapping PDBs arbitrarily
		// and leaves the other's currentHealthy permanently at 0 -- observedGeneration is the
		// reliable "controller has made its (possibly permanent) decision" signal here, not
		// currentHealthy, which may never reach 1 for the non-chosen budget.
		waitFor(t, "pdb/pdb-conflict-test", "jsonpath={.status.observedGeneration}=1")
		waitFor(t, "pdb/pdb-conflict-test-catch-all", "jsonpath={.status.observedGeneration}=1")
		cmdTest{
			args:            []string{"pod/pdb-conflict-test-0", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pdb-empty-selector-conflict.pod.regex",
		}.assert(t, nodeNameModifier)
	})
	t.Run("tcproute-with-gateway", func(t *testing.T) {
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/tcproute-with-gateway.yaml")
		cmdTest{
			args:            []string{"tcproute/e2e-tcproute", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/tcproute-with-gateway.regex",
		}.assert(t, nil)
		cmdTest{
			args:            []string{"tcproute/e2e-tcproute", "--include-events=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/tcproute-with-gateway.deep.regex",
		}.assert(t, nil)
	})
	t.Run("udproute-with-gateway", func(t *testing.T) {
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/udproute-with-gateway.yaml")
		cmdTest{
			args:            []string{"udproute/e2e-udproute", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/udproute-with-gateway.regex",
		}.assert(t, nil)
		cmdTest{
			args:            []string{"udproute/e2e-udproute", "--include-events=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/udproute-with-gateway.deep.regex",
		}.assert(t, nil)
	})
	t.Run("listenerset-with-gateway", func(t *testing.T) {
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/listenerset-with-gateway.yaml")
		cmdTest{
			args:            []string{"listenerset/e2e-listenerset", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/listenerset-with-gateway.regex",
		}.assert(t, nil)
		cmdTest{
			args:            []string{"listenerset/e2e-listenerset", "--include-events=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/listenerset-with-gateway.deep.regex",
		}.assert(t, nil)
	})
	t.Run("backendtlspolicy-with-target", func(t *testing.T) {
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/backendtlspolicy-with-target.yaml")
		cmdTest{
			args:            []string{"backendtlspolicy/e2e-backendtlspolicy", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/backendtlspolicy-with-target.regex",
		}.assert(t, nil)
		cmdTest{
			args:            []string{"backendtlspolicy/e2e-backendtlspolicy", "--include-events=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/backendtlspolicy-with-target.deep.regex",
		}.assert(t, nil)
	})
	t.Run("sts-without-service", func(t *testing.T) {
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/sts-without-service.yaml")
		waitFor(t, "sts/sts-without-service", "jsonpath={.status.readyReplicas}=1")
		cmdTest{
			args:            []string{"sts/sts-without-service", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-without-service.regex",
		}.assert(t, nil)
	})
	t.Run("tls-validation", func(t *testing.T) {
		// Builds a real cert-manager CA chain (self-signed root -> ca-type Issuer -> leaf
		// certificate) so the Ingress/Gateway/Secret TLS-consistency checks (self-signed,
		// hostname/SAN match, wrong type, missing keys) can be exercised against genuine
		// certificate content. --shallow (used by the offline golden-file tests) makes
		// KubeGetFirst a no-op, so this e2e suite is the only place in the whole test suite
		// that exercises the found-secret validation branches of Ingress.tmpl/Gateway.tmpl.
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/tls-validation-ca.yaml")
		waitFor(t, "certificate/e2e-tls-root-ca", "condition=Ready")
		waitFor(t, "issuer/e2e-tls-ca-issuer", "condition=Ready")
		applyManifest(t, "e2e-artifacts/tls-validation-leaf.yaml")
		waitFor(t, "certificate/e2e-tls-leaf", "condition=Ready")

		t.Run("secret/leaf shows full non-self-signed certificate detail", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"secret/e2e-tls-leaf-tls", "--include-events=false", "--v", "5"})
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-secret-leaf.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			// The secret also carries a ca.crt (the self-signed root CA cert), which
			// legitimately renders its own "Self-signed:" line further down -- scope the
			// check to the leaf cert's own block, which precedes it.
			leafBlock, _, _ := strings.Cut(stdout, "Certificate (ca.crt)")
			assert.NotContains(t, leafBlock, "Self-signed:")
		})
		t.Run("secret/leaf with --deep inlines the full Certificate and Issuer detail", func(t *testing.T) {
			cmdTest{
				args:            []string{"secret/e2e-tls-leaf-tls", "--deep", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-secret-leaf-deep.regex",
			}.assert(t, nil)
		})
		t.Run("secret/root-ca is flagged self-signed", func(t *testing.T) {
			cmdTest{
				args:            []string{"secret/e2e-tls-root-ca-secret", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-secret-root.regex",
			}.assert(t, nil)
		})
		t.Run("ingress with matching hostname is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"ingress/e2e-tls-ingress-healthy", "--include-events=false", "--v", "5"})
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-ingress-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{
				"doesn't exist",
				"wrong type:",
				"missing keys:",
				"certificate parse error:",
				"certificate is self-signed",
				"certificate doesn't match host",
			} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("ingress with mismatched hostname flags hostname mismatch", func(t *testing.T) {
			cmdTest{
				args:            []string{"ingress/e2e-tls-ingress-mismatch", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-ingress-mismatch.regex",
			}.assert(t, nil)
		})
		t.Run("ingress referencing the root CA secret flags self-signed", func(t *testing.T) {
			cmdTest{
				args:            []string{"ingress/e2e-tls-ingress-selfsigned", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-ingress-selfsigned.regex",
			}.assert(t, nil)
		})
		t.Run("ingress with --deep inlines the full Secret detail", func(t *testing.T) {
			cmdTest{
				args:            []string{"ingress/e2e-tls-ingress-healthy", "--deep", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-ingress-deep.regex",
			}.assert(t, nil)
		})
		t.Run("gateway with matching hostname is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"gateway/e2e-tls-gw-healthy", "--include-events=false", "--v", "5"})
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-gateway-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("gateway with mismatched hostname flags hostname mismatch", func(t *testing.T) {
			cmdTest{
				args:            []string{"gateway/e2e-tls-gw-mismatch", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-gateway-mismatch.regex",
			}.assert(t, nil)
		})
		applyManifest(t, "e2e-artifacts/tls-validation-grpcroute.yaml")
		t.Run("grpcroute attached to healthy gateway listener shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"grpcroute/e2e-tls-grpcroute-healthy", "--include-events=false", "--v", "5"})
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-grpcroute-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:", "doesn't exist"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("grpcroute with its own hostname mismatching the cert SANs flags hostname mismatch", func(t *testing.T) {
			cmdTest{
				args:            []string{"grpcroute/e2e-tls-grpcroute-mismatch", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-grpcroute-mismatch.regex",
			}.assert(t, nil)
		})
		applyManifest(t, "e2e-artifacts/tls-validation-tlsroute.yaml")
		t.Run("tlsroute attached to Terminate listener with matching hostname is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"tlsroute/e2e-tlsroute-healthy", "--include-events=false", "--v", "5"})
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-tlsroute-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:", "doesn't exist"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("tlsroute with its own hostname mismatching the cert SANs flags hostname mismatch", func(t *testing.T) {
			cmdTest{
				args:            []string{"tlsroute/e2e-tlsroute-mismatch", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-tlsroute-mismatch.regex",
			}.assert(t, nil)
		})
		t.Run("tlsroute attached to a Passthrough listener shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"tlsroute/e2e-tlsroute-passthrough", "--include-events=false", "--v", "5"})
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-tlsroute-passthrough.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:", "doesn't exist"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		applyManifest(t, "e2e-artifacts/tls-validation-httproute.yaml")
		t.Run("httproute attached to a healthy listener is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"httproute/e2e-tls-httproute-healthy", "--include-events=false", "--v", "5"})
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-httproute-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{"doesn't exist", "wrong type:", "missing keys:", "parse error:", "self-signed", "hostname mismatch"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("httproute attached to a mismatched-hostname listener flags hostname mismatch", func(t *testing.T) {
			cmdTest{
				args:            []string{"httproute/e2e-tls-httproute-mismatch", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-httproute-mismatch.regex",
			}.assert(t, nil)
		})
	})
	t.Run("pod-image-pull-secrets", func(t *testing.T) {
		// --shallow (used by the offline golden-file tests) makes KubeGetFirst a no-op,
		// so this e2e suite is the only place that exercises the found-secret validation
		// branches of Pod.tmpl's imagePullSecrets check (Check A) and the "broken secrets"
		// correlation branch of the ImagePullBackOff hint (Check B).
		viperTestHack(t)
		applyManifest(t, "e2e-artifacts/pod-image-pull-secrets.yaml")
		// waitForImagePullBackoff accepts either ErrImagePull or ImagePullBackOff, but the
		// kubelet keeps cycling between them on its retry loop, so it doesn't give a stable
		// render. Pin to ImagePullBackOff specifically -- it's the longer-lived of the two
		// (exponential backoff), so it survives the gap until the subtests below observe it.
		waitForContainerWaitingReason(t, "pod/e2e-pod-missing-pull-secret", "main", "ImagePullBackOff")
		waitForContainerWaitingReason(t, "pod/e2e-pod-wrong-type-pull-secret", "main", "ImagePullBackOff")
		waitForContainerWaitingReason(t, "pod/e2e-pod-healthy-pull-secret", "main", "ImagePullBackOff")

		t.Run("pod referencing a non-existent Secret flags it and correlates with the pull failure", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-missing-pull-secret", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-missing.regex",
			}.assert(t, nil)
		})
		t.Run("pod referencing a wrong-type Secret flags it and correlates with the pull failure", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-wrong-type-pull-secret", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-wrong-type.regex",
			}.assert(t, nil)
		})
		t.Run("pod referencing a healthy Secret shows no warnings", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-healthy-pull-secret", "--include-events=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-healthy.regex",
			}.assert(t, nil)
		})
	})
	t.Run("pod-container-logs", func(t *testing.T) {
		// --shallow (used by the offline golden-file tests) makes KubeGetContainerLogs a
		// no-op, so this e2e suite is the only place that exercises real log fetching: a
		// terminated init container with output (current-state logs), a terminated init
		// container with no output (yellow "no logs to show"), a crashlooping regular
		// container that has recently restarted (previous-instance logs), and a healthy
		// sidecar plus a healthy regular container that should show neither.
		//
		// withinLastHour compares real container timestamps against nowFunc, so the
		// suite-wide fixed clock (testHack, frozen at 2026-06-30) has to be swapped for the
		// real wall clock for this render, or a live restart looks like it happened in the
		// future and never matches.
		viperTestHack(t)
		plugin.SetNowFunc(time.Now)
		t.Cleanup(func() {
			plugin.SetNowFunc(func() time.Time { return time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC) })
		})
		applyManifest(t, "e2e-artifacts/pod-container-logs.yaml")
		// Wait for a stable Waiting(CrashLoopBackOff) state rather than just restartCount > 0:
		// the container's current state otherwise flips between Waiting and Terminated(Error)
		// as the kubelet retries, which would make the golden regex flaky.
		waitForContainerWaitingReason(t, "pod/e2e-pod-container-logs", "crasher", "CrashLoopBackOff")

		cmdTest{
			args:            []string{"pod/e2e-pod-container-logs", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-container-logs.regex",
		}.assert(t, nil)
	})
	t.Run("node correctly resolves pod metrics for pods in multiple namespaces via the batched PodMetrics lookup", func(t *testing.T) {
		// Node.tmpl loops over every pod on the node (KubeGetNonTerminatedPodsOnNode) and looks
		// up each one's PodMetrics via KubeGetPodMetrics, which fetches metrics.k8s.io once for
		// the whole render (cluster-wide, or per-namespace as a fallback) instead of once per
		// pod. Pods in two distinct namespaces exercise the namespace-aware lookup within that
		// shared result: only --shallow-free live runs touch this path at all (see
		// TestAllArtifactsLocal), so this is the only place it's covered.
		viperTestHack(t)
		nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, nodes.Items)
		nodeName := nodes.Items[0].Name

		for _, ns := range []string{"e2e-node-metrics-a", "e2e-node-metrics-b"} {
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-metrics-pod", Namespace: ns},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "busybox", Command: []string{"sleep", "infinity"}}},
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
				"pod/e2e-metrics-pod", "-n", ns, "--timeout=2m").Run())
			waitForPodMetrics(t, ns, "e2e-metrics-pod")
		}

		cmdTest{
			args:            []string{"node/" + nodeName, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/node-metrics-multi-namespace.regex",
		}.assert(t, nil)
	})
	t.Run("pod containers section warns when metrics-server's APIService is missing", func(t *testing.T) {
		// Issue #165 case 1: metrics-server was never installed. We simulate that by removing
		// just the APIService object that fronts it (not the Deployment/Service), which is
		// exactly what KubeMetricsUnavailableReason checks -- so the round trip is near-instant
		// and doesn't disturb metrics-server's actual health for other subtests.
		viperTestHack(t)
		apiServiceYAML, err := exec.Command("kubectl", "get", "apiservice", "v1beta1.metrics.k8s.io", "-o", "yaml").Output()
		require.NoError(t, err)
		require.NoError(t, exec.Command("kubectl", "delete", "apiservice", "v1beta1.metrics.k8s.io").Run())
		t.Cleanup(func() {
			applyCmd := exec.Command("kubectl", "apply", "-f", "-")
			applyCmd.Stdin = bytes.NewReader(apiServiceYAML)
			require.NoError(t, applyCmd.Run())
			waitForMetricsAPIServiceAvailable(t)
		})

		_, err = clientset.CoreV1().Pods("default").Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-pod-metrics-server-missing"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "busybox", Command: []string{"sleep", "infinity"}}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods("default").Delete(context.TODO(), "e2e-pod-metrics-server-missing", metav1.DeleteOptions{})
		})
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
			"pod/e2e-pod-metrics-server-missing", "--timeout=2m").Run())

		cmdTest{
			args:            []string{"pod/e2e-pod-metrics-server-missing", "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-metrics-server-missing.regex",
		}.assert(t, nil)
	})
}

func applyManifest(t *testing.T, filepath string) {
	t.Helper()
	filepath = path.Join("..", "tests", filepath)
	cmd := exec.Command("kubectl", "apply", "-f", filepath)
	output, err := cmd.CombinedOutput()
	t.Cleanup(func() {
		t.Logf("deleting manifest %s", filepath)
		cmd := exec.Command("kubectl", "delete", "-f", filepath)
		output, err := cmd.CombinedOutput()
		assert.NoError(t, err)
		t.Logf("manifest deleted %s: %s", filepath, string(output))
	})
	require.NoError(t, err)
	t.Logf("applied manifest %s: %s", filepath, string(output))
}

func waitFor(t *testing.T, resource, forParam string) {
	t.Helper()
	cmd := exec.Command("kubectl", "wait", "--for", forParam, resource, "--timeout=2m")
	output, err := cmd.CombinedOutput()
	t.Logf("wait result for %s: %s", resource, string(output))
	require.NoError(t, err)
}

// waitForContainerRestart polls until the named container in the resource reports a
// restartCount greater than zero.
// waitForSinglePod polls until exactly one pod matches the given label selector. Used after a
// rollout to make sure the previous revision's pod has actually finished terminating: `kubectl
// rollout status` and the Deployment's `.status.replicas` field can both report the rollout as
// done slightly before the old pod object is removed, which otherwise makes the rendered output
// briefly list two Pods instead of one.
func waitForSinglePod(t *testing.T, namespace, labelSelector string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", "get", "pods", "-n", namespace, "-l", labelSelector,
			"-o", "jsonpath={.items[*].metadata.name}")
		output, err := cmd.CombinedOutput()
		if err == nil {
			names := strings.Fields(string(output))
			if len(names) == 1 {
				t.Logf("exactly one pod %s matches selector %s in namespace %s", names[0], labelSelector, namespace)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for exactly one pod matching selector %s in namespace %s", labelSelector, namespace)
}

// waitForContainerWaitingReason polls until the named container in the resource reports the
// given waiting-state reason. Used instead of a plain restart-count check because a crashlooping
// container's current state flips between Waiting(CrashLoopBackOff) and Terminated(Error) as the
// kubelet retries, so waiting for a stable, specific state avoids a flaky render.
// waitForPodByLabel polls until exactly one pod matches the given label selector and returns
// its name. Used for Deployment/DaemonSet, whose pod names include a random suffix that isn't
// known ahead of time (unlike StatefulSet, where pod names are predictable).
func waitForPodByLabel(t *testing.T, namespace, labelSelector string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", "get", "pods", "-n", namespace, "-l", labelSelector,
			"-o", "jsonpath={.items[*].metadata.name}")
		output, err := cmd.CombinedOutput()
		if err == nil {
			names := strings.Fields(string(output))
			if len(names) == 1 {
				name := names[0]
				t.Logf("found pod %s matching selector %s in namespace %s", name, labelSelector, namespace)
				return name
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for exactly one pod matching selector %s in namespace %s", labelSelector, namespace)
	return ""
}

func waitForContainerWaitingReason(t *testing.T, resource, containerName, reason string) {
	t.Helper()
	jsonpath := fmt.Sprintf(`{.status.containerStatuses[?(@.name=="%s")].state.waiting.reason}`, containerName)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", "get", resource, "-o", "jsonpath="+jsonpath)
		output, err := cmd.CombinedOutput()
		if err == nil && strings.TrimSpace(string(output)) == reason {
			t.Logf("%s container %s reached waiting reason %s", resource, containerName, reason)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for %s container %s to report waiting reason %s", resource, containerName, reason)
}

// waitForPodMetrics polls the metrics.k8s.io API directly until it has scraped data for the
// given pod. metrics-server's scrape interval means a freshly-created pod's metrics aren't
// available immediately after it goes Ready.
func waitForPodMetrics(t *testing.T, namespace, name string) {
	t.Helper()
	rawPath := fmt.Sprintf("/apis/metrics.k8s.io/v1beta1/namespaces/%s/pods/%s", namespace, name)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if err := exec.Command("kubectl", "get", "--raw", rawPath).Run(); err == nil {
			t.Logf("metrics available for pod %s/%s", namespace, name)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for metrics.k8s.io data for pod %s/%s", namespace, name)
}

// waitForMetricsAPIServiceAvailable polls until the metrics-server APIService reports
// Available=True. Used after recreating it post-deletion: the backing Deployment/Service were
// never touched, so this is a quick re-sync, not the ~1 minute metrics-server itself needs to
// scrape fresh data.
func waitForMetricsAPIServiceAvailable(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		output, err := exec.Command("kubectl", "get", "apiservice", "v1beta1.metrics.k8s.io",
			"-o", `jsonpath={.status.conditions[?(@.type=="Available")].status}`).Output()
		if err == nil && strings.TrimSpace(string(output)) == "True" {
			t.Log("metrics-server APIService is Available again")
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for metrics-server APIService to become Available again")
}
