package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

// buildHelmReleaseSecretData encodes release the same way Helm's own storage driver does for its
// Secret backend (base64(gzip(JSON)), see encodeRelease in
// https://github.com/helm/helm/blob/main/pkg/storage/driver/util.go) -- this single explicit
// encoding, plus the outer base64 the Kubernetes API/dynamic client applies automatically to every
// []byte Secret data value on the wire, reproduces the double-base64 layering
// parseHelmReleaseSecret (pkg/plugin/template_functions_static.go) decodes.
func buildHelmReleaseSecretData(t *testing.T, release map[string]interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(release)
	require.NoError(t, err)
	var buf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	require.NoError(t, err)
	_, err = gw.Write(b)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

func runHelmReleaseSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	t.Run("Helm release Secret flags a superseded revision and resolves its manifest's live objects", func(t *testing.T) {
		t.Parallel()
		// A Helm release Secret's data.release is opaque (base64(gzip(JSON))) until decoded, and
		// there was previously no indication at all that a Secret was Helm's own release-storage
		// object, let alone whether it was superseded by a later revision or what it actually
		// deployed. Offline artifact tests (tests/artifacts/secret-helm-release-*) already cover
		// the parse itself and the --shallow/--local fallback (resource_ref, no "Newer revision"
		// line); this exercises the two live-query-only branches: the sibling-Secret revision scan
		// ($.KubeGetByLabelsMap) and resolving a manifest entry to its live object ($.KubeGetFirst
		// / $.IncludeRenderableObject in --deep).
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-helm-release"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		configMapName := "e2e-helm-release-config"
		_, err = clientset.CoreV1().ConfigMaps(ns).Create(context.TODO(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: ns},
			Data:       map[string]string{"foo": "bar"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		manifest := "---\n# Source: e2e-chart/templates/configmap.yaml\n" +
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + configMapName + "\ndata:\n  foo: bar\n"

		releaseName := "e2e-helm-release"
		newRelease := func(revision int, status string) map[string]interface{} {
			return map[string]interface{}{
				"name":      releaseName,
				"namespace": ns,
				"version":   revision,
				"manifest":  manifest,
				"info":      map[string]interface{}{"status": status, "description": "Install complete"},
				"chart": map[string]interface{}{"metadata": map[string]interface{}{
					"name": "e2e-chart", "version": "1.2.3", "appVersion": "2.0.0",
				}},
			}
		}
		newReleaseSecret := func(revision int, status string) *corev1.Secret {
			return &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("sh.helm.release.v1.%s.v%d", releaseName, revision),
					Namespace: ns,
					Labels: map[string]string{
						"name":    releaseName,
						"owner":   "helm",
						"status":  status,
						"version": fmt.Sprintf("%d", revision),
					},
				},
				Type: "helm.sh/release.v1",
				Data: map[string][]byte{"release": buildHelmReleaseSecretData(t, newRelease(revision, status))},
			}
		}

		_, err = clientset.CoreV1().Secrets(ns).Create(context.TODO(), newReleaseSecret(1, "superseded"), metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = clientset.CoreV1().Secrets(ns).Create(context.TODO(), newReleaseSecret(2, "deployed"), metav1.CreateOptions{})
		require.NoError(t, err)

		oldSecretName := fmt.Sprintf("sh.helm.release.v1.%s.v1", releaseName)
		cmdTest{
			args:            []string{"secret/" + oldSecretName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/secret-helm-release-superseded.regex",
		}.assert(t, nil, opts...)
		// --deep inlines the manifest's resolved ConfigMap in full -- offline artifact tests can't
		// reach this branch either, since --shallow/--local (used by every offline test) makes the
		// KubeGetFirst behind it a no-op.
		cmdTest{
			args:            []string{"secret/" + oldSecretName, "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/secret-helm-release-superseded-deep.regex",
		}.assert(t, nil, opts...)
	})
}
