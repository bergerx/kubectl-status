package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

// runDRASubtests covers Dynamic Resource Allocation (ResourceClaim/DeviceClass/ResourceSlice,
// plus Pod's pod_device_claims section) -- qualifies for TestE2EParallel's pool like everything
// else here (own namespace, and the two cluster-scoped kinds it touches -- DeviceClass,
// ResourceSlice -- use names no other subtest's domain ever creates, so there's nothing to
// collide with; see CONTRIBUTING.md's Parallel-Safe e2e Subtests section).
//
// Neither subtest needs a real DRA driver. A ResourceSlice is just an API object a driver would
// normally publish -- nothing stops a test from publishing one directly, and kube-scheduler's
// dynamicresources plugin allocates against it for real (matches the DeviceClass's CEL selector,
// picks the device, reserves the claim for the consuming Pod) with no driver controller involved.
// Only the kubelet-side NodePrepareResources step -- driver-specific, needing a real gRPC/CDI
// plugin registered under that driver name -- never completes, which is why the Pod in the second
// subtest never leaves Pending/ContainerCreating. Everything asserted on here is set by the
// scheduler before that point.
func runDRASubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	t.Run("ResourceClaim and DeviceClass render an unallocated claim, and a Pod flags a missing direct claim reference", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-dra-unalloc"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		// No ResourceSlice anywhere in the cluster ever advertises this driver, so the claim
		// below can never be allocated -- deterministic, no scheduler race to wait out.
		className := "e2e-dra-unsatisfiable"
		_, err = clientset.ResourceV1().DeviceClasses().Create(context.TODO(), &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
			Spec: resourcev1.DeviceClassSpec{
				Selectors: []resourcev1.DeviceSelector{{
					CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == "e2e-dra-nonexistent.example.com"`},
				}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.ResourceV1().DeviceClasses().Delete(context.TODO(), className, metav1.DeleteOptions{})
		})

		claimName := "e2e-dra-unalloc-claim"
		_, err = clientset.ResourceV1().ResourceClaims(ns).Create(context.TODO(), &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: claimName},
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{{
						Name:    "gpu",
						Exactly: &resourcev1.ExactDeviceRequest{DeviceClassName: className},
					}},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		cmdTest{
			args:            []string{"deviceclass/" + className, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/dra-deviceclass-unsatisfiable.regex",
		}.assert(t, nil, opts...)

		cmdTest{
			args:            []string{"resourceclaim/" + claimName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/dra-resourceclaim-unallocated.regex",
		}.assert(t, nil, opts...)

		// A direct resourceClaimName referencing a ResourceClaim that was never created --
		// pod_device_claims resolves this via managed_resource_line the same way a Pod naming a
		// nonexistent PVC/ConfigMap does elsewhere in the suite.
		missingClaimName := "e2e-dra-does-not-exist"
		podName := "e2e-dra-missing-claim-pod"
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName},
			Spec: corev1.PodSpec{
				ResourceClaims: []corev1.PodResourceClaim{{
					Name:              "gpu",
					ResourceClaimName: &missingClaimName,
				}},
				Containers: []corev1.Container{{
					Name: "main", Image: "busybox", Command: []string{"sleep", "infinity"},
					Resources: corev1.ResourceRequirements{
						Claims: []corev1.ResourceClaim{{Name: "gpu"}},
					},
				}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods(ns).Delete(context.TODO(), podName, metav1.DeleteOptions{})
		})

		cmdTest{
			args:            []string{"pod/" + podName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/dra-pod-missing-claim.regex",
		}.assert(t, nil, opts...)
	})

	t.Run("ResourceClaim gets a real scheduler allocation against a hand-published ResourceSlice, and Pod and ResourceSlice resolve it", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-dra-alloc"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, nodes.Items, "expected at least one Node")
		nodeName := nodes.Items[0].Name

		driver := "e2e-dra-fake.example.com"
		sliceName := "e2e-dra-slice"
		_, err = clientset.ResourceV1().ResourceSlices().Create(context.TODO(), &resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: sliceName},
			Spec: resourcev1.ResourceSliceSpec{
				Driver:   driver,
				NodeName: &nodeName,
				Pool: resourcev1.ResourcePool{
					// Fixed rather than derived from nodeName: the pool name is the driver's own
					// choice, unrelated to which node happens to run this cluster, and CI's
					// node name differs from a local dev cluster's -- keeping it out of
					// the pool name keeps the fixtures below free of a value that varies by
					// environment (unlike an actual Node/ ref, which nodeNameModifier normalizes).
					Name:               "e2e-dra-pool",
					Generation:         1,
					ResourceSliceCount: 1,
				},
				Devices: []resourcev1.Device{{Name: "dev-0"}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.ResourceV1().ResourceSlices().Delete(context.TODO(), sliceName, metav1.DeleteOptions{})
		})

		className := "e2e-dra-gpu"
		_, err = clientset.ResourceV1().DeviceClasses().Create(context.TODO(), &resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: className},
			Spec: resourcev1.DeviceClassSpec{
				Selectors: []resourcev1.DeviceSelector{{
					CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == "` + driver + `"`},
				}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.ResourceV1().DeviceClasses().Delete(context.TODO(), className, metav1.DeleteOptions{})
		})

		// A directly-named claim (not a ResourceClaimTemplate) so its name is known up front --
		// a template-generated name carries a random suffix that a regex fixture can't pin
		// without a wildcard on the one thing (the claim's own name) every other assertion below
		// keys off.
		claimName := "e2e-dra-claim"
		_, err = clientset.ResourceV1().ResourceClaims(ns).Create(context.TODO(), &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{Name: claimName},
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{{
						Name:    "gpu",
						Exactly: &resourcev1.ExactDeviceRequest{DeviceClassName: className},
					}},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		podName := "e2e-dra-pod"
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName},
			Spec: corev1.PodSpec{
				ResourceClaims: []corev1.PodResourceClaim{{
					Name:              "gpu",
					ResourceClaimName: &claimName,
				}},
				Containers: []corev1.Container{{
					Name: "main", Image: "busybox", Command: []string{"sleep", "infinity"},
					Resources: corev1.ResourceRequirements{
						Claims: []corev1.ResourceClaim{{Name: "gpu"}},
					},
				}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods(ns).Delete(context.TODO(), podName, metav1.DeleteOptions{})
		})

		// No condition=X shorthand exists for a ResourceClaim -- a bare jsonpath (no "=value"
		// suffix) waits for the field to become non-empty, same trick as everywhere else in this
		// suite that has no built-in --for condition to reach for (e.g. the RWOP PVC's
		// jsonpath={.status.phase}=Bound above uses the "=value" form because Bound is a known
		// value; allocation has no such fixed value to wait for).
		waitForInNamespace(t, "resourceclaim/"+claimName, "jsonpath={.status.allocation}", ns)

		cmdTest{
			args:            []string{"resourceclaim/" + claimName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/dra-resourceclaim-allocated.regex",
		}.assert(t, nil, opts...)
		// --deep inlines the resolved DeviceClass -- offline artifact tests can't reach this
		// branch (they run --shallow/--local, which makes the underlying KubeGetFirst a no-op),
		// so this is the only tier that verifies it renders after the full request line rather
		// than splicing into the middle of it (see dra_device_subrequest's $deepBlock).
		cmdTest{
			args:            []string{"resourceclaim/" + claimName, "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/dra-resourceclaim-allocated-deep.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"pod/" + podName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/dra-pod-allocated-claim.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"resourceslice/" + sliceName, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/dra-resourceslice.regex",
		}.assert(t, nodeNameModifier, opts...)
	})
}
