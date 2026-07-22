package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func runPodSchedulingSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset, dynamicClient dynamic.Interface) {
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
	t.Run("pod's priorityClassName resolves to the PriorityClass and surfaces value/globalDefault/preemptionPolicy", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(viperTestHackOpts())
		ns := "e2e-pod-priorityclass"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		// A minikube cluster has no globalDefault PriorityClass out of the box, so setting it here
		// is safe: it won't clash with any other PriorityClass in the (parallel) test pool.
		never := corev1.PreemptNever
		pc := &schedulingv1.PriorityClass{
			ObjectMeta:       metav1.ObjectMeta{Name: "e2e-test-priority"},
			Value:            1000000,
			GlobalDefault:    true,
			PreemptionPolicy: &never,
		}
		_, err = clientset.SchedulingV1().PriorityClasses().Create(context.TODO(), pc, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.SchedulingV1().PriorityClasses().Delete(context.TODO(), pc.Name, metav1.DeleteOptions{})
		})

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pod-with-priorityclass",
				Namespace: ns,
			},
			Spec: corev1.PodSpec{
				PriorityClassName: pc.Name,
				Containers:        []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), pod, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.CoreV1().Pods(ns).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})

		cmdTest{
			args:            []string{"pod/pod-with-priorityclass", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-with-priorityclass.regex",
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
	t.Run("pod nodeSelector key no NodePool declares surfaces a Karpenter incompatibility, a satisfiable one stays silent", func(t *testing.T) {
		t.Parallel()
		// No real Karpenter controller runs here (CRDs only, see ensureKarpenterCRDs), so neither
		// Pod below is ever actually provisioned for -- ordinary real-node scheduling failure
		// (no matching Node exists in this minikube cluster either) is what keeps them Pending,
		// which is all that's needed to exercise the render path: it only reads the NodePool's
		// declared spec.requirements, never its status/conditions (never populated without a
		// reconciler) or whether a NodeClaim was actually created.
		ensureKarpenterCRDs(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-karpenter-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		// This NodePool only declares a zone requirement -- it says nothing about the custom
		// label the first Pod below hard-requires (so every NodePool disqualifies on that key),
		// and its only allowed zone value is exactly what the second Pod requires (so no key
		// disqualifies every NodePool for that Pod).
		nodePoolGVR := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
		nodePoolName := "e2e-karpenter-pool-" + ns
		nodePool := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "karpenter.sh/v1",
			"kind":       "NodePool",
			"metadata":   map[string]interface{}{"name": nodePoolName},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"nodeClassRef": map[string]interface{}{
							"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default",
						},
						"requirements": []interface{}{
							map[string]interface{}{
								"key": "topology.kubernetes.io/zone", "operator": "In",
								"values": []interface{}{"e2e-zone-a"},
							},
						},
					},
				},
			},
		}}
		_, err = dynamicClient.Resource(nodePoolGVR).Create(context.TODO(), nodePool, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			dynamicClient.Resource(nodePoolGVR).Delete(context.TODO(), nodePoolName, metav1.DeleteOptions{})
		})

		unsatisfiablePodName := "karpenter-unsatisfiable-pod"
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: unsatisfiablePodName, Labels: map[string]string{"app": unsatisfiablePodName}},
			Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"workload.example.com/tier": "stateful"},
				Containers:   []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods(ns).Delete(context.TODO(), unsatisfiablePodName, metav1.DeleteOptions{})
		})
		waitForInNamespace(t, "pod/"+unsatisfiablePodName,
			`jsonpath={.status.conditions[?(@.type=="PodScheduled")].status}=False`, ns)
		waitForPodScheduleWindow(t, ns, "app="+unsatisfiablePodName)

		cmdTest{
			args:            []string{"pod/" + unsatisfiablePodName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-karpenter-unsatisfiable.regex",
		}.assert(t, nil, opts...)

		satisfiablePodName := "karpenter-satisfiable-pod"
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: satisfiablePodName, Labels: map[string]string{"app": satisfiablePodName}},
			Spec: corev1.PodSpec{
				NodeSelector: map[string]string{"topology.kubernetes.io/zone": "e2e-zone-a"},
				Containers:   []corev1.Container{{Name: "app", Image: "busybox"}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods(ns).Delete(context.TODO(), satisfiablePodName, metav1.DeleteOptions{})
		})
		waitForInNamespace(t, "pod/"+satisfiablePodName,
			`jsonpath={.status.conditions[?(@.type=="PodScheduled")].status}=False`, ns)
		waitForPodScheduleWindow(t, ns, "app="+satisfiablePodName)

		cmdTest{
			args:            []string{"pod/" + satisfiablePodName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-karpenter-satisfiable.regex",
		}.assert(t, nil, opts...)
	})
}
