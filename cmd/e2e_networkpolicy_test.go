package main

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func runNetworkPolicySubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset, dynamicClient dynamic.Interface) {
	ensureCiliumCalicoCRDs(t)
	t.Run("pod selected by a NetworkPolicy surfaces the compact isolation signal", func(t *testing.T) {
		t.Parallel()
		// A dedicated namespace keeps this test in control of exactly which NetworkPolicy
		// objects exist -- an empty podSelector elsewhere in a shared namespace (e.g. "default")
		// would also match this Pod and make the asserted policy name/count non-deterministic.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-netpol-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netpol-selected-pod",
				Namespace: ns,
				Labels:    map[string]string{"app": "kubectl-status-test-netpol-target"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "busybox", Command: []string{"sleep", "infinity"}}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		netpol := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "deny-ingress-to-app", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "kubectl-status-test-netpol-target"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			},
		}
		_, err = clientset.NetworkingV1().NetworkPolicies(ns).Create(context.TODO(), netpol, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.NetworkingV1().NetworkPolicies(ns).Delete(context.TODO(), netpol.Name, metav1.DeleteOptions{})

		// The full-output regex fixture below pins the Pod to a Running/Ready state, so this
		// must wait rather than race the kubelet -- otherwise the render can catch it Pending.
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
			"pod/netpol-selected-pod", "-n", ns, "--timeout=4m").Run())

		cmdTest{
			args:            []string{"pod/netpol-selected-pod", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-selected-by-network-policy.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod selected by multiple NetworkPolicies lists all of them and unions both directions", func(t *testing.T) {
		t.Parallel()
		// Same isolation rationale as the single-policy case above, but with three policies that
		// each cover only part of the picture -- a default-deny (both directions, no rules), an
		// egress-only allow, and an ingress-only allow -- to exercise the union across matching
		// policies (Kubernetes NetworkPolicy is additive/OR'd, never a single winning policy) and
		// the multi-name/plural "NetworkPolicies" wording. Names are chosen to sort the same way
		// alphabetically as they're created below: the underlying list (server-side, not an
		// informer cache) comes back name-ordered, and creationTimestamp -- the only explicit
		// sort key applied -- has second granularity, so objects created in the same second (as
		// these three are) keep that name order rather than creation order.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-netpol-multi-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		podLabels := map[string]string{"app": "kubectl-status-test-netpol-multi-target"}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netpol-multi-selected-pod",
				Namespace: ns,
				Labels:    podLabels,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "busybox", Command: []string{"sleep", "infinity"}}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		defaultDeny := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "default-deny-both", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			},
		}
		_, err = clientset.NetworkingV1().NetworkPolicies(ns).Create(context.TODO(), defaultDeny, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.NetworkingV1().NetworkPolicies(ns).Delete(context.TODO(), defaultDeny.Name, metav1.DeleteOptions{})

		egressOnly := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "egress-only", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
			},
		}
		_, err = clientset.NetworkingV1().NetworkPolicies(ns).Create(context.TODO(), egressOnly, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.NetworkingV1().NetworkPolicies(ns).Delete(context.TODO(), egressOnly.Name, metav1.DeleteOptions{})

		ingressOnly := &networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "ingress-only", Namespace: ns},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress:     []networkingv1.NetworkPolicyIngressRule{{}},
			},
		}
		_, err = clientset.NetworkingV1().NetworkPolicies(ns).Create(context.TODO(), ingressOnly, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.NetworkingV1().NetworkPolicies(ns).Delete(context.TODO(), ingressOnly.Name, metav1.DeleteOptions{})

		// The full-output regex fixture below pins the Pod to a Running/Ready state, so this
		// must wait rather than race the kubelet -- otherwise the render can catch it Pending.
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
			"pod/netpol-multi-selected-pod", "-n", ns, "--timeout=4m").Run())

		cmdTest{
			args:            []string{"pod/netpol-multi-selected-pod", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-selected-by-multiple-network-policies.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod selected by CiliumNetworkPolicy/CiliumClusterwideNetworkPolicy and Calico NetworkPolicy/GlobalNetworkPolicy surfaces each compact signal", func(t *testing.T) {
		t.Parallel()
		// These CRDs are only installed standalone (via install-e2e-deps), without Cilium or
		// Calico actually running as the cluster's CNI -- kubectl-status only ever matches these
		// objects' selectors against the Pod's own labels client-side, it never depends on either
		// CNI actually enforcing traffic, so the CRDs alone are enough to exercise this.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-cni-policy-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		appLabel := "kubectl-status-test-cni-policy-target"
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cni-policy-selected-pod",
				Namespace: ns,
				Labels:    map[string]string{"app": appLabel},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "busybox", Command: []string{"sleep", "infinity"}}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cnpGVR := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
		cnp := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumNetworkPolicy",
			"metadata":   map[string]interface{}{"name": "cnp-ingress", "namespace": ns},
			"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": appLabel}},
				"ingress":          []interface{}{map[string]interface{}{}},
			},
		}}
		_, err = dynamicClient.Resource(cnpGVR).Namespace(ns).Create(context.TODO(), cnp, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(cnpGVR).Namespace(ns).Delete(context.TODO(), cnp.GetName(), metav1.DeleteOptions{})

		ccnpGVR := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
		ccnp := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumClusterwideNetworkPolicy",
			"metadata":   map[string]interface{}{"name": "ccnp-egress-" + ns},
			"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": appLabel}},
				"egress":           []interface{}{map[string]interface{}{}},
			},
		}}
		_, err = dynamicClient.Resource(ccnpGVR).Create(context.TODO(), ccnp, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(ccnpGVR).Delete(context.TODO(), ccnp.GetName(), metav1.DeleteOptions{})

		calicoNpGVR := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "networkpolicies"}
		calicoNp := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "crd.projectcalico.org/v1",
			"kind":       "NetworkPolicy",
			"metadata":   map[string]interface{}{"name": "calico-np-ingress", "namespace": ns},
			"spec": map[string]interface{}{
				"selector": fmt.Sprintf("app == '%s'", appLabel),
				"types":    []interface{}{"Ingress"},
			},
		}}
		_, err = dynamicClient.Resource(calicoNpGVR).Namespace(ns).Create(context.TODO(), calicoNp, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(calicoNpGVR).Namespace(ns).Delete(context.TODO(), calicoNp.GetName(), metav1.DeleteOptions{})

		calicoGnpGVR := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"}
		calicoGnp := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "crd.projectcalico.org/v1",
			"kind":       "GlobalNetworkPolicy",
			"metadata":   map[string]interface{}{"name": "calico-gnp-egress-" + ns},
			"spec": map[string]interface{}{
				"selector":          fmt.Sprintf("app == '%s'", appLabel),
				"namespaceSelector": fmt.Sprintf("projectcalico.org/name == '%s'", ns),
				"types":             []interface{}{"Egress"},
			},
		}}
		_, err = dynamicClient.Resource(calicoGnpGVR).Create(context.TODO(), calicoGnp, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(calicoGnpGVR).Delete(context.TODO(), calicoGnp.GetName(), metav1.DeleteOptions{})

		// The full-output regex fixture below pins the Pod to a Running/Ready state, so this
		// must wait rather than race the kubelet -- otherwise the render can catch it Pending.
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
			"pod/cni-policy-selected-pod", "-n", ns, "--timeout=4m").Run())

		cmdTest{
			args:            []string{"pod/cni-policy-selected-pod", "-n", ns, "--include-events=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-selected-by-cilium-and-calico-network-policies.regex",
		}.assert(t, nil, opts...)
	})
}
