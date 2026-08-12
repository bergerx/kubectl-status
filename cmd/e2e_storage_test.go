package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func runStorageSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset, dynamicClient dynamic.Interface) {
	t.Run("PersistentVolumeClaim fetches its StorageClass and surfaces a non-default binding mode and volume expansion", func(t *testing.T) {
		t.Parallel()
		// Issue #669: PersistentVolumeClaim.tmpl previously only printed the storage class name
		// as a string, never fetching the object -- so provisioning-relevant fields like
		// volumeBindingMode (which explains a claim staying Pending until a Pod is scheduled)
		// were invisible. This exercises the live KubeGetFirst fetch, which --shallow/--local
		// (and thus every offline artifact test) makes a no-op.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-storageclass"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		storageClasses, err := clientset.StorageV1().StorageClasses().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, storageClasses.Items, "expected minikube's default storage-provisioner addon to have registered a StorageClass")
		provisioner := storageClasses.Items[0].Provisioner

		scName := "e2e-wait-for-first-consumer"
		bindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		allowExpansion := true
		_, err = clientset.StorageV1().StorageClasses().Create(context.TODO(), &storagev1.StorageClass{
			ObjectMeta:           metav1.ObjectMeta{Name: scName},
			Provisioner:          provisioner,
			VolumeBindingMode:    &bindingMode,
			AllowVolumeExpansion: &allowExpansion,
			// Issue #738: this is the only e2e path that renders storageclass_summary (used by
			// PersistentVolumeClaim.tmpl below) against a live class, so allowedTopologies is
			// added here rather than to a separate StorageClass -- a dedicated fixture would only
			// exercise the already-covered standalone StorageClass.tmpl render, not this compact
			// partial's new branch.
			AllowedTopologies: []corev1.TopologySelectorTerm{{
				MatchLabelExpressions: []corev1.TopologySelectorLabelRequirement{{
					Key:    "topology.kubernetes.io/zone",
					Values: []string{"e2e-zone-a", "e2e-zone-b"},
				}},
			}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.StorageV1().StorageClasses().Delete(context.TODO(), scName, metav1.DeleteOptions{})
		})

		// WaitForFirstConsumer keeps the claim Pending with no consuming Pod -- exactly the
		// "unbound/late-bound claim" case the issue asks for, and it needs no wait: the claim
		// is Pending as soon as it's created.
		pvcName := "e2e-wfc-pvc"
		_, err = clientset.CoreV1().PersistentVolumeClaims(ns).Create(context.TODO(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &scName,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		cmdTest{
			args:            []string{"pvc/" + pvcName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pvc-storageclass-wait-for-first-consumer.regex",
		}.assert(t, nil, opts...)
		// --deep inlines the fetched StorageClass in full -- offline artifact tests can't reach
		// this branch either, since --shallow/--local (which every offline test uses) makes the
		// KubeGetFirst behind it a no-op, so this is the only tier that verifies it renders.
		cmdTest{
			args:            []string{"pvc/" + pvcName, "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pvc-storageclass-wait-for-first-consumer-deep.regex",
		}.assert(t, nil, opts...)

		// A claim referencing a StorageClass that doesn't exist can't be told apart from one that
		// simply wasn't fetched (--shallow/--local) by any offline artifact -- only a live fetch
		// that comes back empty proves the "not found" warning path.
		missingSCPVCName := "e2e-missing-sc-pvc"
		missingSCName := "e2e-no-such-storageclass"
		_, err = clientset.CoreV1().PersistentVolumeClaims(ns).Create(context.TODO(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: missingSCPVCName, Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: &missingSCName,
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		cmdTest{
			args:            []string{"pvc/" + missingSCPVCName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pvc-storageclass-missing.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("PersistentVolumeClaim renders an explicit conflict when two non-terminal Pods are scheduled against one ReadWriteOncePod claim", func(t *testing.T) {
		t.Parallel()
		// Issue #669: rwop_holder_diagnosis must never pick one Pod arbitrarily when more than
		// one non-terminal Pod is scheduled against the same RWOP claim -- it has to render an
		// explicit conflict instead. The kube-scheduler's VolumeRestrictions plugin normally
		// prevents this from happening for real (see the ReadWriteOncePod holder/conflict
		// subtest in cmd/e2e_dynamic_test.go), so to exercise the conflict branch
		// deterministically we set spec.nodeName at Pod creation time, which
		// skips the scheduler (and its RWOP check) entirely -- same "create it directly against
		// the API" trick the VolumeAttachment subtest below uses to bypass needing a real CSI
		// driver behind it. Pointed at a node name that doesn't exist rather than a real one: no
		// kubelet ever claims the Pod, so phase stays Pending with no containerStatuses forever,
		// instead of racing a real kubelet's admission/scheduling of the render against the
		// test -- which flipped phase and readiness between runs when tried against a real node.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-rwop-conflict"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		const nodeName = "e2e-rwop-conflict-no-such-node"

		pvcName := "e2e-rwop-conflict-pvc"
		_, err = clientset.CoreV1().PersistentVolumeClaims(ns).Create(context.TODO(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		waitForInNamespace(t, "pvc/"+pvcName, "jsonpath={.status.phase}=Bound", ns)

		for _, name := range []string{"e2e-rwop-conflict-a", "e2e-rwop-conflict-b"} {
			_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: corev1.PodSpec{
					NodeName: nodeName,
					Containers: []corev1.Container{{
						Name: "main", Image: "busybox", Command: []string{"sleep", "infinity"},
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName},
						},
					}},
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			podName := name
			t.Cleanup(func() {
				clientset.CoreV1().Pods(ns).Delete(context.TODO(), podName, metav1.DeleteOptions{})
			})
		}

		cmdTest{
			args:            []string{"pvc/" + pvcName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pvc-rwop-conflict.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("PersistentVolume surfaces a VolumeAttachment attach error", func(t *testing.T) {
		t.Parallel()
		// Issue #669: VolumeAttachment has zero references anywhere in the templates today, so a
		// PV/PVC pair can render fully Bound while the actual CSI attach/detach is stuck or
		// erroring -- invisible from both the Pod and PVC/PV views. minikube's own
		// storage-provisioner addon isn't a real CSI driver (hostpath needs no attacher), so it
		// never creates VolumeAttachment objects itself -- there's nothing to wait on
		// deterministically. Instead we create the VolumeAttachment object directly against the
		// API (same trick as the StorageClass subtest above): the apiserver only validates the
		// object's shape, not that a driver is actually behind it, so this is fully
		// deterministic and not flaky.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		// Its own namespace, not the "e2e-storageclass" one the subtest above uses: reusing it
		// raced against that subtest's own namespace deletion still being in flight ("unable to
		// create new content ... because it is being terminated") when both ran in the same
		// process.
		ns := "e2e-volumeattachment"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, nodes.Items)
		nodeName := nodes.Items[0].Name

		// No storageClassName -- picks up the cluster's default class (Immediate binding), so
		// this actually provisions and binds a real PV to attach the fake VolumeAttachment to.
		pvcName := "e2e-attach-error-pvc"
		_, err = clientset.CoreV1().PersistentVolumeClaims(ns).Create(context.TODO(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		waitForInNamespace(t, "pvc/"+pvcName, "jsonpath={.status.phase}=Bound", ns)

		pvc, err := clientset.CoreV1().PersistentVolumeClaims(ns).Get(context.TODO(), pvcName, metav1.GetOptions{})
		require.NoError(t, err)
		pvName := pvc.Spec.VolumeName
		require.NotEmpty(t, pvName)

		vaName := "e2e-fake-attach-error"
		va := &storagev1.VolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{Name: vaName},
			Spec: storagev1.VolumeAttachmentSpec{
				Attacher: "fake.csi.kubectl-status.io",
				NodeName: nodeName,
				Source:   storagev1.VolumeAttachmentSource{PersistentVolumeName: &pvName},
			},
		}
		created, err := clientset.StorageV1().VolumeAttachments().Create(context.TODO(), va, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.StorageV1().VolumeAttachments().Delete(context.TODO(), vaName, metav1.DeleteOptions{})
		})
		created.Status = storagev1.VolumeAttachmentStatus{
			Attached: false,
			AttachError: &storagev1.VolumeError{
				Time:    metav1.Now(),
				Message: "rpc error: code = Internal desc = fake attach failure for e2e test",
			},
		}
		_, err = clientset.StorageV1().VolumeAttachments().UpdateStatus(context.TODO(), created, metav1.UpdateOptions{})
		require.NoError(t, err)

		cmdTest{
			args:            []string{"pv/" + pvName, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pv-volumeattachment-error.regex",
		}.assert(t, nil, opts...)
		// --deep inlines the matching VolumeAttachment in full -- offline artifact tests can't
		// reach this branch either, since --shallow/--local (used by every offline test) makes
		// the KubeGet behind it a no-op.
		cmdTest{
			args:            []string{"pv/" + pvName, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pv-volumeattachment-error-deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("VolumeSnapshot and VolumeSnapshotContent surface readiness, bound linkage, and restore-target context", func(t *testing.T) {
		t.Parallel()
		// Issue #669: VolumeSnapshot/VolumeSnapshotContent (snapshot.storage.k8s.io) had no
		// standalone templates -- `kubectl status volumesnapshot/x` fell through to
		// DefaultResource. minikube's hostpath storage-provisioner has no CSI snapshot support,
		// so getting a real snapshot to reach ReadyToUse deterministically isn't possible here;
		// instead (same trick as the VolumeAttachment subtest above) the objects and their
		// status are created directly against the API -- the apiserver only validates their
		// shape, not that a real external-snapshotter controller is behind them.
		ensureVolumeSnapshotCRDs(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-volumesnapshot"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		// Source PVC the snapshot is (nominally) taken from.
		sourcePVCName := "e2e-vs-source-pvc"
		_, err = clientset.CoreV1().PersistentVolumeClaims(ns).Create(context.TODO(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: sourcePVCName, Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		vsGVR := schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}
		vscGVR := schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotcontents"}
		vsName := "e2e-vs"
		vscName := "e2e-vsc"

		vs := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "snapshot.storage.k8s.io/v1",
			"kind":       "VolumeSnapshot",
			"metadata":   map[string]interface{}{"name": vsName, "namespace": ns},
			"spec": map[string]interface{}{
				"volumeSnapshotClassName": "e2e-snapclass",
				"source": map[string]interface{}{
					"persistentVolumeClaimName": sourcePVCName,
				},
			},
		}}
		createdVS, err := dynamicClient.Resource(vsGVR).Namespace(ns).Create(context.TODO(), vs, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			dynamicClient.Resource(vsGVR).Namespace(ns).Delete(context.TODO(), vsName, metav1.DeleteOptions{})
		})

		vsc := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "snapshot.storage.k8s.io/v1",
			"kind":       "VolumeSnapshotContent",
			"metadata":   map[string]interface{}{"name": vscName},
			"spec": map[string]interface{}{
				"deletionPolicy":          "Delete",
				"driver":                  "fake.csi.kubectl-status.io",
				"volumeSnapshotClassName": "e2e-snapclass",
				"volumeSnapshotRef": map[string]interface{}{
					"name":      vsName,
					"namespace": ns,
					"uid":       string(createdVS.GetUID()),
				},
				"source": map[string]interface{}{
					"volumeHandle": "vol-e2e-fake",
				},
			},
		}}
		createdVSC, err := dynamicClient.Resource(vscGVR).Create(context.TODO(), vsc, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			dynamicClient.Resource(vscGVR).Delete(context.TODO(), vscName, metav1.DeleteOptions{})
		})

		// Bind them to each other and mark both ready -- status is a subresource, set via
		// UpdateStatus rather than at creation time.
		require.NoError(t, unstructured.SetNestedField(createdVS.Object, true, "status", "readyToUse"))
		require.NoError(t, unstructured.SetNestedField(createdVS.Object, vscName, "status", "boundVolumeSnapshotContentName"))
		require.NoError(t, unstructured.SetNestedField(createdVS.Object, "2026-07-21T02:00:00Z", "status", "creationTime"))
		require.NoError(t, unstructured.SetNestedField(createdVS.Object, "5Gi", "status", "restoreSize"))
		_, err = dynamicClient.Resource(vsGVR).Namespace(ns).UpdateStatus(context.TODO(), createdVS, metav1.UpdateOptions{})
		require.NoError(t, err)

		require.NoError(t, unstructured.SetNestedField(createdVSC.Object, true, "status", "readyToUse"))
		require.NoError(t, unstructured.SetNestedField(createdVSC.Object, "snap-e2e-fake-handle", "status", "snapshotHandle"))
		require.NoError(t, unstructured.SetNestedField(createdVSC.Object, int64(1784599200000000000), "status", "creationTime"))
		require.NoError(t, unstructured.SetNestedField(createdVSC.Object, int64(5368709120), "status", "restoreSize"))
		_, err = dynamicClient.Resource(vscGVR).UpdateStatus(context.TODO(), createdVSC, metav1.UpdateOptions{})
		require.NoError(t, err)

		// A second PVC requesting to restore FROM the snapshot -- restore-target context.
		restorePVCName := "e2e-vs-restore-pvc"
		apiGroup := "snapshot.storage.k8s.io"
		_, err = clientset.CoreV1().PersistentVolumeClaims(ns).Create(context.TODO(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: restorePVCName, Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
				},
				DataSourceRef: &corev1.TypedObjectReference{
					APIGroup: &apiGroup,
					Kind:     "VolumeSnapshot",
					Name:     vsName,
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		cmdTest{
			args:            []string{"volumesnapshot/" + vsName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/volumesnapshot-bound.regex",
		}.assert(t, nil, opts...)
		// --deep inlines the bound VolumeSnapshotContent in full -- offline artifact tests can't
		// reach this branch either, since --shallow/--local (used by every offline test) makes
		// the KubeGetFirst behind it a no-op.
		cmdTest{
			args:            []string{"volumesnapshot/" + vsName, "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/volumesnapshot-bound-deep.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"volumesnapshotcontent/" + vscName, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/volumesnapshotcontent-bound.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"volumesnapshotcontent/" + vscName, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/volumesnapshotcontent-bound-deep.regex",
		}.assert(t, nil, opts...)
	})
}
