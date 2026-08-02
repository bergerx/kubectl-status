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

// runIstioSubtests is one of TestE2EParallel's topical groups (cmd/main_test.go). It qualifies on
// both of that function's criteria: it owns a namespace nothing else touches
// (e2e-istio-routing), and every object it asserts on is namespaced -- the only cluster-scoped
// thing in play is the CRD pair ensureIstioCRDs installs, a shared onceInstaller like the other
// groups' bundles rather than a fixed object name this could collide on.
//
// It covers the live-query branches of VirtualService.tmpl and DestinationRule.tmpl, which every
// offline artifact under tests/artifacts/{virtualservice,destinationrule}-* leaves untouched
// because --shallow/--local make KubeGet/KubeGetFirst no-ops:
//
//   - a destination whose subset no paired DestinationRule declares, and one whose host names no
//     Service -- the two ways a route that reads correctly still 503s,
//   - a subset whose labels select no Pod, which needs the Service's own selector intersected
//     with the subset's before it can be counted at all,
//   - --deep inlining those Pods instead of counting them,
//   - and none of the three firing under --shallow, where every lookup comes back empty for an
//     unrelated reason and calling that a missing Service would be a lie.
func runIstioSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	t.Run("istio-routing", func(t *testing.T) {
		t.Parallel()
		ensureIstioCRDs(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-istio-routing"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		// A StatefulSet rather than a Deployment so the Pod the --deep render inlines has a
		// predictable name, the same reason the service-routing scenarios use one.
		applyManifestInNamespace(t, "e2e-artifacts/istio-routing.yaml", ns)
		waitForInNamespace(t, "sts/reviews-v1", "jsonpath={.status.readyReplicas}=1", ns)
		cmdTest{
			args:            []string{"virtualservice/reviews", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/istio-routing.virtualservice.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"virtualservice/reviews", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--shallow", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/istio-routing.virtualservice-shallow.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"destinationrule/reviews", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/istio-routing.destinationrule.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"destinationrule/reviews", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--deep", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/istio-routing.destinationrule-deep.regex",
		}.assert(t, nodeNameModifier, opts...)
		cmdTest{
			args:            []string{"destinationrule/reviews-legacy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/istio-routing.destinationrule-no-service.regex",
		}.assert(t, nil, opts...)
	})
}
