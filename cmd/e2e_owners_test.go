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

func runOwnersSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
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
	// There's no e2e coverage for "ownerReference pointing at a deleted owner is flagged as
	// orphan": a live Secret with a dangling ownerReference gets swept by the built-in garbage
	// collector in well under a second regardless of how the dangling reference comes to exist
	// (deleting the real owner, or patching a fake one onto an existing dependent), so there's
	// no way to construct this scenario against a real cluster without racing the GC on every
	// run. It used to be exercised via --local straight from a manifest -- --local resolved the
	// ownerReference against the real API server without the object itself ever touching the
	// cluster, sidestepping the GC entirely -- but --local no longer does any live queries (see
	// LiveQueriesDisabled), so that path no longer exists either. The orphan-detection logic
	// itself is still covered deterministically: TestOwnersOrphanDetection in
	// pkg/input/input_test.go and TestOwnersTemplate in pkg/plugin/templates_common_test.go
	// exercise it against a fake clientset.
}
