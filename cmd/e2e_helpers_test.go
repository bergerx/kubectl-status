package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

// neverSchedulingGate is a spec.schedulingGates entry nothing ever removes, which parks a Pod in
// Pending indefinitely: PodScheduled:False SchedulingGated, no container statuses, no node. A
// subtest that only cares about a spec-driven section (the ServiceAccount/PriorityClass/
// RuntimeClass lookups) gates its Pod so the whole-output fixture isn't racing the scheduler and
// kubelet through Scheduled -> ContainerCreating -> Running -> the exit/restart cycle of whatever
// image it used. Preferred over pointing spec.nodeName at a Node that doesn't exist, which parks
// the Pod just as well but leaves it for the pod-GC controller to reap on its own ~40s timer.
const neverSchedulingGate = "kubectl-status.example.com/never-schedule"

// gatedPodSpecWith returns a PodSpec for a Pod that stays Pending forever (see
// neverSchedulingGate), with the field the caller's subtest is actually about set on it.
func gatedPodSpecWith(set func(*corev1.PodSpec)) corev1.PodSpec {
	spec := corev1.PodSpec{
		SchedulingGates: []corev1.PodSchedulingGate{{Name: neverSchedulingGate}},
		Containers:      []corev1.Container{{Name: "app", Image: "busybox"}},
	}
	set(&spec)
	return spec
}

type cmdTest struct {
	name            string
	args            []string
	stdoutRegexPath string // Regex match against file contents under test folder
	stdoutEqualPath string // Exact match with file contents under test folder
	stderrRegex     string // Regex
	stderrEqual     string // Exact
	wantErr         string // Contains
	// retryStdoutRegexFor re-runs the command until its stdout matches stdoutRegexPath, for
	// state a `kubectl get` poll can confirm is present but that won't hold still for the length
	// of one render (see the CrashLoopBackOff note on cmdTest.execute). Zero -- the default, and
	// what every other subtest wants -- renders exactly once.
	retryStdoutRegexFor time.Duration
}

// createBadNode creates a synthetic Node (no real kubelet backs it) that's cordoned, tainted,
// and reporting NotReady/MemoryPressure -- everything pod_node_problems/pod_node_problem_flags
// are meant to surface. It also carries the content-free placeholder condition real clusters are
// seen leaving behind (an empty type with nothing but a status and heartbeats, see #768), which
// must stay invisible in every render; that rides along here rather than on the cluster's own
// Node, which no test ever mutates. It registers cleanup and returns the Node's name.
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
			{
				Type:               "",
				Status:             corev1.ConditionFalse,
				LastHeartbeatTime:  metav1.Now(),
				LastTransitionTime: metav1.Now(),
			},
		}
		_, err = clientset.CoreV1().Nodes().UpdateStatus(context.TODO(), latest, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err)
	// The node-lifecycle-controller mirrors the conditions set above into taints, and callers pin
	// the full rendered taint list -- so wait for it to have caught up rather than racing it.
	// node.kubernetes.io/unschedulable comes from Spec.Unschedulable and is already there.
	wantTaints := []string{
		"dedicated",
		corev1.TaintNodeUnschedulable,
		corev1.TaintNodeNotReady,
		corev1.TaintNodeMemoryPressure,
	}
	require.Eventuallyf(t, func() bool {
		latest, err := clientset.CoreV1().Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		have := map[string]bool{}
		for _, taint := range latest.Spec.Taints {
			have[taint.Key] = true
		}
		for _, key := range wantTaints {
			if !have[key] {
				return false
			}
		}
		return true
	}, time.Minute, time.Second, "Node/%s never got all of the expected taints %v", node.Name, wantTaints)
	return node.Name
}

func nodeNameModifier(stdout string) string {
	return string(regexp.MustCompile(`Node/[a-z0-9-]+`).ReplaceAll([]byte(stdout), []byte(`Node/minikube`)))
}

// assertStdoutMatchesRegexFixture matches stdout against the regex in tests/<fixture> (a path
// relative to tests/, e.g. "e2e-artifacts/node-query.regex"). Fixtures are whole-output matches
// anchored with \A...\z (see CONTRIBUTING.md), and are matched in (?ms) mode so `.` spans the
// newlines between output lines. Subtests that assert on stdout beyond the fixture match (an
// extra NotContains sweep, say) call this directly; the rest go through cmdTest.stdoutRegexPath.
func assertStdoutMatchesRegexFixture(t *testing.T, stdout, fixture string) {
	t.Helper()
	outFile := path.Join("..", "tests", fixture)
	regexBytes, err := os.ReadFile(outFile)
	assert.NoErrorf(t, err, "failed to read test artifact file: %s", outFile)
	assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
}

