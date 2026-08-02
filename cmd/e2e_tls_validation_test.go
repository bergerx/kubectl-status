package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

// copyLeafSecretToNamespace clones the cert-manager-issued leaf TLS secret into a second
// namespace, so a certificateRef can point across namespaces at genuine certificate content.
// Copying beats issuing a second Certificate there: the CA Issuer is namespaced to srcNS, so
// reaching it from another namespace would mean promoting it to a ClusterIssuer for every other
// subtest too. Creating dstNS is part of the same job: it has to exist before
// tls-validation-crossns-grant.yaml, which is applied into it, lands.
func copyLeafSecretToNamespace(t *testing.T, clientset *kubernetes.Clientset, srcNS, dstNS string) {
	t.Helper()
	_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: dstNS}}, metav1.CreateOptions{})
	t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, dstNS) })
	require.NoError(t, err)
	src, err := clientset.CoreV1().Secrets(srcNS).Get(context.TODO(), "e2e-tls-leaf-tls", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = clientset.CoreV1().Secrets(dstNS).Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: dstNS},
		Type:       src.Type,
		Data:       src.Data,
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

func runTLSValidationSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	ensureCertManager(t)
	ensureGatewayAPICRDs(t)
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
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-ca.yaml", ns)
		waitForInNamespace(t, "certificate/e2e-tls-root-ca", "condition=Ready", ns)
		waitForInNamespace(t, "issuer/e2e-tls-ca-issuer", "condition=Ready", ns)
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-leaf.yaml", ns)
		waitForInNamespace(t, "certificate/e2e-tls-leaf", "condition=Ready", ns)

		t.Run("secret/leaf shows full non-self-signed certificate detail", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"secret/e2e-tls-leaf-tls", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-secret-leaf.regex")
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
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-ingress-healthy.regex")
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
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-gateway-healthy.regex")
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
		t.Run("gateway with wildcard hostname covering the cert shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"gateway/e2e-tls-gw-wildcard-host", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-gateway-wildcard-host.regex")
			assert.NotContains(t, stdout, ", hostname mismatch")
		})
		t.Run("gateway with wildcard hostname outside the cert flags hostname mismatch", func(t *testing.T) {
			cmdTest{
				args:            []string{"gateway/e2e-tls-gw-wildcard-mismatch", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-gateway-wildcard-mismatch.regex",
			}.assert(t, nil, opts...)
		})
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-grpcroute.yaml", ns)
		t.Run("grpcroute attached to healthy gateway listener shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"grpcroute/e2e-tls-grpcroute-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-grpcroute-healthy.regex")
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
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-tlsroute-healthy.regex")
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
		t.Run("tlsroute unpinned to a section ignores listeners it doesn't attach to", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"tlsroute/e2e-tlsroute-multi-listener", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			// The Gateway's other TLS listener terminates a different hostname with a self-signed
			// CA certificate; it never serves this route, so neither flag belongs here.
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-tlsroute-multi-listener.regex")
			for _, problem := range []string{", self-signed", ", hostname mismatch"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("tlsroute attached to a Passthrough listener shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"tlsroute/e2e-tlsroute-passthrough", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-tlsroute-passthrough.regex")
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:", "doesn't exist"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-httproute.yaml", ns)
		t.Run("httproute attached to a healthy listener is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"httproute/e2e-tls-httproute-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-httproute-healthy.regex")
			for _, problem := range []string{"doesn't exist", "wrong type:", "missing keys:", "parse error:", "self-signed", "hostname mismatch"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("httproute unpinned to a section checks the wildcard listener it attaches to", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"httproute/e2e-tls-httproute-wildcard-listener", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			// The cert line's presence is the point: the wildcard listener used to be dropped as
			// non-matching, leaving its certificate unchecked.
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-httproute-wildcard-listener.regex")
			assert.Contains(t, stdout, "cert:Secret/e2e-tls-leaf-tls")
			assert.NotContains(t, stdout, "hostname mismatch")
		})
		t.Run("httproute with a wildcard hostname covering the cert shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"httproute/e2e-tls-httproute-wildcard-host", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-httproute-wildcard-host.regex")
			assert.NotContains(t, stdout, "hostname mismatch")
		})
		t.Run("httproute attached to a mismatched-hostname listener flags hostname mismatch", func(t *testing.T) {
			cmdTest{
				args:            []string{"httproute/e2e-tls-httproute-mismatch", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-httproute-mismatch.regex",
			}.assert(t, nil, opts...)
		})
		certsNS := ns + "-certs"
		copyLeafSecretToNamespace(t, clientset, ns, certsNS)
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-crossns-grant.yaml", certsNS)
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-crossns.yaml", ns)
		t.Run("httproute whose listener certificate lives in another namespace names that namespace", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"httproute/e2e-tls-httproute-crossns", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-crossns-httproute.regex")
			// The namespace is the whole point: a bare "cert:Secret/e2e-tls-leaf-tls" would
			// point at a Secret that doesn't exist in the route's own namespace.
			assert.Contains(t, stdout, "cert:Secret/e2e-tls-leaf-tls -n "+certsNS)
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:", "doesn't exist"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("tlsroute whose listener certificate lives in another namespace names that namespace", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"tlsroute/e2e-tlsroute-crossns", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			assertStdoutMatchesRegexFixture(t, stdout, "e2e-artifacts/tls-validation-crossns-tlsroute.regex")
			assert.Contains(t, stdout, "cert:Secret/e2e-tls-leaf-tls -n "+certsNS)
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:", "doesn't exist"} {
				assert.NotContains(t, stdout, problem)
			}
		})
	})
}
