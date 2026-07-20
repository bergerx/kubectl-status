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

func runServiceRoutingSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	ensureGatewayAPICRDs(t)
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
}
