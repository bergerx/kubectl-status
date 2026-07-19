package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

// TestE2EParallel is a dedicated home for e2e subtests that are independent of each other and can
// therefore run concurrently. RootCmd (cmd/main.go) and pkg/plugin no longer read a process-global
// viper singleton or package-level Now/DurationRound/StartedAfterClause overrides -- each RootCmd()
// call owns its own *viper.Viper and plugin.RenderConfig (see #694), and testHackOpts/
// viperTestHackOpts just build option values rather than mutating shared state, so calling them
// from concurrent subtests is safe. The two remaining process-global sinks on the render path --
// cmdutil.BehaviorOnFatal in RootCmd's RunE and slog.SetDefault in newRenderEngine's
// setupDeprecationFilter -- are also now safe under concurrent RootCmd().Execute() calls: the
// former is guarded by cmd/main.go's fatalMu, held only around installing/consuming the handler
// rather than around the render itself; the latter installs its filtering handler once per
// process (sync.Once) instead of rebinding it on every render (see #701). A subtest qualifies for
// t.Parallel() once it:
//   - needs no namespace, or creates/uses a namespace dedicated to that subtest (never `default`,
//     and never a namespace another subtest might also touch)
//   - never relies on a fixed cluster-scoped resource name (Node, CustomResourceDefinition,
//     ClusterRole, ...) another subtest could also use -- generate one instead, e.g. with
//     GenerateName (see createBadNode)
//
// Add a qualifying subtest with t.Run(name, func(t *testing.T) { t.Parallel(); ... }) so it
// actually runs alongside its siblings instead of just living next to them; that subtest-level
// t.Parallel() is what makes siblings run concurrently, regardless of this function's own.
//
// This function itself must NOT call t.Parallel(): e2eMinikubeTest below falls back to
// startMinikube, which calls t.Setenv("KUBECONFIG", ...) for ad hoc `go test -run TestE2E...` runs
// that don't set ASSUME_MINIKUBE_IS_CONFIGURED=true -- and t.Setenv panics if called on a test
// already marked parallel.
func TestE2EParallel(t *testing.T) {
	e2eMinikubeTest(t)
	hackOpts, clientset, dynamicClient := e2eClients(t)
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
	t.Run("pod on a cordoned node with an untolerated taint and a bad condition", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-bad-node-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		nodeName := createBadNode(t, clientset)

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-on-bad-node",
				Namespace: ns,
			},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"pod/pod-on-bad-node", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-on-bad-node.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod's serviceAccountName resolves to the ServiceAccount and surfaces automount/imagePullSecrets", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(viperTestHackOpts())
		ns := "e2e-pod-custom-sa"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		f := false
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kubectl-status-test-sa",
				Namespace: ns,
			},
			AutomountServiceAccountToken: &f,
			ImagePullSecrets:             []corev1.LocalObjectReference{{Name: "regcred"}},
		}
		_, err = clientset.CoreV1().ServiceAccounts(ns).Create(context.TODO(), sa, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().ServiceAccounts(ns).Delete(context.TODO(), sa.Name, metav1.DeleteOptions{})

		// The ServiceAccount admission plugin merges its imagePullSecrets onto every Pod that
		// uses it, so Pod.tmpl's own (pre-existing) imagePullSecrets check will flag "regcred" as
		// missing unless it actually exists with the expected type.
		regcred := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "regcred", Namespace: ns},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{".dockerconfigjson": []byte("{}")},
		}
		_, err = clientset.CoreV1().Secrets(ns).Create(context.TODO(), regcred, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Secrets(ns).Delete(context.TODO(), regcred.Name, metav1.DeleteOptions{})

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-custom-sa",
				Namespace: ns,
			},
			Spec: corev1.PodSpec{
				ServiceAccountName: sa.Name,
				Containers:         []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"pod/pod-with-custom-sa", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-with-custom-sa.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod referencing a missing ServiceAccount surfaces a doesn't-exist warning", func(t *testing.T) {
		t.Parallel()
		// A real cluster's ServiceAccount admission plugin rejects a Pod at creation time when
		// its serviceAccountName doesn't resolve, so this scenario can't be created directly --
		// instead a Pod is created against a real ServiceAccount (which admission accepts), then
		// the ServiceAccount is deleted out from under it. Nothing reconciles a running Pod's
		// serviceAccountName after admission, so the Pod is left referencing a name that no
		// longer resolves, which is exactly the "doesn't exist" case being tested -- just
		// reached via drift instead of an upfront invalid manifest.
		opts := combineOpts(viperTestHackOpts())
		ns := "e2e-pod-missing-sa"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "will-be-deleted", Namespace: ns},
		}
		_, err = clientset.CoreV1().ServiceAccounts(ns).Create(context.TODO(), sa, metav1.CreateOptions{})
		require.NoError(t, err)

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-missing-service-account",
				Namespace: ns,
			},
			Spec: corev1.PodSpec{
				ServiceAccountName: sa.Name,
				Containers:         []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		require.NoError(t, clientset.CoreV1().ServiceAccounts(ns).Delete(context.TODO(), sa.Name, metav1.DeleteOptions{}))

		cmdTest{
			args:            []string{"pod/pod-missing-service-account", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-missing-service-account.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod's runtimeClassName resolves to the RuntimeClass and surfaces overhead.podFixed", func(t *testing.T) {
		t.Parallel()
		// No real gVisor/Kata handler is installed on the e2e cluster, so the RuntimeClass object
		// is created directly (its "handler" field is never resolved to an actual runtime by this
		// test) -- the goal is validating the fetch-and-render path, not sandboxed execution. The
		// Pod is created for real (not rendered via --local): --local's builder can't mix -f with a
		// secondary lookup that's expected to resolve (KubeGetFirst always hits the "when paths,
		// URLs, or stdin is provided as input, you may not specify resource arguments as well"
		// error in that mode -- invisible for lookups expected to fail, like the missing-SA case
		// above, since a builder error and a real not-found both render as "doesn't exist", but
		// fatal for a lookup that's expected to succeed). The API server's RuntimeClass admission
		// plugin will also copy overhead.podFixed into this Pod's own spec.overhead, but Pod.tmpl
		// reads it from the fetched RuntimeClass object, not spec.overhead, so that duplication
		// doesn't undermine what's being verified here.
		opts := combineOpts(viperTestHackOpts())
		ns := "e2e-pod-runtimeclass-overhead"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		rc := &nodev1.RuntimeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-test-gvisor"},
			Handler:    "runsc",
			Overhead: &nodev1.Overhead{
				PodFixed: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
			},
		}
		_, err = clientset.NodeV1().RuntimeClasses().Create(context.TODO(), rc, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.NodeV1().RuntimeClasses().Delete(context.TODO(), rc.Name, metav1.DeleteOptions{})
		})

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-runtimeclass-overhead",
				Namespace: ns,
			},
			Spec: corev1.PodSpec{
				RuntimeClassName: &rc.Name,
				Containers:       []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"pod/pod-with-runtimeclass-overhead", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-with-runtimeclass-overhead.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("workload's matching pod on a cordoned node surfaces a compact node-problem flag", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-bad-node-rs"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		nodeName := createBadNode(t, clientset)

		// The Pod's spec.nodeName is set directly at creation, bypassing the scheduler, so it
		// never actually runs -- ReplicaSet.tmpl's selector-based pod lookup only needs matching
		// labels, not real ownership, to include it in the health summary.
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-on-bad-node-for-rs",
				Namespace: ns,
				Labels:    map[string]string{"app": "kubectl-status-test-bad-rs"},
			},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		one := int32(1)
		rs := &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bad-rs",
				Namespace: ns,
			},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "kubectl-status-test-bad-rs"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "kubectl-status-test-bad-rs"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
				},
			},
		}
		_, err = clientset.AppsV1().ReplicaSets(ns).Create(context.TODO(), rs, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.AppsV1().ReplicaSets(ns).Delete(context.TODO(), rs.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"rs/bad-rs", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-on-bad-node-for-rs.regex",
		}.assert(t, nil, opts...)
	})
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
	t.Run("deployment rollout with --include-rollout-diffs shows the diff between revisions", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-rollout-diff"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		name := "rollout-diff-test"
		one := int32(1)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}}},
				},
			},
		}
		_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.AppsV1().Deployments(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
		waitForInNamespace(t, "deployment/"+name, "condition=Available", ns)

		// Update the image so a second ReplicaSet revision is created, giving --include-rollout-diffs
		// something to diff.
		dep, err = clientset.AppsV1().Deployments(ns).Get(context.TODO(), name, metav1.GetOptions{})
		require.NoError(t, err)
		dep.Spec.Template.Spec.Containers[0].Image = "nginx:1.26"
		_, err = clientset.AppsV1().Deployments(ns).Update(context.TODO(), dep, metav1.UpdateOptions{})
		require.NoError(t, err)
		rolloutCmd := exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
		output, err := rolloutCmd.CombinedOutput()
		t.Logf("rollout status for %s: %s", name, output)
		require.NoError(t, err)

		// The order in which the two ReplicaSet revisions are diffed (and so which side
		// gets "-" vs "+") isn't guaranteed, so the fixture alternates both directions.
		cmdTest{
			args:            []string{"deployment/" + name, "-n", ns, "--include-rollout-diffs", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/rollout-diff.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("Rollouts section shows a single blocked rollout even without a second one to compare against", func(t *testing.T) {
		t.Parallel()
		// #213: the Rollouts list used to be suppressed unless there were 2+ rollouts to
		// compare, hiding a stuck or unhealthy first/only rollout. It should now also show up
		// for a single rollout that isn't done yet.
		badImage := "kubectl-status-e2e-this-image-does-not-exist"

		t.Run("deployment", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-blocked-deployment"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-blocked-deployment"
			one := int32(1)
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.DeploymentSpec{
					Replicas: &one,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: badImage}}},
					},
				},
			}
			_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().Deployments(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			podName := waitForPodByLabel(t, ns, "app="+name)
			waitForContainerWaitingReasonInNamespace(t, "pod/"+podName, "app", "ImagePullBackOff", ns)

			cmdTest{
				args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-deployment.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("statefulset", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-blocked-statefulset"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-blocked-statefulset"
			one := int32(1)
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    &one,
					ServiceName: name,
					Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: badImage}}},
					},
				},
			}
			_, err = clientset.AppsV1().StatefulSets(ns).Create(context.TODO(), sts, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().StatefulSets(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			waitForContainerWaitingReasonInNamespace(t, "pod/"+name+"-0", "app", "ImagePullBackOff", ns)

			cmdTest{
				args:            []string{"statefulset/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-statefulset.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("daemonset", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-blocked-daemonset"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-blocked-daemonset"
			ds := &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.DaemonSetSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: badImage}}},
					},
				},
			}
			_, err = clientset.AppsV1().DaemonSets(ns).Create(context.TODO(), ds, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().DaemonSets(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			podName := waitForPodByLabel(t, ns, "app="+name)
			waitForContainerWaitingReasonInNamespace(t, "pod/"+podName, "app", "ImagePullBackOff", ns)

			cmdTest{
				args:            []string{"daemonset/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-blocked-daemonset.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("healthy single rollout stays suppressed", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-healthy-deployment"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-healthy-deployment"
			one := int32(1)
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.DeploymentSpec{
					Replicas: &one,
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}}},
					},
				},
			}
			_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().Deployments(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			waitForInNamespace(t, "deployment/"+name, "condition=Available", ns)

			cmdTest{
				args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-single-healthy-deployment.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("three healthy revisions with --include-rollout-diffs shows both consecutive diffs", func(t *testing.T) {
			// Needs two distinct spec changes (three revisions total) before the check, so
			// there are two consecutive pairs to diff, not just the one covered by the
			// "--include-rollout-diffs shows the diff between revisions" test above.
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-three-revisions"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			name := "e2e-rollouts-three-revisions"
			applyManifestInNamespace(t, "e2e-artifacts/rollouts-three-revisions.yaml", ns)
			waitForInNamespace(t, "deployment/"+name, "condition=Available", ns)

			out, err := exec.Command("kubectl", "set", "image", "deployment/"+name, "nginx=nginx:1.26", "-n", ns).CombinedOutput()
			require.NoError(t, err, string(out))
			rolloutCmd := exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
			output, err := rolloutCmd.CombinedOutput()
			t.Logf("rollout status for %s (nginx:1.26): %s", name, output)
			require.NoError(t, err)
			waitForSinglePod(t, ns, "app="+name)

			out, err = exec.Command("kubectl", "set", "image", "deployment/"+name, "nginx=nginx:1.27", "-n", ns).CombinedOutput()
			require.NoError(t, err, string(out))
			rolloutCmd = exec.Command("kubectl", "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
			output, err = rolloutCmd.CombinedOutput()
			t.Logf("rollout status for %s (nginx:1.27): %s", name, output)
			require.NoError(t, err)
			waitForSinglePod(t, ns, "app="+name)

			cmdTest{
				args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--include-rollout-diffs", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-three-revisions-with-diffs.regex",
			}.assert(t, nil, opts...)
		})
	})
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
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-ca.yaml", ns)
		waitForInNamespace(t, "certificate/e2e-tls-root-ca", "condition=Ready", ns)
		waitForInNamespace(t, "issuer/e2e-tls-ca-issuer", "condition=Ready", ns)
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-leaf.yaml", ns)
		waitForInNamespace(t, "certificate/e2e-tls-leaf", "condition=Ready", ns)

		t.Run("secret/leaf shows full non-self-signed certificate detail", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"secret/e2e-tls-leaf-tls", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-secret-leaf.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
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
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-ingress-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
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
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-gateway-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
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
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-grpcroute.yaml", ns)
		t.Run("grpcroute attached to healthy gateway listener shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"grpcroute/e2e-tls-grpcroute-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-grpcroute-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
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
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-tlsroute-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
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
		t.Run("tlsroute attached to a Passthrough listener shows no cert flags", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"tlsroute/e2e-tlsroute-passthrough", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-tlsroute-passthrough.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{", self-signed", ", hostname mismatch", "wrong type:", "missing keys:", "parse error:", "doesn't exist"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		applyManifestInNamespace(t, "e2e-artifacts/tls-validation-httproute.yaml", ns)
		t.Run("httproute attached to a healthy listener is healthy", func(t *testing.T) {
			stdout, _, err := executeCMD(t, []string{"httproute/e2e-tls-httproute-healthy", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"}, opts...)
			require.NoError(t, err)
			regexBytes, rerr := os.ReadFile(path.Join("..", "tests", "e2e-artifacts", "tls-validation-httproute-healthy.regex"))
			require.NoError(t, rerr)
			assert.Regexp(t, `(?ms)`+string(regexBytes), stdout)
			for _, problem := range []string{"doesn't exist", "wrong type:", "missing keys:", "parse error:", "self-signed", "hostname mismatch"} {
				assert.NotContains(t, stdout, problem)
			}
		})
		t.Run("httproute attached to a mismatched-hostname listener flags hostname mismatch", func(t *testing.T) {
			cmdTest{
				args:            []string{"httproute/e2e-tls-httproute-mismatch", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/tls-validation-httproute-mismatch.regex",
			}.assert(t, nil, opts...)
		})
	})
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
	t.Run("pod-container-logs", func(t *testing.T) {
		t.Parallel()
		// --shallow (used by the offline golden-file tests) makes KubeGetContainerLogs a
		// no-op, so this e2e suite is the only place that exercises real log fetching: a
		// terminated init container with output (current-state logs), a terminated init
		// container with no output (yellow "no logs to show"), a crashlooping regular
		// container that has recently restarted (previous-instance logs), and a healthy
		// sidecar plus a healthy regular container that should show neither.
		//
		// withinLastHour compares real container timestamps against nowFunc, so the
		// suite-wide fixed clock (testHack, frozen at 2026-06-30) has to be swapped for the
		// real wall clock for this render, or a live restart looks like it happened in the
		// future and never matches.
		opts := combineOpts(hackOpts, viperTestHackOpts(), []func(*plugin.RenderConfig){
			func(cfg *plugin.RenderConfig) { cfg.Now = time.Now },
		})
		ns := "e2e-pod-container-logs"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})
		applyManifestInNamespace(t, "e2e-artifacts/pod-container-logs.yaml", ns)
		// The fixture pins a usage line for both healthy containers -- wait for metrics-server to
		// have scraped each of them specifically, not just the Pod overall: a container that
		// started slightly later than its siblings can still be missing from PodMetrics even once
		// the pod-level object exists, which otherwise renders that container's usage line blank.
		// Done before the CrashLoopBackOff wait below (not after): metrics-server's scrape
		// interval can take tens of seconds, and the crasher container keeps cycling
		// Waiting/Terminated the whole time it waits, so checking CrashLoopBackOff first and
		// then waiting on metrics just reintroduces the same flip the CrashLoopBackOff wait was
		// meant to avoid, by leaving a wide gap between the check and the actual assertion.
		waitForContainerMetrics(t, ns, "e2e-pod-container-logs", "healthy", "sidecar")
		// Wait for a stable Waiting(CrashLoopBackOff) state rather than just restartCount > 0:
		// the container's current state otherwise flips between Waiting and Terminated(Error)
		// as the kubelet retries, which would make the golden regex flaky. This has to be the
		// last wait before the assertion below -- see the comment above.
		waitForContainerWaitingReasonInNamespace(t, "pod/e2e-pod-container-logs", "crasher", "CrashLoopBackOff", ns)

		cmdTest{
			args:            []string{"pod/e2e-pod-container-logs", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-container-logs.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("node correctly resolves pod metrics for pods in multiple namespaces via the batched PodMetrics lookup", func(t *testing.T) {
		t.Parallel()
		// Node.tmpl loops over every pod on the node (KubeGetNonTerminatedPodsOnNode) and looks
		// up each one's PodMetrics via KubeGetPodMetrics, which fetches metrics.k8s.io once for
		// the whole render (cluster-wide, or per-namespace as a fallback) instead of once per
		// pod. Pods in two distinct namespaces exercise the namespace-aware lookup within that
		// shared result: only --shallow-free live runs touch this path at all (see
		// TestAllArtifactsLocal), so this is the only place it's covered.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		var nodeName string
		for _, n := range nodes.Items {
			if n.Spec.Unschedulable {
				continue
			}
			for _, cond := range n.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					nodeName = n.Name
				}
			}
			if nodeName != "" {
				break
			}
		}
		require.NotEmpty(t, nodeName, "expected at least one schedulable, Ready node")

		for _, ns := range []string{"e2e-node-metrics-a", "e2e-node-metrics-b"} {
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			require.NoError(t, err)
			t.Cleanup(func() {
				clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
			})
			_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-metrics-pod", Namespace: ns},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "busybox", Command: []string{"sleep", "infinity"}}},
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
				"pod/e2e-metrics-pod", "-n", ns, "--timeout=4m").Run())
			waitForPodMetrics(t, ns, "e2e-metrics-pod")
		}

		cmdTest{
			args:            []string{"node/" + nodeName, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/node-metrics-multi-namespace.regex",
		}.assert(t, nil, opts...)
	})
}
