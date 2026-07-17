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
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
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

func TestRootCmdWithoutACluster(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	opts := []func(*plugin.RenderConfig){
		func(cfg *plugin.RenderConfig) {
			cfg.DurationRound = func(_ interface{}) string { return "1m" }
		},
	}
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
			tt.assert(t, nil, opts...)
		})
	}
}

// TestDeepRespectsExplicitIncludeFalse guards a fix to --deep: it used to force every
// --include-* flag to true unconditionally (enableAllIncludes), silently overriding an explicit
// --include-events=false/--include-managed-fields=false passed alongside it on the same command
// line -- exactly the flags the e2e "--deep" test cases rely on to keep their fixtures
// deterministic. --deep now only fills in flags the user didn't set explicitly, mirroring how
// --shallow already respects an explicit --include-*=true. This is a pure viper/pflag check, no
// live cluster or even a real render involved, so it runs unconditionally.
func TestDeepRespectsExplicitIncludeFalse(t *testing.T) {
	v := viper.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	addRenderFlags(flags)
	require.NoError(t, v.BindPFlags(flags))
	require.NoError(t, flags.Parse([]string{"--deep", "--include-events=false", "--include-managed-fields=false"}))

	require.True(t, v.GetBool("deep"))
	enableAllIncludes(v)

	assert.False(t, v.GetBool("include-events"), "explicit --include-events=false must survive --deep")
	assert.False(t, v.GetBool("include-managed-fields"), "explicit --include-managed-fields=false must survive --deep")
	assert.True(t, v.GetBool("include-owners"), "--deep must still enable flags the user didn't set explicitly")
}

// TestE2ERegexFixturesAreAnchored guards the whole-output convention documented in
// CONTRIBUTING.md: a fixture under tests/e2e-artifacts/ either pins the full rendered output,
// anchored at both ends with `\A`...`\z`, or is a deliberately partial one-off-lines match with
// neither anchor. A fixture with only one of the two anchors is neither -- almost always a fixture
// that was meant to be anchored but lost its `\z` (or gained a stray `\A`) while being edited, so
// it silently stopped verifying the parts of the output past/before the missing anchor. This
// doesn't require a live cluster, so it runs unconditionally.
func TestE2ERegexFixturesAreAnchored(t *testing.T) {
	fixtures, err := filepath.Glob("../tests/e2e-artifacts/*.regex")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			content, err := os.ReadFile(fixture)
			require.NoError(t, err)
			startsAnchored := bytes.HasPrefix(content, []byte(`\A`))
			endsAnchored := bytes.HasSuffix(content, []byte(`\z`))
			if startsAnchored != endsAnchored {
				t.Errorf("%s: has `\\A` at the start=%v but `\\z` at the end=%v -- a whole-output "+
					"fixture needs both anchors (see CONTRIBUTING.md), a partial one-off-lines "+
					"fixture needs neither; use the --include-* flags to trim sections you don't "+
					"want to pin instead of matching only part of the output",
					fixture, startsAnchored, endsAnchored)
			}
		})
	}
}

func TestE2EAgainstVanillaMinikube(t *testing.T) {
	e2eMinikubeTest(t)
	hackOpts := testHackOpts(t)
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
			args:            []string{"pods", "-n", "kube-system", "--include-events=false", "--include-managed-fields=false"},
			stdoutRegexPath: "e2e-artifacts/pods-kube-system.regex",
		},
		{
			name:            "node query should return at least a node",
			args:            []string{"node", "--include-events=false", "--include-managed-fields=false"},
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
			tt.assert(t, nil, combineOpts(hackOpts, viperTestHackOpts())...)
		})
	}
}

// testHackOpts fixes plugin.RenderConfig's Now/DurationRound/StartedAfterClause for
// deterministic e2e output. Each RootCmd() invocation gets its own fresh RenderConfig (see
// cmd/main.go), so unlike the old global package-var overrides this needs no revert -- see #694.
func testHackOpts(t *testing.T) []func(*plugin.RenderConfig) {
	t.Helper()
	fixedNow := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	return []func(*plugin.RenderConfig){
		func(cfg *plugin.RenderConfig) {
			cfg.DurationRound = func(_ interface{}) string { return "1m" }
		},
		func(cfg *plugin.RenderConfig) {
			cfg.Now = func() time.Time { return fixedNow }
		},
		// Whether a live pod's creation and kubelet-acknowledge timestamps land in the same
		// wall-clock second (hiding the "started after" clause) or not (showing it) is a coin
		// flip e2e tests can't control -- both timestamps only carry 1-second resolution over
		// the wire. Force the clause present whenever Status.startTime is set, so fixtures can
		// pin it literally instead of making it optional.
		func(cfg *plugin.RenderConfig) {
			cfg.StartedAfterClause = func(_, _ string) string { return ", started after 1m" }
		},
	}
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

func TestAllArtifactsLocal(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	opts := combineOpts(testHackOpts(t), viperTestHackOpts())
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
			test.assert(t, nil, opts...) // to update the out files check /tests/artifacts/README.md
		})
	}
}

func TestAllArtifactsLocalWithIncludeAllVolumes(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	opts := combineOpts(testHackOpts(t), viperTestHackOpts())
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
			test.assert(t, nil, opts...)
		})
	}
}

func TestAllArtifactsLocalWithAbsoluteTime(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	opts := combineOpts(viperTestHackOpts())
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
			test.assert(t, nil, opts...)
		})
	}
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

