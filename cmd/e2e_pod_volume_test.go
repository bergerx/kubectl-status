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

func runPodVolumeSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	t.Run("pod-image-pull-secrets", func(t *testing.T) {
		t.Parallel()
		// --shallow (used by the offline golden-file tests) makes KubeGetFirst a no-op,
		// so this e2e suite is the only place that exercises the found-secret validation
		// branches of Pod.tmpl's imagePullSecrets check (Check A) and the "broken secrets"
		// correlation branch of the ImagePullBackOff hint (Check B).
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pod-image-pull-secrets"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/pod-image-pull-secrets.yaml", ns)

		// The kubelet keeps cycling a failing pull between ErrImagePull and ImagePullBackOff on
		// its retry loop; ImagePullBackOff is the longer-lived of the two (exponential backoff),
		// but under concurrent cluster load even that can elapse before a later sibling subtest's
		// render runs -- so re-confirm right before each assert instead of once up front for all
		// three pods.
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-missing-pull-secret", "main", "ImagePullBackOff", ns)
		t.Run("pod referencing a non-existent Secret flags it and correlates with the pull failure", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-missing-pull-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-missing.regex",
			}.assert(t, nil, opts...)
		})
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-wrong-type-pull-secret", "main", "ImagePullBackOff", ns)
		t.Run("pod referencing a wrong-type Secret flags it and correlates with the pull failure", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-wrong-type-pull-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-wrong-type.regex",
			}.assert(t, nil, opts...)
		})
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-healthy-pull-secret", "main", "ImagePullBackOff", ns)
		t.Run("pod referencing a healthy Secret shows no warnings", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-healthy-pull-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-image-pull-secrets-healthy.regex",
			}.assert(t, nil, opts...)
		})
	})
	t.Run("pod-volume-configmap-secret", func(t *testing.T) {
		t.Parallel()
		// --shallow (used by the offline golden-file tests) makes KubeGetFirst a no-op, so
		// this e2e suite is the only place that exercises the configMap/secret volume
		// existence and key-presence checks in Pod.tmpl's pod_volumes/pod_volume_line.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pod-volume-configmap-secret"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/pod-volume-configmap-secret.yaml", ns)
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-volume-missing-configmap", "main", "ContainerCreating", ns)
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-volume-missing-secret", "main", "ContainerCreating", ns)
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-volume-missing-key", "main", "ContainerCreating", ns)
		waitForInNamespace(t, "pod/e2e-pod-volume-optional-missing", "condition=Ready", ns)
		waitForInNamespace(t, "pod/e2e-pod-volume-optional-missing-key", "condition=Ready", ns)
		waitForInNamespace(t, "pod/e2e-pod-volume-healthy", "condition=Ready", ns)

		t.Run("pod referencing a non-existent ConfigMap volume flags it without --include-all-volumes", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-missing-configmap", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-missing-configmap.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("pod referencing a non-existent Secret volume flags it without --include-all-volumes", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-missing-secret", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-missing-secret.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("pod referencing an existing ConfigMap but a missing key flags it", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-missing-key", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-missing-key.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("optional configMap volume referencing a non-existent ConfigMap flags it as an optional-missing note without --include-all-volumes", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-optional-missing", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-optional-missing.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("optional configMap volume with items referencing a missing key shows no warning", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-optional-missing-key", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--include-all-volumes", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-optional-missing-key.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("healthy configMap and secret volumes show no warnings", func(t *testing.T) {
			cmdTest{
				args:            []string{"pod/e2e-pod-volume-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--include-all-volumes", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/pod-volume-configmap-secret-healthy.regex",
			}.assert(t, nil, opts...)
		})
	})
}
