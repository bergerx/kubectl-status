package main

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func runPodLogsAndMetricsSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
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
