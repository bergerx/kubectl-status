package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// patchKustomizationStatus writes conditions and a status.inventory onto a Kustomization through
// the status subresource. Inventory entry ids are "<namespace>_<name>_<group>_<kind>" (core-group
// kinds leave the group segment empty, cluster-scoped ones the namespace segment), so they can only
// be built once the test knows its namespace -- which is why they aren't in the fixture yaml.
func patchKustomizationStatus(t *testing.T, namespace, name string, entryIDs []string) {
	t.Helper()
	entries := make([]map[string]string, 0, len(entryIDs))
	for _, id := range entryIDs {
		entries = append(entries, map[string]string{"id": id, "v": "v1"})
	}
	patch, err := json.Marshal(map[string]interface{}{
		"status": map[string]interface{}{
			"observedGeneration":  1,
			"lastAppliedRevision": "refs/heads/main@sha1:7f3c1a9e4b2d8c60f5a3e1b7d9c2f480a6e5b3d1",
			"conditions": []map[string]interface{}{{
				"type":               "Ready",
				"status":             "True",
				"reason":             "ReconciliationSucceeded",
				"message":            "Applied revision: refs/heads/main@sha1:7f3c1a9e",
				"lastTransitionTime": "2020-01-01T00:00:00Z",
				"observedGeneration": 1,
			}},
			"inventory": map[string]interface{}{"entries": entries},
		},
	})
	require.NoError(t, err)
	out, err := exec.Command("kubectl", "patch", "kustomization", name, "-n", namespace,
		"--subresource=status", "--type=merge", "-p", string(patch)).CombinedOutput()
	require.NoErrorf(t, err, "failed to patch Kustomization status: %s", string(out))
	t.Logf("patched Kustomization %s/%s status: %s", namespace, name, string(out))
}

// TestE2EFluxKustomizationInventory covers Kustomization.tmpl's live-query branches, which every
// offline artifact under tests/artifacts/kustomization-* leaves untouched because --shallow/--local
// make KubeGetFirst a no-op:
//
//   - default mode resolves each status.inventory entry into a per-kind health summary
//     (managed_resource_line -> resource_health_summary),
//   - --deep inlines each entry's full render instead,
//   - an entry whose object isn't in the cluster is flagged, and is *not* flagged under --shallow,
//     where every lookup comes back empty for an unrelated reason.
//
// It also pins the one --deep behaviour that can't be reached offline: an inlined object naming the
// Kustomization that owns it (via common.tmpl's flux_object_management) without recursing back into
// it. The render engine has no cycle guard, so a deep_render_ref there would not terminate.
//
// Only the CRD is installed, not Flux itself -- see tests/e2e-artifacts/flux-kustomization-crd.yaml
// for why.
func TestE2EFluxKustomizationInventory(t *testing.T) {
	e2eMinikubeTest(t)
	hackOpts, clientset, _ := e2eClients(t)
	opts := combineOpts(hackOpts, viperTestHackOpts())

	ns := "e2e-flux-kustomization"
	_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
	})

	applyManifest(t, "e2e-artifacts/flux-kustomization-crd.yaml")
	require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Established",
		"crd/kustomizations.kustomize.toolkit.fluxcd.io", "--timeout=60s").Run())
	applyManifestInNamespace(t, "e2e-artifacts/flux-kustomization-inventory.yaml", ns)
	waitForInNamespace(t, "deployment/podinfo", "condition=Available", ns)

	// Four entries: three backed by objects this fixture creates (one of them cluster-scoped, so
	// its id has an empty namespace segment and its ref must render without "-n"), and one Secret
	// that is deliberately never created -- the inventory says it was applied, nothing is there.
	patchKustomizationStatus(t, ns, "podinfo", []string{
		fmt.Sprintf("%s_podinfo-config__ConfigMap", ns),
		fmt.Sprintf("%s_podinfo_apps_Deployment", ns),
		fmt.Sprintf("%s_deleted-by-hand__Secret", ns),
		"_e2e-flux-podinfo-viewer_rbac.authorization.k8s.io_ClusterRole",
	})

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
}