// TestE2EParallel is a dedicated home for e2e subtests that are independent of each other and can
// therefore run concurrently. RootCmd (cmd/main.go) and pkg/plugin no longer read a process-global
// viper singleton or package-level Now/DurationRound/StartedAfterClause overrides -- each RootCmd()
// call owns its own *viper.Viper and plugin.RenderConfig (see #694), and testHackOpts/
// viperTestHackOpts just build option values rather than mutating shared state, so calling them
// from concurrent subtests is safe. The two remaining process-global sinks on the render path --
// cmdutil.BehaviorOnFatal in RootCmd's RunE and slog.SetDefault in newRenderEngine's
// setupDeprecationFilter -- are also now safe under concurrent RootCmd().Execute() calls: the
// former is guarded by cmd/main.go's fatalMu, held only around installing/consuming the handler
// rather than around the render itself; the latter installs its filtering handler once per
// process (sync.Once) instead of rebinding it on every render (see #701). A subtest qualifies for
// t.Parallel() once it:
//   - needs no namespace, or creates/uses a namespace dedicated to that subtest (never `default`,
//     and never a namespace another subtest might also touch)
//   - never relies on a fixed cluster-scoped resource name (Node, CustomResourceDefinition,
//     ClusterRole, ...) another subtest could also use -- generate one instead, e.g. with
//     GenerateName (see createBadNode)
//
// Add a qualifying subtest with t.Run(name, func(t *testing.T) { t.Parallel(); ... }) so it
// actually runs alongside its siblings instead of just living next to them; that subtest-level
// t.Parallel() is what makes siblings run concurrently, regardless of this function's own.
//
// This function itself must NOT call t.Parallel(): e2eMinikubeTest below falls back to
// startMinikube, which calls t.Setenv("KUBECONFIG", ...) for ad hoc `go test -run TestE2E...` runs
// that don't set ASSUME_MINIKUBE_IS_CONFIGURED=true -- and t.Setenv panics if called on a test
// already marked parallel.
func TestE2EParallel(t *testing.T) {
	e2eMinikubeTest(t)
	hackOpts, clientset, dynamicClient := e2eClients(t)
	t.Run("owners should be included with deep", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-owner-secret"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		owner := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner",
				Namespace: ns,
			},
		}
		owner, err = clientset.CoreV1().Secrets(ns).Create(context.TODO(), owner, metav1.CreateOptions{})
		t.Cleanup(func() {
			clientset.CoreV1().Secrets(ns).Delete(context.TODO(), "owner", metav1.DeleteOptions{})
		})
		require.NoError(t, err)
		uid := owner.GetUID()
		t.Logf("owner secret is created, uid is %s", uid)
		// Create the child secret with owner reference
		child := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "child",
				Namespace: ns,
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
		_, err = clientset.CoreV1().Secrets(ns).Create(context.TODO(), child, metav1.CreateOptions{})
		t.Log("child secret is created")
		defer clientset.CoreV1().Secrets(ns).Delete(context.TODO(), "child", metav1.DeleteOptions{})
		require.NoError(t, err)

		test := cmdTest{
			args: []string{"secret/child", "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "7"},
			// Secret.tmpl intentionally omits kstatus_summary (Secret is always reported
			// "Resource is always ready" by kstatus, so the "Current:" line is redundant
			// noise) -- see tests/artifacts/secret-tls-healthy.out for the same committed
			// expectation.
			stdoutRegexPath: "e2e-artifacts/secret-child-with-owner.regex",
		}
		test.assert(t, nil, opts...) // to update the out files check /tests/artifacts/README.md
	})
	t.Run("ownerReference pointing at a deleted owner is flagged as orphan", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		// The child is rendered with --local straight from a manifest rather than created on
		// the cluster: a live Secret with a dangling ownerReference gets swept up by the
		// built-in garbage collector almost immediately (it treats a missing owner as a signal
		// to cascade-delete the dependent), which would make this test flaky. --local still
		// resolves the ownerReference against the real API server (only the child object itself
		// is local), so the orphan check is exercised the same way, without the race.
		cmdTest{
			args:            []string{"-f", "../tests/e2e-artifacts/secret-orphan-owner-reference.yaml", "--local", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/secret-orphan-owner-reference.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod on a cordoned node with an untolerated taint and a bad condition", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-bad-node-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		nodeName := createBadNode(t, clientset)

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-on-bad-node",
				Namespace: ns,
			},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"pod/pod-on-bad-node", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-on-bad-node.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod's serviceAccountName resolves to the ServiceAccount and surfaces automount/imagePullSecrets", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(viperTestHackOpts())
		ns := "e2e-pod-custom-sa"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		f := false
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kubectl-status-test-sa",
				Namespace: ns,
			},
			AutomountServiceAccountToken: &f,
			ImagePullSecrets:             []corev1.LocalObjectReference{{Name: "regcred"}},
		}
		_, err = clientset.CoreV1().ServiceAccounts(ns).Create(context.TODO(), sa, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().ServiceAccounts(ns).Delete(context.TODO(), sa.Name, metav1.DeleteOptions{})

		// The ServiceAccount admission plugin merges its imagePullSecrets onto every Pod that
		// uses it, so Pod.tmpl's own (pre-existing) imagePullSecrets check will flag "regcred" as
		// missing unless it actually exists with the expected type.
		regcred := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "regcred", Namespace: ns},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{".dockerconfigjson": []byte("{}")},
		}
		_, err = clientset.CoreV1().Secrets(ns).Create(context.TODO(), regcred, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Secrets(ns).Delete(context.TODO(), regcred.Name, metav1.DeleteOptions{})

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-custom-sa",
				Namespace: ns,
			},
			Spec: corev1.PodSpec{
				ServiceAccountName: sa.Name,
				Containers:         []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"pod/pod-with-custom-sa", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-with-custom-sa.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod referencing a missing ServiceAccount surfaces a doesn't-exist warning", func(t *testing.T) {
		t.Parallel()
		// Rendered with --local (rather than created on the cluster) since a real cluster's
		// ServiceAccount admission plugin rejects a Pod at creation time when its
		// serviceAccountName doesn't resolve -- --local still resolves the reference against the
		// real API server (only the Pod object itself is local), so the not-found check is
		// exercised the same way, without needing admission to allow the invalid Pod through.
		opts := combineOpts(viperTestHackOpts())
		cmdTest{
			args:            []string{"-f", "../tests/e2e-artifacts/pod-missing-service-account.yaml", "--local", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-missing-service-account.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("workload's matching pod on a cordoned node surfaces a compact node-problem flag", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-bad-node-rs"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		nodeName := createBadNode(t, clientset)

		// The Pod's spec.nodeName is set directly at creation, bypassing the scheduler, so it
		// never actually runs -- ReplicaSet.tmpl's selector-based pod lookup only needs matching
		// labels, not real ownership, to include it in the health summary.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-on-bad-node-for-rs",
				Namespace: ns,
				Labels:    map[string]string{"app": "kubectl-status-test-bad-rs"},
			},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		one := int32(1)
		rs := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bad-rs",
				Namespace: ns,
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
		_, err = clientset.AppsV1().ReplicaSets(ns).Create(context.TODO(), rs, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.AppsV1().ReplicaSets(ns).Delete(context.TODO(), rs.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"rs/bad-rs", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-on-bad-node-for-rs.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod selected by a NetworkPolicy surfaces the compact isolation signal", func(t *testing.T) {
		t.Parallel()
		// A dedicated namespace keeps this test in control of exactly which NetworkPolicy
		// objects exist -- an empty podSelector elsewhere in a shared namespace (e.g. "default")
		// would also match this Pod and make the asserted policy name/count non-deterministic.
		opts := combineOpts(hackOpts, viperTestHackOpts())
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

		// The full-output regex fixture below pins the Pod to a Running/Ready state, so this
		// must wait rather than race the kubelet -- otherwise the render can catch it Pending.
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
			"pod/netpol-selected-pod", "-n", ns, "--timeout=4m").Run())

		cmdTest{
			args:            []string{"pod/netpol-selected-pod", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-selected-by-network-policy.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod selected by multiple NetworkPolicies lists all of them and unions both directions", func(t *testing.T) {
		t.Parallel()
		// Same isolation rationale as the single-policy case above, but with three policies that
		// each cover only part of the picture -- a default-deny (both directions, no rules), an
		// egress-only allow, and an ingress-only allow -- to exercise the union across matching
		// policies (Kubernetes NetworkPolicy is additive/OR'd, never a single winning policy) and
		// the multi-name/plural "NetworkPolicies" wording. Names are chosen to sort the same way
		// alphabetically as they're created below: the underlying list (server-side, not an
		// informer cache) comes back name-ordered, and creationTimestamp -- the only explicit
		// sort key applied -- has second granularity, so objects created in the same second (as
		// these three are) keep that name order rather than creation order.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-netpol-multi-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		podLabels := map[string]string{"app": "kubectl-status-test-netpol-multi-target"}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netpol-multi-selected-pod",
				Namespace: ns,
				Labels:    podLabels,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "busybox", Command: []string{"sleep", "infinity"}}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		defaultDeny := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "default-deny-both", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			},
		}
		_, err = clientset.NetworkingV1().NetworkPolicies(ns).Create(context.TODO(), defaultDeny, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.NetworkingV1().NetworkPolicies(ns).Delete(context.TODO(), defaultDeny.Name, metav1.DeleteOptions{})

		egressOnly := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "egress-only", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
			},
		}
		_, err = clientset.NetworkingV1().NetworkPolicies(ns).Create(context.TODO(), egressOnly, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.NetworkingV1().NetworkPolicies(ns).Delete(context.TODO(), egressOnly.Name, metav1.DeleteOptions{})

		ingressOnly := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "ingress-only", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
			},
		}
		_, err = clientset.NetworkingV1().NetworkPolicies(ns).Create(context.TODO(), ingressOnly, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.NetworkingV1().NetworkPolicies(ns).Delete(context.TODO(), ingressOnly.Name, metav1.DeleteOptions{})

		// The full-output regex fixture below pins the Pod to a Running/Ready state, so this
		// must wait rather than race the kubelet -- otherwise the render can catch it Pending.
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
			"pod/netpol-multi-selected-pod", "-n", ns, "--timeout=4m").Run())

		cmdTest{
			args:            []string{"pod/netpol-multi-selected-pod", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-selected-by-multiple-network-policies.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod selected by CiliumNetworkPolicy/CiliumClusterwideNetworkPolicy and Calico NetworkPolicy/GlobalNetworkPolicy surfaces each compact signal", func(t *testing.T) {
		t.Parallel()
		// These CRDs are only installed standalone (via install-e2e-deps), without Cilium or
		// Calico actually running as the cluster's CNI -- kubectl-status only ever matches these
		// objects' selectors against the Pod's own labels client-side, it never depends on either
		// CNI actually enforcing traffic, so the CRDs alone are enough to exercise this.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-cni-policy-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		appLabel := "kubectl-status-test-cni-policy-target"
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cni-policy-selected-pod",
				Namespace: ns,
				Labels:    map[string]string{"app": appLabel},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "busybox", Command: []string{"sleep", "infinity"}}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cnpGVR := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
		cnp := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumNetworkPolicy",
			"metadata":   map[string]interface{}{"name": "cnp-ingress", "namespace": ns},
			"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": appLabel}},
				"ingress":          []interface{}{map[string]interface{}{}},
			},
		}}
		_, err = dynamicClient.Resource(cnpGVR).Namespace(ns).Create(context.TODO(), cnp, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(cnpGVR).Namespace(ns).Delete(context.TODO(), cnp.GetName(), metav1.DeleteOptions{})

		ccnpGVR := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
		ccnp := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumClusterwideNetworkPolicy",
			"metadata":   map[string]interface{}{"name": "ccnp-egress-" + ns},
			"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": appLabel}},
				"egress":           []interface{}{map[string]interface{}{}},
			},
		}}
		_, err = dynamicClient.Resource(ccnpGVR).Create(context.TODO(), ccnp, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(ccnpGVR).Delete(context.TODO(), ccnp.GetName(), metav1.DeleteOptions{})

		calicoNpGVR := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "networkpolicies"}
		calicoNp := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "crd.projectcalico.org/v1",
			"kind":       "NetworkPolicy",
			"metadata":   map[string]interface{}{"name": "calico-np-ingress", "namespace": ns},
			"spec": map[string]interface{}{
				"selector": fmt.Sprintf("app == '%s'", appLabel),
				"types":    []interface{}{"Ingress"},
			},
		}}
		_, err = dynamicClient.Resource(calicoNpGVR).Namespace(ns).Create(context.TODO(), calicoNp, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(calicoNpGVR).Namespace(ns).Delete(context.TODO(), calicoNp.GetName(), metav1.DeleteOptions{})

		calicoGnpGVR := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"}
		calicoGnp := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "crd.projectcalico.org/v1",
			"kind":       "GlobalNetworkPolicy",
			"metadata":   map[string]interface{}{"name": "calico-gnp-egress-" + ns},
			"spec": map[string]interface{}{
				"selector":          fmt.Sprintf("app == '%s'", appLabel),
				"namespaceSelector": fmt.Sprintf("projectcalico.org/name == '%s'", ns),
				"types":             []interface{}{"Egress"},
			},
		}}
		_, err = dynamicClient.Resource(calicoGnpGVR).Create(context.TODO(), calicoGnp, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(calicoGnpGVR).Delete(context.TODO(), calicoGnp.GetName(), metav1.DeleteOptions{})

		// The full-output regex fixture below pins the Pod to a Running/Ready state, so this
		// must wait rather than race the kubelet -- otherwise the render can catch it Pending.
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
			"pod/cni-policy-selected-pod", "-n", ns, "--timeout=4m").Run())

		cmdTest{
			args:            []string{"pod/cni-policy-selected-pod", "-n", ns, "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-selected-by-cilium-and-calico-network-policies.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("deployment rollout with --include-rollout-diffs shows the diff between revisions", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-rollout-diff"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		name := "rollout-diff-test"
		one := int32(1)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
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
		_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.AppsV1().Deployments(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
		waitForInNamespace(t, "deployment/"+name, "condition=Available", ns)

		// Update the image so a second ReplicaSet revision is created, giving --include-rollout-diffs
		// something to diff.
		dep, err = clientset.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
		require.NoError(t, err)
		dep.Spec.Template.Spec.Containers[0].Image = "nginx:1.26"
		_, err = clientset.AppsV1().Deployments(ns).Update(context.TODO(), dep, metav1.UpdateOptions{})
		require.NoError(t, err)
		rolloutCmd := exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
		output, err := rolloutCmd.CombinedOutput()
		t.Logf("rollout status for %s: %s", name, output)
		require.NoError(t, err)

		// The order in which the two ReplicaSet revisions are diffed (and so which side
		// gets "-" vs "+") isn't guaranteed, so the fixture alternates both directions.
		cmdTest{
			args:            []string{"deployment/" + name, "-n", ns, "--include-rollout-diffs", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/rollout-diff.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("Rollouts section shows a single blocked rollout even without a second one to compare against", func(t *testing.T) {
		t.Parallel()
		// #213: the Rollouts list used to be suppressed unless there were 2+ rollouts to
		// compare, hiding a stuck or unhealthy first/only rollout. It should now also show up
		// for a single rollout that isn't done yet.
		badImage := "kubectl-status-e2e-this-image-does-not-exist"

		t.Run("deployment", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-blocked-deployment"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-blocked-deployment"
			one := int32(1)
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.DeploymentSpec{
					Replicas: &one,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: badImage}}},
					},
				},
			}
			_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().Deployments(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			podName := waitForPodByLabel(t, ns, "app="+name)
			waitForContainerWaitingReasonInNamespace(t, "pod/"+podName, "app", "ImagePullBackOff", ns)

			cmdTest{
				args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-deployment.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("statefulset", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-blocked-statefulset"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-blocked-statefulset"
			one := int32(1)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
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
			_, err = clientset.AppsV1().StatefulSets(ns).Create(context.TODO(), sts, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().StatefulSets(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			waitForContainerWaitingReasonInNamespace(t, "pod/"+name+"-0", "app", "ImagePullBackOff", ns)

			cmdTest{
				args:            []string{"statefulset/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-statefulset.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("daemonset", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-blocked-daemonset"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-blocked-daemonset"
			ds := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.DaemonSetSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: badImage}}},
					},
				},
			}
			_, err = clientset.AppsV1().DaemonSets(ns).Create(context.TODO(), ds, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().DaemonSets(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			podName := waitForPodByLabel(t, ns, "app="+name)
			waitForContainerWaitingReasonInNamespace(t, "pod/"+podName, "app", "ImagePullBackOff", ns)

			cmdTest{
				args:            []string{"daemonset/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-daemonset.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("healthy single rollout stays suppressed", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-healthy-deployment"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-healthy-deployment"
			one := int32(1)
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.DeploymentSpec{
					Replicas: &one,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}}},
					},
				},
			}
			_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().Deployments(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			waitForInNamespace(t, "deployment/"+name, "condition=Available", ns)

			cmdTest{
				args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-healthy-deployment.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("three healthy revisions with --include-rollout-diffs shows both consecutive diffs", func(t *testing.T) {
			// Needs two distinct spec changes (three revisions total) before the check, so
			// there are two consecutive pairs to diff, not just the one covered by the
			// "--include-rollout-diffs shows the diff between revisions" test above.
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-three-revisions"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-three-revisions"
			applyManifestInNamespace(t, "e2e-artifacts/rollouts-three-revisions.yaml", ns)
			waitForInNamespace(t, "deployment/"+name, "condition=Available", ns)

			out, err := exec.Command("kubectl", "set", "image", "deployment/"+name, "nginx=nginx:1.26", "-n", ns).CombinedOutput()
			require.NoError(t, err, string(out))
			rolloutCmd := exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
			output, err := rolloutCmd.CombinedOutput()
			t.Logf("rollout status for %s (nginx:1.26): %s", name, output)
			require.NoError(t, err)
			waitForSinglePod(t, ns, "app="+name)

			out, err = exec.Command("kubectl", "set", "image", "deployment/"+name, "nginx=nginx:1.27", "-n", ns).CombinedOutput()
			require.NoError(t, err, string(out))
			rolloutCmd = exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
			output, err = rolloutCmd.CombinedOutput()
			t.Logf("rollout status for %s (nginx:1.27): %s", name, output)
			require.NoError(t, err)
			waitForSinglePod(t, ns, "app="+name)

			cmdTest{
				args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--include-rollout-diffs", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-three-revisions-with-diffs.regex",
			}.assert(t, nil, opts...)
		})
	})
	t.Run("sts-with-ingress", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-sts-with-ingress"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		// using sts here as the pod name is predictable in that case, not true for deployments and ds
		applyManifestInNamespace(t, "e2e-artifacts/sts-with-ingress.yaml", ns)
		waitForInNamespace(t, "sts/sts-with-ingress", "jsonpath={.status.readyReplicas}=1", ns)
		cmdTest{
			// Log/volume usage bytes come from live kubelet stats and aren't reproducible
			// across runs, so this is matched as a regex rather than exact text.
			args:            []string{"pod/sts-with-ingress-0", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-ingress.pod.regex",
		}.assert(t, nodeNameModifier, opts...)
		cmdTest{
			args:            []string{"service/sts-with-ingress", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-ingress.service.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"service/sts-with-ingress", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-ingress.service-deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("sts-with-ingress-routes", func(t *testing.T) {
		t.Parallel()
		// Builds on sts-with-ingress above: adds a Gateway/HTTPRoute/TCPRoute targeting the
		// same Service, so its "Routes matching this Service" section shows up alongside the
		// Ingress already covered there.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-sts-with-ingress-routes"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/sts-with-ingress.yaml", ns)
		applyManifestInNamespace(t, "e2e-artifacts/sts-with-ingress-routes.yaml", ns)
		waitForInNamespace(t, "sts/sts-with-ingress", "jsonpath={.status.readyReplicas}=1", ns)
		cmdTest{
			args:            []string{"service/sts-with-ingress", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-ingress-routes.regex",
		}.assert(t, nodeNameModifier, opts...)
		cmdTest{
			args:            []string{"service/sts-with-ingress", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-ingress-routes.deep.regex",
		}.assert(t, nodeNameModifier, opts...)
	})
	t.Run("svc-with-httproute", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-svc-httproute"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/svc-with-httproute.yaml", ns)
		cmdTest{
			args:            []string{"service/svc-with-httproute", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/svc-with-httproute.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"service/svc-with-httproute", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/svc-with-httproute.deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("sts-with-nodeport", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-sts-nodeport"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		// using sts here as the pod name is predictable in that case, not true for deployments and ds
		applyManifestInNamespace(t, "e2e-artifacts/sts-with-nodeport.yaml", ns)
		waitForInNamespace(t, "sts/sts-with-nodeport", "jsonpath={.status.readyReplicas}=1", ns)
		waitForInNamespace(t, "pdb/sts-with-nodeport", "jsonpath={.status.currentHealthy}=1", ns)
		cmdTest{
			// Log/volume usage bytes come from live kubelet stats and aren't reproducible
			// across runs, so this is matched as a regex rather than exact text.
			args:            []string{"pod/sts-with-nodeport-0", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-nodeport.pod.regex",
		}.assert(t, nodeNameModifier, opts...)
		cmdTest{
			args:            []string{"pdb/sts-with-nodeport", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-nodeport.pdb.regex",
		}.assert(t, nodeNameModifier, opts...)
		cmdTest{
			args:            []string{"sts/sts-with-nodeport", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-with-nodeport.sts.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pdb-empty-selector-conflict", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pdb-conflict"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/pdb-empty-selector-conflict.yaml", ns)
		waitForInNamespace(t, "sts/pdb-conflict-test", "jsonpath={.status.readyReplicas}=1", ns)
		// Kubernetes' disruption controller picks one of the two overlapping PDBs arbitrarily
		// and leaves the other's currentHealthy permanently at 0 -- observedGeneration is not
		// enough to prove the controller has converged, since it only tracks spec generation:
		// a PDB can briefly report observedGeneration=1 with expectedPods=0 (as if its selector
		// matched no pods) before a later resync corrects it to the real count. Confirmed by
		// concurrently creating this fixture across many namespaces and polling: expectedPods=0
		// shows up transiently under load and always self-heals within seconds, it's never a
		// stable end state -- so wait for expectedPods=1 (both PDBs' selectors match the single
		// Pod here once converged) rather than trusting observedGeneration alone.
		waitForInNamespace(t, "pdb/pdb-conflict-test", "jsonpath={.status.observedGeneration}=1", ns)
		waitForInNamespace(t, "pdb/pdb-conflict-test", "jsonpath={.status.expectedPods}=1", ns)
		waitForInNamespace(t, "pdb/pdb-conflict-test-catch-all", "jsonpath={.status.observedGeneration}=1", ns)
		waitForInNamespace(t, "pdb/pdb-conflict-test-catch-all", "jsonpath={.status.expectedPods}=1", ns)
		cmdTest{
			args:            []string{"pod/pdb-conflict-test-0", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pdb-empty-selector-conflict.pod.regex",
		}.assert(t, nodeNameModifier, opts...)
	})
	t.Run("tcproute-with-gateway", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-tcproute"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/tcproute-with-gateway.yaml", ns)
		cmdTest{
			args:            []string{"tcproute/e2e-tcproute", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/tcproute-with-gateway.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"tcproute/e2e-tcproute", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/tcproute-with-gateway.deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("udproute-with-gateway", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-udproute"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/udproute-with-gateway.yaml", ns)
		cmdTest{
			args:            []string{"udproute/e2e-udproute", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/udproute-with-gateway.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"udproute/e2e-udproute", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/udproute-with-gateway.deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("listenerset-with-gateway", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-listenerset"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/listenerset-with-gateway.yaml", ns)
		cmdTest{
			args:            []string{"listenerset/e2e-listenerset", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/listenerset-with-gateway.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"listenerset/e2e-listenerset", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/listenerset-with-gateway.deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("backendtlspolicy-with-target", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-backendtlspolicy"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/backendtlspolicy-with-target.yaml", ns)
		cmdTest{
			args:            []string{"backendtlspolicy/e2e-backendtlspolicy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/backendtlspolicy-with-target.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"backendtlspolicy/e2e-backendtlspolicy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/backendtlspolicy-with-target.deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("vap-binding-resolves-policy", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		// The policy itself is cluster-scoped (ValidatingAdmissionPolicy/Binding aren't
		// namespaced), but its matchConstraints.namespaceSelector in vap-binding.yaml scopes
		// enforcement to this namespace specifically -- see the comment there for why.
		ns := "e2e-vap-binding"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifest(t, "e2e-artifacts/vap-binding.yaml")
		cmdTest{
			args:            []string{"validatingadmissionpolicybinding/e2e-require-team-label-binding", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/vap-binding.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("vapbinding referencing a missing policy is flagged not found", func(t *testing.T) {
		t.Parallel()
		// The binding is rendered with --local straight from a manifest rather than created on
		// the cluster, mirroring the orphan-owner-reference pattern above -- --local still
		// resolves the policyName against the real API server (only the binding object itself is
		// local), so the not-found check is exercised the same way.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		cmdTest{
			args:            []string{"-f", "../tests/e2e-artifacts/vapbinding-orphan-policy.yaml", "--local", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/vapbinding-orphan-policy.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("web-cert", func(t *testing.T) {
		t.Parallel()
		// A self-signed local CA issuing a leaf certificate, so the leaf's Secret shows
		// "issued by <CA>" rather than "Self-signed" -- the same cert-manager chain used for
		// the demo screenshot's Secret example, but exercised here as a regular e2e fixture.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-web-cert"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/web-cert.yaml", ns)
		waitForInNamespace(t, "certificate/web-ca", "condition=Ready", ns)
		waitForInNamespace(t, "certificate/web-tls", "condition=Ready", ns)
		cmdTest{
			args:            []string{"secret/web-tls", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/web-cert.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"secret/web-tls", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/web-cert.deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("web-policies", func(t *testing.T) {
		t.Parallel()
		// A PodDisruptionBudget and NetworkPolicy both selecting the same Deployment's Pods --
		// the same fixture used for the demo screenshot's matching-PDB/NetworkPolicy example.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-web-policies"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/web.yaml", ns)
		applyManifestInNamespace(t, "e2e-artifacts/web-policies.yaml", ns)
		waitForInNamespace(t, "deployment/web", "condition=Available", ns)
		waitForInNamespace(t, "pdb/web", "jsonpath={.status.observedGeneration}=1", ns)
		cmdTest{
			args:            []string{"deployment/web", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/web-policies.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("sts-without-service", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-sts-without-service"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/sts-without-service.yaml", ns)
		waitForInNamespace(t, "sts/sts-without-service", "jsonpath={.status.readyReplicas}=1", ns)
		cmdTest{
			args:            []string{"sts/sts-without-service", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-without-service.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("tls-validation", func(t *testing.T) {
		t.Parallel()
		// Builds a real cert-manager CA chain (self-signed root -> ca-type Issuer -> leaf
		// certificate) so the Ingress/Gateway/Secret TLS-consistency checks (self-signed,
		// hostname/SAN match, wrong type, missing keys) can be exercised against genuine
		// certificate content. --shallow (used by the offline golden-file tests) makes
		// KubeGetFirst a no-op, so this e2e suite is the only place in the whole test suite
		// that exercises the found-secret validation branches of Ingress.tmpl/Gateway.tmpl.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-tls-validation"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-ca.yaml", ns)
		waitForInNamespace(t, "certificate/e2e-tls-root-ca", "condition=Ready", ns)
		waitForInNamespace(t, "issuer/e2e-tls-ca-issuer", "condition=Ready", ns)
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-leaf.yaml", ns)
		waitForInNamespace(t, "certificate/e2e-tls-leaf", "condition=Ready", ns)

		t.Run("secret/leaf shows full non-self-signed certificate detail", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"secret/e2e-tls-leaf-tls", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
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
				args:            []string{"secret/e2e-tls-leaf-tls", "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-secret-leaf-deep.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("secret/root-ca is flagged self-signed", func(t *testing.T) {
			cmdTest{
				args:            []string{"secret/e2e-tls-root-ca-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-secret-root.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("ingress with matching hostname is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"ingress/e2e-tls-ingress-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
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
				args:            []string{"ingress/e2e-tls-ingress-mismatch", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-ingress-mismatch.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("ingress referencing the root CA secret flags self-signed", func(t *testing.T) {
			cmdTest{
				args:            []string{"ingress/e2e-tls-ingress-selfsigned", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-ingress-selfsigned.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("ingress with --deep inlines the full Secret detail", func(t *testing.T) {
			cmdTest{
				args:            []string{"ingress/e2e-tls-ingress-healthy", "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-ingress-deep.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("gateway with matching hostname is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"gateway/e2e-tls-gw-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
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
				args:            []string{"gateway/e2e-tls-gw-mismatch", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-gateway-mismatch.regex",
			}.assert(t, nil, opts...)
		})
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-grpcroute.yaml", ns)
		t.Run("grpcroute attached to healthy gateway listener shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"grpcroute/e2e-tls-grpcroute-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
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
				args:            []string{"grpcroute/e2e-tls-grpcroute-mismatch", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-grpcroute-mismatch.regex",
			}.assert(t, nil, opts...)
		})
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-tlsroute.yaml", ns)
		t.Run("tlsroute attached to Terminate listener with matching hostname is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"tlsroute/e2e-tlsroute-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
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
				args:            []string{"tlsroute/e2e-tlsroute-mismatch", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-tlsroute-mismatch.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("tlsroute attached to a Passthrough listener shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"tlsroute/e2e-tlsroute-passthrough", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-tlsroute-passthrough.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:", "doesn't exist"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-httproute.yaml", ns)
		t.Run("httproute attached to a healthy listener is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"httproute/e2e-tls-httproute-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
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
				args:            []string{"httproute/e2e-tls-httproute-mismatch", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-httproute-mismatch.regex",
			}.assert(t, nil, opts...)
		})
	})
	t.Run("pod-image-pull-secrets", func(t *testing.T) {
		t.Parallel()
		// --shallow (used by the offline golden-file tests) makes KubeGetFirst a no-op,
		// so this e2e suite is the only place that exercises the found-secret validation
		// branches of Pod.tmpl's imagePullSecrets check (Check A) and the "broken secrets"
		// correlation branch of the ImagePullBackOff hint (Check B).
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pod-image-pull-secrets"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/pod-image-pull-secrets.yaml", ns)

		// The kubelet keeps cycling a failing pull between ErrImagePull and ImagePullBackOff on
		// its retry loop; ImagePullBackOff is the longer-lived of the two (exponential backoff),
		// but under concurrent cluster load even that can elapse before a later sibling subtest's
		// render runs -- so re-confirm right before each assert instead of once up front for all
		// three pods.
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-missing-pull-secret", "main", "ImagePullBackOff", ns)
		t.Run("pod referencing a non-existent Secret flags it and correlates with the pull failure", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-missing-pull-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-missing.regex",
			}.assert(t, nil, opts...)
		})
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-wrong-type-pull-secret", "main", "ImagePullBackOff", ns)
		t.Run("pod referencing a wrong-type Secret flags it and correlates with the pull failure", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-wrong-type-pull-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-wrong-type.regex",
			}.assert(t, nil, opts...)
		})
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-healthy-pull-secret", "main", "ImagePullBackOff", ns)
		t.Run("pod referencing a healthy Secret shows no warnings", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-healthy-pull-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-healthy.regex",
			}.assert(t, nil, opts...)
		})
	})
	t.Run("pod-volume-configmap-secret", func(t *testing.T) {
		t.Parallel()
		// --shallow (used by the offline golden-file tests) makes KubeGetFirst a no-op, so
		// this e2e suite is the only place that exercises the configMap/secret volume
		// existence and key-presence checks in Pod.tmpl's pod_volumes/pod_volume_line.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pod-volume-configmap-secret"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/pod-volume-configmap-secret.yaml", ns)
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-volume-missing-configmap", "main", "ContainerCreating", ns)
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-volume-missing-secret", "main", "ContainerCreating", ns)
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-volume-missing-key", "main", "ContainerCreating", ns)
		waitForInNamespace(t, "pod/e2e-pod-volume-optional-missing", "condition=Ready", ns)
		waitForInNamespace(t, "pod/e2e-pod-volume-optional-missing-key", "condition=Ready", ns)
		waitForInNamespace(t, "pod/e2e-pod-volume-healthy", "condition=Ready", ns)

		t.Run("pod referencing a non-existent ConfigMap volume flags it without --include-all-volumes", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-missing-configmap", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-missing-configmap.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("pod referencing a non-existent Secret volume flags it without --include-all-volumes", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-missing-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-missing-secret.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("pod referencing an existing ConfigMap but a missing key flags it", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-missing-key", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-missing-key.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("optional configMap volume referencing a non-existent ConfigMap shows no warning", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-optional-missing", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--include-all-volumes", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-optional-missing.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("optional configMap volume with items referencing a missing key shows no warning", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-optional-missing-key", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--include-all-volumes", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-optional-missing-key.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("healthy configMap and secret volumes show no warnings", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--include-all-volumes", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-healthy.regex",
			}.assert(t, nil, opts...)
		})
	})
	t.Run("pod-container-logs", func(t *testing.T) {
		t.Parallel()
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
		opts := combineOpts(hackOpts, viperTestHackOpts(), []func(*plugin.RenderConfig){
			func(cfg *plugin.RenderConfig) { cfg.Now = time.Now },
		})
		ns := "e2e-pod-container-logs"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/pod-container-logs.yaml", ns)
		// The fixture pins a usage line for both healthy containers -- wait for metrics-server to
		// have scraped each of them specifically, not just the Pod overall: a container that
		// started slightly later than its siblings can still be missing from PodMetrics even once
		// the pod-level object exists, which otherwise renders that container's usage line blank.
		// Done before the CrashLoopBackOff wait below (not after): metrics-server's scrape
		// interval can take tens of seconds, and the crasher container keeps cycling
		// Waiting/Terminated the whole time it waits, so checking CrashLoopBackOff first and
		// then waiting on metrics just reintroduces the same flip the CrashLoopBackOff wait was
		// meant to avoid, by leaving a wide gap between the check and the actual assertion.
		waitForContainerMetrics(t, ns, "e2e-pod-container-logs", "healthy", "sidecar")
		// Wait for a stable Waiting(CrashLoopBackOff) state rather than just restartCount > 0:
		// the container's current state otherwise flips between Waiting and Terminated(Error)
		// as the kubelet retries, which would make the golden regex flaky. This has to be the
		// last wait before the assertion below -- see the comment above.
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-container-logs", "crasher", "CrashLoopBackOff", ns)

		cmdTest{
			args:            []string{"pod/e2e-pod-container-logs", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-container-logs.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("node correctly resolves pod metrics for pods in multiple namespaces via the batched PodMetrics lookup", func(t *testing.T) {
		t.Parallel()
		// Node.tmpl loops over every pod on the node (KubeGetNonTerminatedPodsOnNode) and looks
		// up each one's PodMetrics via KubeGetPodMetrics, which fetches metrics.k8s.io once for
		// the whole render (cluster-wide, or per-namespace as a fallback) instead of once per
		// pod. Pods in two distinct namespaces exercise the namespace-aware lookup within that
		// shared result: only --shallow-free live runs touch this path at all (see
		// TestAllArtifactsLocal), so this is the only place it's covered.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		var nodeName string
		for _, n := range nodes.Items {
			if n.Spec.Unschedulable {
				continue
			}
			for _, cond := range n.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					nodeName = n.Name
				}
			}
			if nodeName != "" {
				break
			}
		}
		require.NotEmpty(t, nodeName, "expected at least one schedulable, Ready node")

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
				"pod/e2e-metrics-pod", "-n", ns, "--timeout=4m").Run())
			waitForPodMetrics(t, ns, "e2e-metrics-pod")
		}

		cmdTest{
			args:            []string{"node/" + nodeName, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/node-metrics-multi-namespace.regex",
		}.assert(t, nil, opts...)
	})
}

func TestE2EDynamicManifests(t *testing.T) {
	e2eMinikubeTest(t)
	hackOpts, clientset, dynamicClient := e2eClients(t)
	t.Run("pod containers section warns when metrics-server's APIService is missing", func(t *testing.T) {
		// Issue #165 case 1: metrics-server was never installed. We simulate that by removing
		// just the APIService object that fronts it (not the Deployment/Service), which is
		// exactly what KubeMetricsUnavailableReason checks -- so the round trip is near-instant
		// and doesn't disturb metrics-server's actual health for other subtests.
		opts := combineOpts(hackOpts, viperTestHackOpts())
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
			args:            []string{"pod/e2e-pod-metrics-server-missing", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-metrics-server-missing.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("VerticalPodAutoscaler reverse-matches its target workload and shows an applied recommendation", func(t *testing.T) {
		// Deliberately kept out of TestE2EParallel's pool: the burner container below
		// intentionally pegs a full CPU to give the VPA recommender a reason to act, and on a
		// single-node cluster that starves metrics-server's own readiness probe when it runs
		// alongside the other concurrent subtests -- causing unrelated renders elsewhere to
		// intermittently report "metrics-server is not available". Running it serially, alongside
		// the other genuinely cluster-wide-affecting subtest above, avoids that.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-vpa"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		name := "vpa-burner"
		one := int32(1)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:    "burner",
						Image:   "busybox",
						Command: []string{"sh", "-c", "yes > /dev/null"},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("16Mi"),
							},
						},
					}}},
				},
			},
		}
		_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
		require.NoError(t, err)
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Available",
			"deployment/"+name, "-n", ns, "--timeout=4m").Run())
		originalPod := waitForPodByLabel(t, ns, "app="+name)

		vpaGVR := schema.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}
		vpa := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "autoscaling.k8s.io/v1",
			"kind":       "VerticalPodAutoscaler",
			"metadata":   map[string]interface{}{"name": name, "namespace": ns},
			"spec": map[string]interface{}{
				"targetRef": map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment", "name": name},
				"updatePolicy": map[string]interface{}{
					"updateMode":  "Recreate",
					"minReplicas": int64(1),
				},
				"resourcePolicy": map[string]interface{}{
					"containerPolicies": []interface{}{
						map[string]interface{}{
							"containerName": "burner",
							"minAllowed":    map[string]interface{}{"cpu": "10m", "memory": "16Mi"},
							"maxAllowed":    map[string]interface{}{"cpu": "500m", "memory": "128Mi"},
						},
					},
				},
			},
		}}
		_, err = dynamicClient.Resource(vpaGVR).Namespace(ns).Create(context.TODO(), vpa, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(vpaGVR).Namespace(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})

		waitForVPARecommendation(t, ns, name)
		waitForPodRecreated(t, ns, "app="+name, originalPod)
		// The evicted Pod can briefly still be listed (Terminating) alongside the replacement --
		// wait for exactly one to remain so the fixture below can pin a single Pod line.
		waitForSinglePod(t, ns, "app="+name)
		// waitForPodRecreated/waitForSinglePod only check the replacement Pod's name/count, not
		// its readiness -- under concurrent cluster load its Running/Ready transition can lag
		// well behind that, and the fixture below pins the Deployment as fully Available, so wait
		// for that explicitly rather than racing the kubelet.
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Available",
			"deployment/"+name, "-n", ns, "--timeout=5m").Run())
		waitForVPAPodsMatched(t, ns, name)

		cmdTest{
			args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/vpa-workload-reverse-match.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"vpa/" + name, "-n", ns, "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/vpa-standalone.regex",
		}.assert(t, nil, opts...)
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