// execute runs the command once, or -- when retryStdoutRegexFor is set -- until its stdout matches
// the stdoutRegexPath fixture, returning the last attempt's output either way so the assertions
// below still report a real render rather than a bare timeout. Only state the kubelet reports for a
// moment at a time needs this. A crashlooping container is the case in hand: for all but the last
// seconds of each restart backoff window its status shows the exited instance as
// Terminated(Error), and the kubelet swaps that for Waiting(CrashLoopBackOff) only just before it
// starts the container again. So however recently a `kubectl get` poll confirmed CrashLoopBackOff,
// the container can be Running by the time the render's own Pod GET lands -- and since the windows
// double (10s, 20s, 40s, ... capped at 5m), waiting for a later one buys nothing but a longer wait
// for the same few seconds. Re-rendering is what closes the gap: CrashLoopBackOff comes back at the
// end of the next window, and one of the renders lands while it's there.
func (c cmdTest) execute(t *testing.T, stdoutModifier func(string) string, opts ...func(*plugin.RenderConfig)) (string, string, error) {
	t.Helper()
	render := func() (string, string, error) {
		stdout, stderr, err := executeCMD(t, c.args, opts...)
		if stdoutModifier != nil {
			stdout = nodeNameModifier(stdout)
		}
		return stdout, stderr, err
	}
	stdout, stderr, err := render()
	if c.retryStdoutRegexFor == 0 || c.stdoutRegexPath == "" {
		return stdout, stderr, err
	}
	outFile := path.Join("..", "tests", c.stdoutRegexPath)
	regexBytes, readErr := os.ReadFile(outFile)
	require.NoErrorf(t, readErr, "failed to read test artifact file: %s", outFile)
	fixture, compileErr := regexp.Compile(`(?ms)` + string(regexBytes))
	require.NoErrorf(t, compileErr, "failed to compile test artifact regex: %s", outFile)
	deadline := time.Now().Add(c.retryStdoutRegexFor)
	for attempt := 2; !fixture.MatchString(stdout) && time.Now().Before(deadline); attempt++ {
		time.Sleep(2 * time.Second)
		t.Logf("stdout didn't match %s, re-rendering (attempt %d)", c.stdoutRegexPath, attempt)
		stdout, stderr, err = render()
	}
	return stdout, stderr, err
}

