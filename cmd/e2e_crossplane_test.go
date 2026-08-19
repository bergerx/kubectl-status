package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func runCrossplaneSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	t.Run("Crossplane XR composes namespaced children and surfaces their health", func(t *testing.T) {
		t.Parallel()
		// Crossplane core plus the two Composition Functions it needs must actually reconcile
		// to produce the XR's composed children, same "controller must actually run" reasoning
		// as the Flux scenario (runFluxSubtests, cmd/e2e_flux_test.go): the installer is a
		// shared onceInstaller serialized by installMu, so ensureCrossplane is safe from inside
		// a t.Parallel() subtest.
		ensureCrossplane(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-crossplane-xr"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		applyManifest(t, "e2e-artifacts/crossplane-xstatusprobe.yaml")
		require.NoError(t, kubectlCmd(t, "wait", "--for=condition=Established",
			"xrd/xstatusprobes.tests.kubectl-status.io", "--timeout=60s").Run())
		applyManifestInNamespace(t, "e2e-artifacts/crossplane-xr.yaml", ns)
		waitForInNamespace(t, "xstatusprobe/probe-a", "condition=Synced", ns)
		// The Deployment child is deliberately unschedulable (a nodeSelector no node can match),
		// so the XR itself never reaches Ready -- wait on the field kubectl-status actually reads
		// instead of a condition that will never flip.
		waitForCrossplaneComposedRefs(t, ns, "probe-a", 2)
		// Synced/resourceRefs land as soon as the render step runs, but the XR's own Responsive
		// condition and the composed Deployment's Progressing/Available conditions populate
		// slightly later via separate reconciles -- wait for all of them so the fixtures below
		// pin a stable message instead of racing a transient "Replicas: 0/1" kstatus summary.
		waitForInNamespace(t, "xstatusprobe/probe-a", "condition=Responsive", ns)
		waitForInNamespace(t, "deployment/probe-a-blocked", "condition=Progressing", ns)
		require.NoError(t, kubectlCmd(t, "wait", "-n", ns,
			"--for=condition=PodScheduled=false", "pod", "-l", "app=probe-a-blocked", "--timeout=2m").Run())
		// kstatus (sigs.k8s.io/cli-utils/pkg/kstatus/status.ScheduleWindow) gives a Pod 15s from
		// its creationTimestamp before reporting Unschedulable as Failed rather than InProgress --
		// wait that out so the fixtures below pin the stable "Failed: Pod could not be scheduled"
		// message instead of racing the transient one.
		waitForPodScheduleWindow(t, ns, "app=probe-a-blocked")

		// Only the live-query-dependent branches belong here: default mode's KubeGetFirst lookup
		// (populating each composed child's compact health) and --deep's IncludeRenderableObject
		// inline. Shallow rendering and Composition.tmpl make no live queries at all -- both are
		// already covered by the offline artifacts (tests/artifacts/crossplane-*).
		cmdTest{
			args:            []string{"xstatusprobe/probe-a", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/crossplane-xr.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"xstatusprobe/probe-a", "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/crossplane-xr-deep.regex",
		}.assert(t, nil, opts...)
	})
}
