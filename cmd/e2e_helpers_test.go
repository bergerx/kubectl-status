package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

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

func (c cmdTest) assert(t *testing.T, stdoutModifier func(string) string, opts ...func(*plugin.RenderConfig)) {
	t.Helper()
	t.Logf("running cmdTest assert: %s", c)
	stdout, stderr, err := executeCMD(t, c.args, opts...)
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

// testHackOpts fixes plugin.RenderConfig's Now/DurationRound/StartedAfterClause for
// deterministic e2e output (see plugin.ApplyTestHack). Each RootCmd() invocation gets its own
// fresh RenderConfig (see cmd/main.go), so unlike the old global package-var overrides this needs
// no revert -- see #694.
func testHackOpts(t *testing.T) []func(*plugin.RenderConfig) {
	t.Helper()
	return []func(*plugin.RenderConfig){plugin.ApplyTestHack}
}

// viperTestHackOpts sets "test-hack" on this invocation's RenderConfig, which makes ip() report a
// fixed 1.1.1.1 instead of the real address.
func viperTestHackOpts() []func(*plugin.RenderConfig) {
	return []func(*plugin.RenderConfig){
		func(cfg *plugin.RenderConfig) {
			cfg.Viper.Set("test-hack", true)
		},
	}
}

// combineOpts concatenates RenderConfig option groups (e.g. testHackOpts, viperTestHackOpts) into
// a single slice, applied in order by RootCmd.
func combineOpts(groups ...[]func(*plugin.RenderConfig)) []func(*plugin.RenderConfig) {
	var opts []func(*plugin.RenderConfig)
	for _, g := range groups {
		opts = append(opts, g...)
	}
	return opts
}

func executeCMD(t *testing.T, args []string, opts ...func(*plugin.RenderConfig)) (string, string, error) {
	t.Helper()
	cmd := RootCmd(opts...)
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
	// --cpus/--memory: matches the Makefile's e2e-minikube-up sizing, needed for TestE2EParallel's
	// subtests to run concurrently without overwhelming the VM (see that target's comment).
	startMinikube := exec.Command("minikube", "start", "-p", clusterName, "--addons=metrics-server", "--cpus=4", "--memory=6g")
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

func e2eClients(t *testing.T) ([]func(*plugin.RenderConfig), *kubernetes.Clientset, dynamic.Interface) {
	t.Helper()
	hackOpts := testHackOpts(t)
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("failed to get user home directory: %v", err)
		}
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
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	return hackOpts, clientset, dynamicClient
}

// waitForPodScheduleWindow blocks until at least 15s (kstatus's
// sigs.k8s.io/cli-utils/pkg/kstatus/status.ScheduleWindow) have passed since the matching Pod's
// creationTimestamp.
func waitForPodScheduleWindow(t *testing.T, namespace, labelSelector string) {
	t.Helper()
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace, "-l", labelSelector,
		"-o", "jsonpath={.items[0].metadata.creationTimestamp}")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err)
	created, err := time.Parse(time.RFC3339, strings.TrimSpace(string(output)))
	require.NoError(t, err)
	if remaining := time.Until(created.Add(16 * time.Second)); remaining > 0 {
		time.Sleep(remaining)
	}
}

// waitForCrossplaneComposedRefs polls until the XR's spec.crossplane.resourceRefs has at least
// wantCount entries. Used instead of waiting on a Ready condition since the XR under test never
// reaches Ready (one composed child is deliberately unschedulable).
func waitForCrossplaneComposedRefs(t *testing.T, namespace, name string, wantCount int) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", "get", "xstatusprobe", name, "-n", namespace,
			"-o", "jsonpath={.spec.crossplane.resourceRefs}")
		output, err := cmd.CombinedOutput()
		if err == nil {
			var refs []interface{}
			if json.Unmarshal(output, &refs) == nil && len(refs) >= wantCount {
				t.Logf("xstatusprobe %s in namespace %s has %d composed resource refs", name, namespace, len(refs))
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for xstatusprobe %s in namespace %s to have %d composed resource refs", name, namespace, wantCount)
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
		if err != nil {
			t.Logf("warning: failed to delete manifest %s: %v (output: %s)", filepath, err, string(output))
			return
		}
		t.Logf("manifest deleted %s: %s", filepath, string(output))
	})
	require.NoError(t, err)
	t.Logf("applied manifest %s: %s", filepath, string(output))
}

