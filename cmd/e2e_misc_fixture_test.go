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
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)
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
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)
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
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)
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
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)
		applyManifestInNamespace(t, "e2e-artifacts/sts-without-service.yaml", ns)
		waitForInNamespace(t, "sts/sts-without-service", "jsonpath={.status.readyReplicas}=1", ns)
		cmdTest{
			args:            []string{"sts/sts-without-service", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/sts-without-service.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("namespace-gatekeeper-constraint-match", func(t *testing.T) {
		t.Parallel()
		// Real Gatekeeper, not CRDs-only: the ConstraintTemplate's status.created has to come
		// from a real reconciler before the generated K8sRequiredLabels Kind even exists for the
		// Constraint below to be created against -- see ensureGatekeeper for why (and why its
		// webhooks are stripped).
		ensureGatekeeper(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-gatekeeper-hint"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)
		// ConstraintTemplate can't use generateName (see the comment on the fixture's own
		// metadata.name for why -- Gatekeeper's real controller rejects it outright), so this
		// goes through applyManifest rather than createFromManifestCapturingName.
		applyManifest(t, "e2e-artifacts/gatekeeper-constrainttemplate.yaml")
		// ConstraintTemplate is cluster-scoped, so this can't go through waitForInNamespace.
		// status.created is set once gatekeeper-controller-manager has generated and established
		// the K8sRequiredLabels CRD from this template.
		output, err := kubectlCmd(t, "wait", "--for=jsonpath={.status.created}=true",
			"constrainttemplate/k8srequiredlabels", "--timeout=120s").CombinedOutput()
		t.Logf("wait result for constrainttemplate/k8srequiredlabels: %s", output)
		require.NoError(t, err)
		// gatekeeper-constraint.yaml's spec.match.namespaces names this namespace directly --
		// the actual signal KubeGetGatekeeperConstraintsMatchingNamespace verifies, rather than
		// merely a ConstraintTemplate (or Gatekeeper) existing somewhere on the cluster. Its
		// generated name is what the regex fixtures below match with a tolerant [a-z0-9]+.
		createFromManifestCapturingName(t, "e2e-artifacts/gatekeeper-constraint.yaml", "k8srequiredlabels")
		cmdTest{
			args:            []string{"namespace/" + ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/namespace-gatekeeper-constraint-match.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"namespace/" + ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/namespace-gatekeeper-constraint-match-deep.regex",
		}.assert(t, nil, opts...)
	})
}