func (c cmdTest) assert(t *testing.T, stdoutModifier func(string) string, opts ...func(*plugin.RenderConfig)) {
	t.Helper()
	t.Logf("running cmdTest assert: %+v", c)
	stdout, stderr, err := c.execute(t, stdoutModifier, opts...)
	switch {
	case c.stdoutRegexPath == "" && c.stdoutEqualPath == "":
		assert.Empty(t, stdout)
	case c.stdoutEqualPath != "":
		outFile := path.Join("..", "tests", c.stdoutEqualPath)
		out, err := os.ReadFile(outFile)
		assert.NoErrorf(t, err, "failed to read test artifact file: %s", outFile)
		assert.Equal(t, string(out), stdout)
	case c.stdoutRegexPath != "":
		assertStdoutMatchesRegexFixture(t, stdout, c.stdoutRegexPath)
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
	// `make test-e2e`/`test-e2e-quick` use one fixed shared profile name (see Makefile)
	// and pass ASSUME_MINIKUBE_IS_CONFIGURED=true, so they never reach this function.
	// This fallback only matters for ad hoc `go test -run TestE2E...` invocations that
	// bypass the Makefile entirely: set E2E_PROFILE yourself (`make print-e2e-profile`
	// prints the same shared name the Makefile would use) to land on that same cluster
	// instead of starting (and leaking) a one-off one named after t.Name().
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

// deleteNamespaceAndWait deletes a namespace and waits for it to be fully gone, instead of firing
// the delete and moving on -- a fixed namespace name reused on the next run would otherwise race a
// namespace this run left Terminating. Runs from t.Cleanup, so a slow or failed teardown only logs
// a warning: it must not fail the test whose cleanup is calling it, and it must not stop other
// subtests' cleanup from running.
func deleteNamespaceAndWait(t *testing.T, clientset *kubernetes.Clientset, namespace string) {
	t.Helper()
	err := clientset.CoreV1().Namespaces().Delete(context.TODO(), namespace, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Logf("warning: failed to delete namespace %s: %v", namespace, err)
		return
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		_, err := clientset.CoreV1().Namespaces().Get(context.TODO(), namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			t.Logf("namespace %s fully deleted", namespace)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("warning: namespace %s still terminating after 2m, leaving it be", namespace)
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
//
// The timeout has margin above what any single wait needs on an idle cluster: TestE2EParallel's
// pool shares one minikube VM across -parallel subtests, and runFluxSubtests' ensureFlux install
// is the heaviest of them, so a controller (e.g. the PDB/disruption controller another subtest is
// waiting on) can legitimately take longer to reconcile while it's running. A 4m budget measured
// this timing out at ~248s on a loaded CI runner; 8m keeps a real hang catchable well inside the
// job's -timeout=25m without the wait racing pool contention it doesn't control.
func waitForInNamespace(t *testing.T, resource, forParam, namespace string) {
	t.Helper()
	cmd := exec.Command("kubectl", "wait", "-n", namespace, "--for", forParam, resource, "--timeout=8m")
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

// waitForStatefulSetUpdateRevisionChange polls until the StatefulSet's status.updateRevision no
// longer equals staleRevision -- used after reverting spec.template, so the assertion that
// follows reads the controller's reconciled state rather than racing a stale one.
func waitForStatefulSetUpdateRevisionChange(t *testing.T, namespace, name, staleRevision string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", "get", "statefulset", name, "-n", namespace,
			"-o", "jsonpath={.status.updateRevision}")
		output, err := cmd.CombinedOutput()
		if err == nil {
			rev := strings.TrimSpace(string(output))
			if rev != "" && rev != staleRevision {
				t.Logf("statefulset %s/%s updateRevision changed from %s to %s", namespace, name, staleRevision, rev)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for statefulset %s/%s updateRevision to change from %s", namespace, name, staleRevision)
}

// waitForPodReadyInNamespace polls a Pod's Ready condition instead of using `kubectl wait`, which
// errors immediately if the Pod doesn't exist yet -- needed right after deleting a Pod, before the
// owning controller has recreated it.
func waitForPodReadyInNamespace(t *testing.T, namespace, podName string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		cmd := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
			"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		output, err := cmd.CombinedOutput()
		if err == nil && strings.TrimSpace(string(output)) == "True" {
			t.Logf("pod %s/%s is Ready", namespace, podName)
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for pod %s/%s to become Ready", namespace, podName)
}

// waitForContainerWaitingReasonInNamespace polls until the named container in the resource reports
// the given waiting-state reason, so the render that follows has a specific state to assert on
// rather than whatever a plain restart-count check happened to catch. Targets a namespace
// explicitly via `kubectl -n` instead of the kubeconfig's default; pass "" to use the kubeconfig's
// default namespace. Reasons the kubelet drops again on its own schedule (CrashLoopBackOff) need
// cmdTest.retryStdoutRegexFor on top of this -- see the note on cmdTest.execute.
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
// API CRDs, Cilium/Calico CRDs, VPA, Crossplane, Flux) instead of everything being installed
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

// installMu serializes every install against every other one, across all onceInstallers. The
// sync.Once above only keeps a single dependency from being installed twice; it says nothing about
// two *different* ones overlapping. Most call sites can't overlap on their own: a group-level
// ensureX in TestE2EParallel runs in that function's own goroutine, which a parallel subtest can't
// resume ahead of (t.Run parks such a subtest and only releases it once the parent function
// returns), and TestE2EDynamicManifests has no parallel subtests at all. But that's a property of
// where those call sites happen to sit, not something the type enforces, and ensureFlux is already
// the exception: it's called from inside a t.Parallel() subtest (runFluxSubtests,
// cmd/e2e_flux_test.go), where without this mutex it would race installs of unrelated dependencies
// running concurrently in sibling subtests. Serializing here costs nothing to be sure of -- a warm
// re-run of every installer totals ~15s, and a cold one is exactly the case you want taking the
// cluster one dependency at a time rather than several controller rollouts at once.
//
// Held only around the install itself, never around require.NoError: install closures are t-free by
// contract (see above), but keeping the assertion outside means even a stray require inside one
// unwinds via runtime.Goexit without stranding the lock.
var installMu sync.Mutex

func (o *onceInstaller) ensure(t *testing.T, install func() error) {
	t.Helper()
	func() {
		installMu.Lock()
		defer installMu.Unlock()
		o.once.Do(func() {
			o.err = install()
		})
	}()
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

var volumeSnapshotCRDsInstaller onceInstaller

// ensureVolumeSnapshotCRDs installs the VolumeSnapshot/VolumeSnapshotContent CRDs (snapshot.
// storage.k8s.io), needed by TestE2EDynamicManifests' VolumeSnapshot(Content) subtests. Same
// "CRDs only" reasoning as Gateway API/Cilium/Calico above: minikube's hostpath
// storage-provisioner has no CSI snapshot support, and getting a real snapshot to reach
// ReadyToUse deterministically would need a real CSI driver + external-snapshotter controller
// running, which isn't available here -- so the subtests create VolumeSnapshot/
// VolumeSnapshotContent objects (and their status) directly against the API instead (same trick
// as the VolumeAttachment subtest uses), and only need the CRDs' shape validated by the
// apiserver, not a controller actually reconciling them. VolumeSnapshotClass isn't installed:
// nothing here creates one, spec.volumeSnapshotClassName is just a free-form string reference.
func ensureVolumeSnapshotCRDs(t *testing.T) {
	t.Helper()
	volumeSnapshotCRDsInstaller.ensure(t, func() error {
		version, err := versionsEnvValue("EXTERNAL_SNAPSHOTTER_VERSION")
		if err != nil {
			return err
		}
		urls := []string{
			fmt.Sprintf("https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/%s/client/config/crd/snapshot.storage.k8s.io_volumesnapshots.yaml", version),
			fmt.Sprintf("https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/%s/client/config/crd/snapshot.storage.k8s.io_volumesnapshotcontents.yaml", version),
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

var karpenterCRDsInstaller onceInstaller

// ensureKarpenterCRDs installs the NodePool/NodeClaim CRDs (karpenter.sh/v1), needed by
// runKarpenterSubtests. kubectl-status only reads/renders these objects client-side and never
// relies on the Karpenter controller actually provisioning nodes, so the CRDs alone (no Karpenter
// controller installed) are enough -- same "CRDs only" reasoning as ensureCiliumCalicoCRDs/
// ensureVolumeSnapshotCRDs above.
func ensureKarpenterCRDs(t *testing.T) {
	t.Helper()
	karpenterCRDsInstaller.ensure(t, func() error {
		version, err := versionsEnvValue("KARPENTER_VERSION")
		if err != nil {
			return err
		}
		urls := []string{
			fmt.Sprintf("https://raw.githubusercontent.com/kubernetes-sigs/karpenter/%s/pkg/apis/crds/karpenter.sh_nodepools.yaml", version),
			fmt.Sprintf("https://raw.githubusercontent.com/kubernetes-sigs/karpenter/%s/pkg/apis/crds/karpenter.sh_nodeclaims.yaml", version),
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

// helmRepoAddUpdate runs `helm repo add`+`helm repo update` for name/url, retrying both on
// failure. Chart repos here (charts.crossplane.io, cowboysysop.github.io, kyverno.github.io) are
// static-site CDNs that occasionally 403 GitHub Actions' shared runner IPs (seen live on
// charts.crossplane.io, which was reachable moments later from an unrelated network) -- a transient
// block, not a broken repo, so a short backoff is enough to get past it without waiting for the
// whole suite's own outer retry (nick-fields/retry, ~15 minutes per attempt) to cycle around.
func helmRepoAddUpdate(name, url string) error {
	backoff := wait.Backoff{Duration: 5 * time.Second, Factor: 2, Steps: 4}
	return retry.OnError(backoff, func(error) bool { return true }, func() error {
		if output, err := exec.Command("helm", "repo", "add", name, url).CombinedOutput(); err != nil {
			return fmt.Errorf("helm repo add %s: %w: %s", name, err, output)
		}
		if output, err := exec.Command("helm", "repo", "update", name).CombinedOutput(); err != nil {
			return fmt.Errorf("helm repo update %s: %w: %s", name, err, output)
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
		if err := helmRepoAddUpdate("cowboysysop", "https://cowboysysop.github.io/charts/"); err != nil {
			return err
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
		if err := helmRepoAddUpdate("crossplane-stable", "https://charts.crossplane.io/stable"); err != nil {
			return err
		}
		if output, err := exec.Command("helm", "upgrade", "--install", "crossplane", "crossplane-stable/crossplane",
			"--version", version, "-n", "crossplane-system", "--create-namespace", "--wait", "--timeout", "5m").CombinedOutput(); err != nil {
			return fmt.Errorf("helm upgrade crossplane: %w: %s", err, output)
		}
		functionsManifest := path.Join("..", "tests", "e2e-artifacts", "crossplane-functions.yaml")
		if output, err := exec.Command("kubectl", "apply", "-f", functionsManifest).CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl apply %s: %w: %s", functionsManifest, err, output)
		}
		// 5m, matching the helm installs above rather than being the one short wait in the
		// function: both Functions are OCI packages pulled on demand, so this is a cold pull of
		// two images plus a Deployment rollout, racing whatever else the suite has running.
		output, err := exec.Command("kubectl", "wait", "--for=condition=Healthy", "--timeout=300s",
			"function.pkg.crossplane.io", "--all").CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl wait crossplane functions: %w: %s%s", err, output, crossplaneDiagnostics())
		}
		return nil
	})
}

var fluxInstaller onceInstaller

// ensureFlux installs Flux, needed by TestE2EParallel's Flux subtest. Unlike the CRDs-only
// installs above, this one has to actually reconcile: what that test asserts on is
// status.inventory, status.lastAppliedRevision and the kustomize.toolkit.fluxcd.io ownership
// labels -- all of them written by kustomize-controller as it applies a real source, none of them
// fields a user sets. Fabricating them would leave the test asserting against our own idea of what
// Flux writes rather than against Flux, which is the whole point of covering these branches live.
// Same "controller must actually run" reasoning as ensureCrossplane/ensureVPA.
//
// The release's install.yaml is applied directly rather than shelling out to `flux install`: it's
// the same manifest the CLI would render with default components, and it doesn't put the flux
// binary on the list of things a contributor (or the CI runner) needs installed. --server-side:
// the bundle's CRDs are large enough to trip the same client-side last-applied-configuration
// annotation limit as the Gateway API bundle above.
func ensureFlux(t *testing.T) {
	t.Helper()
	fluxInstaller.ensure(t, func() error {
		version, err := versionsEnvValue("FLUX_VERSION")
		if err != nil {
			return err
		}
		url := fmt.Sprintf("https://github.com/fluxcd/flux2/releases/download/%s/install.yaml", version)
		if output, err := exec.Command("kubectl", "apply", "--server-side", "-f", url).CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl apply flux install.yaml: %w: %s", err, output)
		}
		// 300s matches the cert-manager/Crossplane installs: four controller Deployments, each a
		// cold image pull, racing whatever else the suite has running on the shared cluster.
		output, err := exec.Command("kubectl", "wait", "--for=condition=Available", "--timeout=300s",
			"deployment", "--all", "-n", "flux-system").CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl wait flux deployments: %w: %s%s", err, output, fluxDiagnostics())
		}
		return nil
	})
}

// fluxDiagnostics dumps why the controllers never came up, for the same reason
// crossplaneDiagnostics exists: `kubectl wait`'s timeout message names no cause, and this install
// runs unattended on a cluster the rest of the suite is loading, so this is the only evidence
// anyone gets after the fact.
func fluxDiagnostics() string {
	var b strings.Builder
	for _, args := range [][]string{
		{"get", "pods", "-n", "flux-system", "-o", "wide"},
		{"get", "events", "-n", "flux-system", "--sort-by=.lastTimestamp"},
	} {
		out, err := exec.Command("kubectl", args...).CombinedOutput()
		fmt.Fprintf(&b, "\n--- kubectl %s ---\n%s", strings.Join(args, " "), out)
		if err != nil {
			fmt.Fprintf(&b, "(%v)\n", err)
		}
	}
	return b.String()
}

// crossplaneDiagnostics dumps the state that explains why the Functions never went Healthy. On
// timeout `kubectl wait` says only "timed out waiting for the condition on functions/...", which
// names no cause: a package still pulling, a pull that failed outright, and a Function whose pod
// never got scheduled are indistinguishable in CI logs after the fact. The install runs
// unattended on a cluster the rest of the suite is loading, so whatever is collected here is the
// only evidence anyone gets -- the failure is not reproducible by re-reading the log later.
func crossplaneDiagnostics() string {
	var b strings.Builder
	for _, args := range [][]string{
		{"get", "function.pkg.crossplane.io", "-o", "wide"},
		{"get", "functionrevision.pkg.crossplane.io", "-o", "wide"},
		{"get", "pods", "-n", "crossplane-system", "-o", "wide"},
		{"get", "events", "-n", "crossplane-system", "--sort-by=.lastTimestamp"},
	} {
		out, err := exec.Command("kubectl", args...).CombinedOutput()
		fmt.Fprintf(&b, "\n--- kubectl %s ---\n%s", strings.Join(args, " "), out)
		if err != nil {
			fmt.Fprintf(&b, "(%v)\n", err)
		}
	}
	return b.String()
}

var istioCRDsInstaller onceInstaller

// istioCRDsWanted are the CRDs ensureIstioCRDs installs out of Istio's crd-all.gen.yaml bundle.
//
// Deliberately just the two the templates render, not the whole bundle: it also carries
// gateways.networking.istio.io, and a cluster serving both that and the Gateway API's
// gateways.gateway.networking.k8s.io has two resources answering to Kind=Gateway. The RESTMapper
// behind KubeGet/KubeGetFirst resolves an unqualified kind across all groups and silently picks
// one (#789). The Gateway API's wins as things stand, so the existing Gateway subtests would
// keep passing -- but that tie-break is a property of discovery ordering rather than anything
// this repo sets, and a shared cluster every other group's fixtures run against is the last
// place to let a silent resolution decide whether they pass. Istio Gateway support has to settle
// the collision before it can land; until then this suite doesn't put the CRD on the cluster.
var istioCRDsWanted = []string{
	"virtualservices.networking.istio.io",
	"destinationrules.networking.istio.io",
}

// ensureIstioCRDs installs the VirtualService/DestinationRule CRDs, needed by runIstioSubtests.
// CRDs only: kubectl-status reads these objects and matches them against Services/Pods
// client-side, and istiod writes no status to them that the templates render, so no control
// plane needs to be running -- same reasoning as ensureGatewayAPICRDs/ensureCiliumCalicoCRDs.
// That also keeps `istioctl install` (and a full mesh's worth of pods) off a shared single-node
// cluster the rest of the suite is using.
//
// Istio publishes its CRDs only as one bundle, so the bundle is fetched and the two wanted
// documents are applied from stdin rather than the URL being handed to kubectl whole.
// --server-side: these CRDs' embedded schemas are large enough to trip client-side apply's
// last-applied-configuration annotation limit, as the Gateway API's do.
func ensureIstioCRDs(t *testing.T) {
	t.Helper()
	istioCRDsInstaller.ensure(t, func() error {
		version, err := versionsEnvValue("ISTIO_VERSION")
		if err != nil {
			return err
		}
		url := fmt.Sprintf("https://raw.githubusercontent.com/istio/istio/%s/manifests/charts/base/files/crd-all.gen.yaml", version)
		resp, err := http.Get(url)
		if err != nil {
			return fmt.Errorf("fetch istio CRD bundle: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("fetch istio CRD bundle: %s returned %s", url, resp.Status)
		}
		bundle, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read istio CRD bundle: %w", err)
		}
		var wanted []string
		for _, doc := range strings.Split(string(bundle), "\n---\n") {
			for _, name := range istioCRDsWanted {
				if strings.Contains(doc, "\n  name: "+name+"\n") {
					wanted = append(wanted, doc)
				}
			}
		}
		if len(wanted) != len(istioCRDsWanted) {
			return fmt.Errorf("istio CRD bundle %s: found %d of %v", url, len(wanted), istioCRDsWanted)
		}
		cmd := exec.Command("kubectl", "apply", "--server-side", "-f", "-")
		cmd.Stdin = strings.NewReader(strings.Join(wanted, "\n---\n"))
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl apply istio CRDs: %w: %s", err, output)
		}
		return nil
	})
}

var kyvernoInstaller onceInstaller

// ensureKyverno installs Kyverno, needed by runKyvernoSubtests. PolicyReport.tmpl/
// ClusterPolicyReport.tmpl render results/summary/scope that only Kyverno's own
// admission-controller and reports-controller write as they actually evaluate a real
// ClusterPolicy against a real resource -- fabricating that status would only confirm our own
// idea of the wgpolicyk8s.io shape, never Kyverno's actual behaviour, which is the whole point of
// covering this live rather than with hand-written fixtures. Same "controller must actually run"
// reasoning as ensureFlux/ensureCrossplane/ensureVPA.
//
// Chart version is pinned in hack/versions.env as KYVERNO_VERSION (the chart version, e.g.
// "3.8.2" -- Kyverno's Helm chart and app versions diverge, chart 3.8.2 ships app v1.18.2).
func ensureKyverno(t *testing.T) {
	t.Helper()
	kyvernoInstaller.ensure(t, func() error {
		version, err := versionsEnvValue("KYVERNO_VERSION")
		if err != nil {
			return err
		}
		if err := helmRepoAddUpdate("kyverno", "https://kyverno.github.io/kyverno/"); err != nil {
			return err
		}
		// features.backgroundScan.backgroundScanInterval: default 1h, far longer than any test
		// should wait. autoUpdateWebhooks (chart default) reconciles the ValidatingWebhookConfiguration
		// within seconds of a ClusterPolicy being created, so the admission path is the one
		// runKyvernoSubtests' Pod/Namespace creates normally take; this short interval is only the
		// fallback for whatever fraction of that reconcile window it races.
		//
		// cleanupController.enabled=false: that controller only serves CleanupPolicy/
		// ClusterCleanupPolicy, unrelated to PolicyReport generation, so it's pure unused
		// footprint here. The three *Controller.resources.requests.cpu overrides shrink the chart's
		// default 100m/controller: the shared cluster's node measured Allocatable cpu: 2 (not the
		// 4 `make e2e-minikube-up` requests -- the docker driver caps it to whatever the host
		// actually has), and every other onceInstaller's controllers plus kube-system already sit
		// at ~97% of that before Kyverno's own pods are counted, so the chart's defaults leave the
		// reports-controller Pending on "Insufficient cpu" (confirmed against this exact cluster).
		// 10m each is still generous for a controller that's idle between the handful of events
		// this one test namespace produces.
		if output, err := exec.Command("helm", "upgrade", "--install", "kyverno", "kyverno/kyverno",
			"--version", version, "-n", "kyverno", "--create-namespace", "--wait", "--timeout", "5m",
			"--set", "features.backgroundScan.backgroundScanInterval=30s",
			"--set", "cleanupController.enabled=false",
			"--set", "admissionController.resources.requests.cpu=10m",
			"--set", "backgroundController.resources.requests.cpu=10m",
			"--set", "reportsController.resources.requests.cpu=10m").CombinedOutput(); err != nil {
			return fmt.Errorf("helm upgrade kyverno: %w: %s%s", err, output, kyvernoDiagnostics())
		}
		// Belt-and-suspenders alongside helm --wait above, matching ensureVPA: confirms the
		// specific signal (Deployments Available) the rest of the suite depends on, with its own
		// diagnostics if it's ever the one that times out instead of helm's own wait.
		output, err := exec.Command("kubectl", "wait", "--for=condition=Available", "--timeout=300s",
			"deployment", "--all", "-n", "kyverno").CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl wait kyverno deployments: %w: %s%s", err, output, kyvernoDiagnostics())
		}
		return nil
	})
}

// kyvernoDiagnostics dumps why the controllers never came up, for the same reason
// fluxDiagnostics/crossplaneDiagnostics exist: a bare timeout names no cause, and this install
// runs unattended on a cluster the rest of the suite is loading.
func kyvernoDiagnostics() string {
	var b strings.Builder
	for _, args := range [][]string{
		{"get", "pods", "-n", "kyverno", "-o", "wide"},
		{"get", "events", "-n", "kyverno", "--sort-by=.lastTimestamp"},
	} {
		out, err := exec.Command("kubectl", args...).CombinedOutput()
		fmt.Fprintf(&b, "\n--- kubectl %s ---\n%s", strings.Join(args, " "), out)
		if err != nil {
			fmt.Fprintf(&b, "(%v)\n", err)
		}
	}
	return b.String()
}