// applyManifestInNamespace is applyManifest, but targets a namespace via `kubectl -n` instead of
// relying on the manifest's own metadata.namespace (or the kubeconfig's default) -- used to give a
// subtest a dedicated namespace without needing a namespace-specific copy of its fixture yaml. The
// manifest's objects must not already set their own metadata.namespace, since that always wins
// over `-n` and would silently defeat the isolation this is for.
func applyManifestInNamespace(t *testing.T, filepath, namespace string) {
	t.Helper()
	filepath = path.Join("..", "tests", filepath)
	cmd := exec.Command("kubectl", "apply", "-n", namespace, "-f", filepath)
	output, err := cmd.CombinedOutput()
	t.Cleanup(func() {
		t.Logf("deleting manifest %s from namespace %s", filepath, namespace)
		cmd := exec.Command("kubectl", "delete", "-n", namespace, "-f", filepath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("warning: failed to delete manifest %s from namespace %s: %v (output: %s)", filepath, namespace, err, string(output))
			return
		}
		t.Logf("manifest deleted %s from namespace %s: %s", filepath, namespace, string(output))
	})
	require.NoError(t, err)
	t.Logf("applied manifest %s to namespace %s: %s", filepath, namespace, string(output))
}

// waitForInNamespace targets a namespace explicitly via `kubectl -n` instead of the kubeconfig's
// default -- pairs with applyManifestInNamespace for subtests moved off the shared default
// namespace.
func waitForInNamespace(t *testing.T, resource, forParam, namespace string) {
	t.Helper()
	cmd := exec.Command("kubectl", "wait", "-n", namespace, "--for", forParam, resource, "--timeout=4m")
	output, err := cmd.CombinedOutput()
	t.Logf("wait result for %s in namespace %s: %s", resource, namespace, string(output))
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
	deadline := time.Now().Add(4 * time.Minute)
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
	deadline := time.Now().Add(4 * time.Minute)
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

// waitForContainerWaitingReasonInNamespace targets a namespace explicitly via `kubectl -n`
// instead of the kubeconfig's default; pass "" to use the kubeconfig's default namespace.
func waitForContainerWaitingReasonInNamespace(t *testing.T, resource, containerName, reason, namespace string) {
	t.Helper()
	jsonpath := fmt.Sprintf(`{.status.containerStatuses[?(@.name=="%s")].state.waiting.reason}`, containerName)
	args := []string{"get", resource, "-o", "jsonpath=" + jsonpath}
	if namespace != "" {
		args = append([]string{"-n", namespace}, args...)
	}
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", args...)
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
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		if err := exec.Command("kubectl", "get", "--raw", rawPath).Run(); err == nil {
			t.Logf("metrics available for pod %s/%s", namespace, name)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for metrics.k8s.io data for pod %s/%s", namespace, name)
}

// waitForContainerMetrics polls metrics.k8s.io until every named container has a recorded cpu
// usage. A pod's PodMetrics can exist while still missing an entry for a container that started
// slightly later than its siblings (more likely under concurrent cluster load): Pod.tmpl's
// container_status_summary only renders a usage line once a container's own entry is present
// with usage.cpu set, so asserting before that leaves the container's line silently blank
// instead of matching a golden fixture.
func waitForContainerMetrics(t *testing.T, namespace, name string, containerNames ...string) {
	t.Helper()
	rawPath := fmt.Sprintf("/apis/metrics.k8s.io/v1beta1/namespaces/%s/pods/%s", namespace, name)
	type containerMetrics struct {
		Name  string `json:"name"`
		Usage struct {
			CPU string `json:"cpu"`
		} `json:"usage"`
	}
	type podMetrics struct {
		Containers []containerMetrics `json:"containers"`
	}
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		output, err := exec.Command("kubectl", "get", "--raw", rawPath).Output()
		if err == nil {
			var m podMetrics
			if json.Unmarshal(output, &m) == nil {
				have := map[string]bool{}
				for _, c := range m.Containers {
					if c.Usage.CPU != "" {
						have[c.Name] = true
					}
				}
				allPresent := true
				for _, want := range containerNames {
					if !have[want] {
						allPresent = false
						break
					}
				}
				if allPresent {
					t.Logf("metrics available for all of %v in pod %s/%s", containerNames, namespace, name)
					return
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for metrics.k8s.io cpu usage for containers %v in pod %s/%s", containerNames, namespace, name)
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

// waitForVPARecommendation polls until a VerticalPodAutoscaler's status.recommendation is
// populated. The recommender needs a window of real usage samples before it computes a first
// recommendation, so this can take roughly a minute after the VPA and its target Pod both exist.
func waitForVPARecommendation(t *testing.T, namespace, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		output, err := exec.Command("kubectl", "get", "vpa", name, "-n", namespace,
			"-o", "jsonpath={.status.recommendation.containerRecommendations[0].target.cpu}").CombinedOutput()
		if err == nil && strings.TrimSpace(string(output)) != "" {
			t.Logf("VPA %s/%s has a recommendation: %s", namespace, name, strings.TrimSpace(string(output)))
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out waiting for VPA %s/%s to compute a recommendation", namespace, name)
}

// waitForVPAPodsMatched polls until a VerticalPodAutoscaler's NoPodsMatched condition is gone
// (or already False). The VPA controller re-evaluates this independently of, and can lag behind,
// the target Deployment's own Available condition -- right after the updater recreates the Pod,
// the VPA can still be reporting stale NoPodsMatched=True for a beat, which the golden fixture
// doesn't expect to see at all.
func waitForVPAPodsMatched(t *testing.T, namespace, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		output, err := exec.Command("kubectl", "get", "vpa", name, "-n", namespace,
			"-o", `jsonpath={.status.conditions[?(@.type=="NoPodsMatched")].status}`).CombinedOutput()
		if err == nil && strings.TrimSpace(string(output)) != "True" {
			t.Logf("VPA %s/%s no longer reports NoPodsMatched", namespace, name)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for VPA %s/%s to clear its NoPodsMatched condition", namespace, name)
}

// waitForPodRecreated polls until the Pod matching labelSelector is no longer originalPodName --
// evidence the VPA updater actually evicted/recreated it to apply the recommendation, not just
// computed one that nobody applied.
func waitForPodRecreated(t *testing.T, namespace, labelSelector, originalPodName string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		output, err := exec.Command("kubectl", "get", "pods", "-n", namespace, "-l", labelSelector,
			"-o", "jsonpath={.items[*].metadata.name}").CombinedOutput()
		if err == nil {
			for _, name := range strings.Fields(string(output)) {
				if name != originalPodName {
					t.Logf("VPA updater recreated the pod: %s -> %s", originalPodName, name)
					return
				}
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out waiting for pod matching %s in namespace %s to be recreated (still %s)",
		labelSelector, namespace, originalPodName)
}

// ---------------------------------------------------------------------------
// Per-scenario cluster dependency installs (see #720).
//
// Each topical group installs whatever cluster prerequisites *it* needs (cert-manager, Gateway
// API CRDs, Cilium/Calico CRDs, VPA, Crossplane) instead of everything being installed
// unconditionally by `make install-e2e-deps` before any test runs. metrics-server is the one
// exception left as a Makefile step: it must be available before TestE2EParallel's pool starts
// (see that function's doc comment), not merely before whichever group happens to use it.
//
// A dependency used by more than one topical group (Gateway API CRDs: service-routing and
// tls-validation) needs to install exactly once across the whole run, not once per group -- each
// onceInstaller below is a package-level singleton shared by every caller, guarded by
// sync.Once. The error from that single install attempt is cached and replayed to every caller
// (including ones after the first) rather than only failing the subtest that happened to trigger
// the install: sync.Once.Do still marks itself done even if its function calls t.FailNow
// (testify's require does, via runtime.Goexit), so the install closures below stay t-free and
// return a plain error instead.
// ---------------------------------------------------------------------------

type onceInstaller struct {
	once sync.Once
	err  error
}

func (o *onceInstaller) ensure(t *testing.T, install func() error) {
	t.Helper()
	o.once.Do(func() {
		o.err = install()
	})
	require.NoError(t, o.err)
}

var (
	versionsEnvOnce sync.Once
	versionsEnv     map[string]string
	versionsEnvErr  error
)

// loadVersionsEnv parses hack/versions.env's plain VAR=value lines, the same file
// hack/generate-screenshots.sh sources, so both stay pinned to the same versions.
func loadVersionsEnv() (map[string]string, error) {
	versionsEnvOnce.Do(func() {
		data, err := os.ReadFile(path.Join("..", "hack", "versions.env"))
		if err != nil {
			versionsEnvErr = err
			return
		}
		vals := map[string]string{}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if k, v, ok := strings.Cut(line, "="); ok {
				vals[k] = v
			}
		}
		versionsEnv = vals
	})
	return versionsEnv, versionsEnvErr
}

func versionsEnvValue(key string) (string, error) {
	vals, err := loadVersionsEnv()
	if err != nil {
		return "", err
	}
	v, ok := vals[key]
	if !ok {
		return "", fmt.Errorf("missing %s in hack/versions.env", key)
	}
	return v, nil
}

var gatewayAPICRDsInstaller onceInstaller

// ensureGatewayAPICRDs installs the Gateway API CRDs (experimental channel), needed by both
// runServiceRoutingSubtests and runTLSValidationSubtests. CRDs only: kubectl-status only reads/
// matches these objects client-side, it never relies on a real Gateway controller reconciling
// them. Experimental channel is a superset of standard and adds TCPRoute/UDPRoute/
// BackendTLSPolicy/ListenerSet, which some e2e scenarios also render. --server-side: the
// experimental bundle's CRDs (e.g. HTTPRoute) are large enough that client-side apply's
// kubectl.kubernetes.io/last-applied-configuration annotation trips the 262144-byte annotation
// limit; server-side apply doesn't need that annotation.
func ensureGatewayAPICRDs(t *testing.T) {
	t.Helper()
	gatewayAPICRDsInstaller.ensure(t, func() error {
		version, err := versionsEnvValue("GATEWAY_API_VERSION")
		if err != nil {
			return err
		}
		url := fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/experimental-install.yaml", version)
		output, err := exec.Command("kubectl", "apply", "--server-side", "-f", url).CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl apply gateway-api CRDs: %w: %s", err, output)
		}
		return nil
	})
}

var certManagerInstaller onceInstaller

// ensureCertManager installs cert-manager, needed by runTLSValidationSubtests. Versions are
// pinned in hack/versions.env (shared with hack/generate-screenshots.sh); bump them there
// periodically.
func ensureCertManager(t *testing.T) {
	t.Helper()
	certManagerInstaller.ensure(t, func() error {
		version, err := versionsEnvValue("CERT_MANAGER_VERSION")
		if err != nil {
			return err
		}
		url := fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", version)
		if output, err := exec.Command("kubectl", "apply", "-f", url).CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl apply cert-manager.yaml: %w: %s", err, output)
		}
		output, err := exec.Command("kubectl", "wait", "--for=condition=Available", "--timeout=300s",
			"deployment", "--all", "-n", "cert-manager").CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl wait cert-manager deployments: %w: %s", err, output)
		}
		return nil
	})
}

var ciliumCalicoCRDsInstaller onceInstaller

// ensureCiliumCalicoCRDs installs the CiliumNetworkPolicy/CiliumClusterwideNetworkPolicy and
// Calico NetworkPolicy/GlobalNetworkPolicy CRDs, needed by runNetworkPolicySubtests.
// kubectl-status only reads and matches these objects client-side (selector-vs-Pod-labels), it
// never relies on Cilium/Calico actually enforcing traffic, so the CRDs alone (no Cilium/Calico
// installed as CNI) are enough to exercise the e2e scenarios -- same "CRDs only" reasoning as
// cert-manager/Gateway API above. Calico's own NetworkPolicy/GlobalNetworkPolicy are served
// under crd.projectcalico.org/v1 (the Kubernetes-datastore storage CRDs), not the
// projectcalico.org/v3 API calicoctl/the Calico API server present -- that's the group
// kubectl-status's KubeGetCalico*MatchingPod helpers query. --server-side: these CRDs' embedded
// OpenAPI schemas are large enough to trip the same client-side last-applied-configuration
// annotation limit as HTTPRoute above.
func ensureCiliumCalicoCRDs(t *testing.T) {
	t.Helper()
	ciliumCalicoCRDsInstaller.ensure(t, func() error {
		urls := []string{
			"https://raw.githubusercontent.com/cilium/cilium/v1.19.5/pkg/k8s/apis/cilium.io/client/crds/v2/ciliumnetworkpolicies.yaml",
			"https://raw.githubusercontent.com/cilium/cilium/v1.19.5/pkg/k8s/apis/cilium.io/client/crds/v2/ciliumclusterwidenetworkpolicies.yaml",
			"https://raw.githubusercontent.com/projectcalico/calico/v3.32.1/manifests/crds.yaml",
		}
		for _, url := range urls {
			output, err := exec.Command("kubectl", "apply", "--server-side", "-f", url).CombinedOutput()
			if err != nil {
				return fmt.Errorf("kubectl apply %s: %w: %s", url, err, output)
			}
		}
		return nil
	})
}

var vpaInstaller onceInstaller

// ensureVPA installs VerticalPodAutoscaler, needed by TestE2EDynamicManifests' VPA subtest.
// Unlike the CRD-only installers above, that scenario exercises it actually acting (the updater
// evicting/recreating a Pod to apply a recommendation), so its controllers (recommender/updater/
// admission-controller) need to run for real, not just the CRDs. The upstream project has no
// plain `kubectl apply` release bundle (its install script generates webhook certs locally), so
// this uses the cowboysysop community Helm chart instead.
func ensureVPA(t *testing.T) {
	t.Helper()
	vpaInstaller.ensure(t, func() error {
		if output, err := exec.Command("helm", "repo", "add", "cowboysysop", "https://cowboysysop.github.io/charts/").CombinedOutput(); err != nil {
			return fmt.Errorf("helm repo add cowboysysop: %w: %s", err, output)
		}
		if output, err := exec.Command("helm", "repo", "update", "cowboysysop").CombinedOutput(); err != nil {
			return fmt.Errorf("helm repo update cowboysysop: %w: %s", err, output)
		}
		if output, err := exec.Command("helm", "upgrade", "--install", "vpa", "cowboysysop/vertical-pod-autoscaler",
			"--version", "11.1.1", "-n", "kube-system", "--wait", "--timeout", "5m").CombinedOutput(); err != nil {
			return fmt.Errorf("helm upgrade vpa: %w: %s", err, output)
		}
		output, err := exec.Command("kubectl", "wait", "--for=condition=Available", "--timeout=120s",
			"deployment", "-l", "app.kubernetes.io/instance=vpa", "-n", "kube-system").CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl wait vpa deployments: %w: %s", err, output)
		}
		return nil
	})
}

var crossplaneInstaller onceInstaller

// ensureCrossplane installs Crossplane core plus the two Composition Functions the e2e test
// Composition needs, required by TestE2EDynamicManifests' Crossplane subtest. That scenario
// exercises a real XR composing real children (a Composition Function renders them and derives
// readiness), not just kubectl-status reading static CRDs, so Crossplane needs to actually
// reconcile -- same "controller must actually run" reasoning as VPA above. No cloud provider is
// installed: the test Composition composes plain in-cluster Kubernetes resources (ConfigMap/
// Deployment), which Crossplane v2 supports natively. function-patch-and-transform renders the
// test Composition's child resources, function-auto-ready derives the XR's readiness from them.
// Their versions are pinned in the manifest itself (not hack/versions.env) since they're only
// used here.
func ensureCrossplane(t *testing.T) {
	t.Helper()
	crossplaneInstaller.ensure(t, func() error {
		version, err := versionsEnvValue("CROSSPLANE_VERSION")
		if err != nil {
			return err
		}
		if output, err := exec.Command("helm", "repo", "add", "crossplane-stable", "https://charts.crossplane.io/stable").CombinedOutput(); err != nil {
			return fmt.Errorf("helm repo add crossplane-stable: %w: %s", err, output)
		}
		if output, err := exec.Command("helm", "repo", "update", "crossplane-stable").CombinedOutput(); err != nil {
			return fmt.Errorf("helm repo update crossplane-stable: %w: %s", err, output)
		}
		if output, err := exec.Command("helm", "upgrade", "--install", "crossplane", "crossplane-stable/crossplane",
			"--version", version, "-n", "crossplane-system", "--create-namespace", "--wait", "--timeout", "5m").CombinedOutput(); err != nil {
			return fmt.Errorf("helm upgrade crossplane: %w: %s", err, output)
		}
		functionsManifest := path.Join("..", "tests", "e2e-artifacts", "crossplane-functions.yaml")
		if output, err := exec.Command("kubectl", "apply", "-f", functionsManifest).CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl apply %s: %w: %s", functionsManifest, err, output)
		}
		output, err := exec.Command("kubectl", "wait", "--for=condition=Healthy", "--timeout=180s",
			"function.pkg.crossplane.io", "--all").CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl wait crossplane functions: %w: %s", err, output)
		}
		return nil
	})
}
