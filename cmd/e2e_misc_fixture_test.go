package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func runMiscFixtureSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
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
		// Unlike a Pod's serviceAccountName, a ValidatingAdmissionPolicyBinding's policyName
		// isn't checked by admission at creation time, so the binding can be created for real
		// with a policyName that never resolves.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-orphan-binding"},
			Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
				PolicyName:        "e2e-does-not-exist",
				ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny},
			},
		}
		_, err := clientset.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Create(context.TODO(), binding, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Delete(context.TODO(), binding.Name, metav1.DeleteOptions{})
		})

		cmdTest{
			args:            []string{"validatingadmissionpolicybinding/e2e-orphan-binding", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
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
}
