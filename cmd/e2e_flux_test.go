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

// runFluxSubtests is one of TestE2EParallel's topical groups (cmd/main_test.go). It qualifies on
// both of that function's criteria: the scenario below owns a namespace no other subtest touches
// (e2e-flux-kustomization), and every object it asserts on is namespaced -- the only cluster-scoped
// thing in play is the Flux install itself, which is a shared onceInstaller like the CRD bundles
// the other groups pull in, not a fixed object name this test could collide on.
//
// Installing a real controller doesn't by itself make a scenario serial, and neither does depending
// on metrics: the fixtures pin podinfo's HPA at its resting 2/2 against a 99%-of-request CPU target,
// which is a function of podinfo being idle rather than of what else is on the cluster, and the
// render's relative timestamps are frozen by ApplyTestHack's DurationRound rather than measured. The
// one subtest that genuinely can't run here (VPA, in TestE2EDynamicManifests) is excluded because it
// *pegs a CPU* and starves metrics-server for everyone else -- a cause, not a sensitivity. See #784:
// this scenario was a standalone top-level test, then briefly serial, before either was checked
// against the criteria above.
func runFluxSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	// This covers Kustomization.tmpl's live-query branches, which every offline artifact under
	// tests/artifacts/kustomization-* leaves untouched because --shallow/--local make KubeGetFirst
	// a no-op:
	//
	//   - default mode resolves each status.inventory entry into a per-kind health summary
	//     (managed_resource_line -> resource_health_summary),
	//   - --deep inlines each entry's full render instead,
	//   - an entry whose object is no longer in the cluster is flagged, and is *not* flagged under
	//     --shallow, where every lookup comes back empty for an unrelated reason.
	//
	// It also pins the one --deep behaviour that can't be reached offline: an inlined object naming
	// the Kustomization that owns it (via common.tmpl's flux_object_management) without recursing
	// back into it. The render engine has no cycle guard, so a deep_render_ref there would not
	// terminate.
	//
	// Flux is installed and left to reconcile a real source rather than having its status written by
	// the test. Every field asserted on here -- the inventory, lastAppliedRevision, the
	// kustomize.toolkit.fluxcd.io ownership labels, the applied policy annotations -- is one only
	// kustomize-controller writes, so a hand-patched status would only ever confirm our own reading
	// of the Flux API, never Flux's behaviour, and would keep passing after Flux changed it.
	//
	// What that costs: the inventory is whatever the pinned source actually contains, which is three
	// namespaced objects. Inventory entry *id shapes* -- a cluster-scoped entry's empty namespace
	// segment, an id that doesn't split into four parts -- are covered offline instead, by
	// tests/artifacts/kustomization-reconcile-pending, where a hand-written status is the point
	// rather than a substitute for one.
	t.Run("Flux Kustomization resolves its inventory entries, inlines them under --deep, and flags a missing one", func(t *testing.T) {
		t.Parallel()
		// ensureFlux sits inside the subtest rather than at the top of the group, which is where the
		// other topical groups put theirs: Flux is the suite's heaviest install (four controller
		// Deployments, each a cold image pull), and at group level it would go in on any
		// TestE2EParallel run whose -run pattern doesn't even reach this subtest. Safe to call from a
		// t.Parallel() subtest because installMu (cmd/e2e_helpers_test.go) serializes every install
		// against every other -- that mutex exists for exactly this call site.
		ensureFlux(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())

		ns := "e2e-flux-kustomization"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		applyManifestInNamespace(t, "e2e-artifacts/flux-kustomization-inventory.yaml", ns)
		// Ready on the Kustomization means the apply succeeded and the inventory has been written --
		// with spec.wait false it says nothing about the applied objects' health, hence the second
		// wait. The source clone and the podinfo image pull both happen inside this window.
		waitForInNamespace(t, "gitrepository/podinfo", "condition=Ready", ns)
		waitForInNamespace(t, "kustomization/podinfo", "condition=Ready", ns)
		waitForInNamespace(t, "deployment/podinfo", "condition=Available", ns)
		// The HPA is in the inventory too, and its render includes the scale target's current replica
		// count -- which only settles once the HPA has computed a recommendation and stopped moving.
		waitForFluxHPASettled(t, ns, "podinfo")

		// Delete one applied object out-of-band. This is the state the "missing" marker reports: the
		// inventory still records an apply that succeeded, the object is gone, and the Kustomization
		// goes on reporting Ready because nothing re-checks it until spec.interval (60m) elapses.
		// kubectl, not the clientset, so the deletion is ordered against the waits above the same way
		// every other manifest change in this suite is.
		out, err := exec.Command("kubectl", "delete", "service", "podinfo", "-n", ns, "--wait").CombinedOutput()
		require.NoErrorf(t, err, "failed to delete the managed Service out-of-band: %s", out)
		t.Logf("deleted managed Service podinfo in %s out-of-band: %s", ns, out)

		cmdTest{
			args:            []string{"kustomization/podinfo", "-n", ns, "--include-events=false", "--include-managed-fields=false"},
			stdoutRegexPath: "e2e-artifacts/flux-kustomization-inventory.regex",
		}.assert(t, nil, opts...)
		// The node-usage/kubelet-summary/lease sections are switched off because --deep reaches them
		// through the inlined Deployment's Pods, and they render host-specific numbers that would pin
		// this fixture to one machine. What's left of that subtree is matched tolerantly by the
		// fixture: Pod, Node and Service rendering belong to other subtests, and this one is about the
		// inventory entries being inlined at all.
		cmdTest{
			args: []string{"kustomization/podinfo", "-n", ns, "--deep",
				"--include-events=false", "--include-managed-fields=false",
				"--include-node-detailed-usage=false", "--include-node-kubelet-api-summary=false",
				"--include-node-lease=false", "--include-owners=false"},
			stdoutRegexPath: "e2e-artifacts/flux-kustomization-inventory-deep.regex",
		}.assert(t, nil, opts...)
		// --shallow must list the same entries as bare refs and call none of them missing: there every
		// lookup returns nothing because live queries are off, not because the object is gone.
		cmdTest{
			args:            []string{"kustomization/podinfo", "-n", ns, "--shallow"},
			stdoutRegexPath: "e2e-artifacts/flux-kustomization-inventory-shallow.regex",
		}.assert(t, nil, opts...)
	})
}

// waitForFluxHPASettled waits until the HorizontalPodAutoscaler that podinfo's kustomize/ ships has
// reached its resting state, so the inlined HPA render isn't racing the autoscaler: before the first
// metrics scrape currentReplicas reads 0 and the conditions say ScalingActive:False, and the
// replica count then moves from the Deployment's implicit 1 to the HPA's minReplicas of 2.
//
// ScalingLimited is waited on rather than tolerated in the fixture because its resting value here is
// not the intuitive one: podinfo sits near-idle against a 99%-of-request CPU target, so the HPA
// permanently *wants* fewer replicas than minReplicas allows and settles on
// ScalingLimited:True/TooFewReplicas. Reaching that is what tells us the autoscaler has scraped,
// scaled, and recomputed -- the same three steps the rest of the assertions depend on.
//
// AbleToScale is the one condition the fixture does tolerate two values for rather than waiting out:
// it reads SucceededRescale for the cycle after the scale-up and ReadyForNewScale from then on, and
// which of those a render catches depends on how loaded the cluster is, not on anything under test.
func waitForFluxHPASettled(t *testing.T, namespace, name string) {
	t.Helper()
	waitForInNamespace(t, "hpa/"+name, "condition=ScalingActive", namespace)
	waitForInNamespace(t, "hpa/"+name, "condition=ScalingLimited", namespace)
	require.Eventuallyf(t, func() bool {
		out, err := exec.Command("kubectl", "get", "hpa", name, "-n", namespace,
			"-o", "jsonpath={.status.desiredReplicas}/{.status.currentReplicas}").Output()
		if err != nil {
			return false
		}
		t.Logf("hpa/%s desired/current replicas: %s", name, out)
		return string(out) == "2/2"
	}, 3*time.Minute, 5*time.Second, "hpa/%s never settled at 2 replicas", name)
}
