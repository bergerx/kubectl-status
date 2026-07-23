package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestE2EDynamicManifests(t *testing.T) {
	e2eMinikubeTest(t)
	hackOpts, clientset, dynamicClient := e2eClients(t)
	t.Run("pod containers section warns when metrics-server's APIService is missing", func(t *testing.T) {
		// Issue #165 case 1: metrics-server was never installed. We simulate that by removing
		// just the APIService object that fronts it (not the Deployment/Service), which is
		// exactly what KubeMetricsUnavailableReason checks -- so the round trip is near-instant
		// and doesn't disturb metrics-server's actual health for other subtests.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		apiServiceYAML, err := exec.Command("kubectl", "get", "apiservice", "v1beta1.metrics.k8s.io", "-o", "yaml").Output()
		require.NoError(t, err)
		require.NoError(t, exec.Command("kubectl", "delete", "apiservice", "v1beta1.metrics.k8s.io").Run())
		t.Cleanup(func() {
			applyCmd := exec.Command("kubectl", "apply", "-f", "-")
			applyCmd.Stdin = bytes.NewReader(apiServiceYAML)
			require.NoError(t, applyCmd.Run())
			waitForMetricsAPIServiceAvailable(t)
		})

		_, err = clientset.CoreV1().Pods("default").Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-pod-metrics-server-missing"},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "main", Image: "busybox", Command: []string{"sleep", "infinity"}}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods("default").Delete(context.TODO(), "e2e-pod-metrics-server-missing", metav1.DeleteOptions{})
		})
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Ready",
			"pod/e2e-pod-metrics-server-missing", "--timeout=2m").Run())

		cmdTest{
			args:            []string{"pod/e2e-pod-metrics-server-missing", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-metrics-server-missing.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("VerticalPodAutoscaler reverse-matches its target workload and shows an applied recommendation", func(t *testing.T) {
		// Deliberately kept out of TestE2EParallel's pool: the burner container below
		// intentionally pegs a full CPU to give the VPA recommender a reason to act, and on a
		// single-node cluster that starves metrics-server's own readiness probe when it runs
		// alongside the other concurrent subtests -- causing unrelated renders elsewhere to
		// intermittently report "metrics-server is not available". Running it serially, alongside
		// the other genuinely cluster-wide-affecting subtest above, avoids that.
		ensureVPA(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-vpa"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		name := "vpa-burner"
		one := int32(1)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Replicas: &one,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:    "burner",
						Image:   "busybox",
						Command: []string{"sh", "-c", "yes > /dev/null"},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("16Mi"),
							},
						},
					}}},
				},
			},
		}
		_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
		require.NoError(t, err)
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Available",
			"deployment/"+name, "-n", ns, "--timeout=4m").Run())
		originalPod := waitForPodByLabel(t, ns, "app="+name)

		vpaGVR := schema.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}
		vpa := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "autoscaling.k8s.io/v1",
			"kind":       "VerticalPodAutoscaler",
			"metadata":   map[string]interface{}{"name": name, "namespace": ns},
			"spec": map[string]interface{}{
				"targetRef": map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment", "name": name},
				"updatePolicy": map[string]interface{}{
					"updateMode":  "Recreate",
					"minReplicas": int64(1),
				},
				"resourcePolicy": map[string]interface{}{
					"containerPolicies": []interface{}{
						map[string]interface{}{
							"containerName": "burner",
							"minAllowed":    map[string]interface{}{"cpu": "10m", "memory": "16Mi"},
							"maxAllowed":    map[string]interface{}{"cpu": "500m", "memory": "128Mi"},
						},
					},
				},
			},
		}}
		_, err = dynamicClient.Resource(vpaGVR).Namespace(ns).Create(context.TODO(), vpa, metav1.CreateOptions{})
		require.NoError(t, err)
		defer dynamicClient.Resource(vpaGVR).Namespace(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})

		waitForVPARecommendation(t, ns, name)
		waitForPodRecreated(t, ns, "app="+name, originalPod)
		// The evicted Pod can briefly still be listed (Terminating) alongside the replacement --
		// wait for exactly one to remain so the fixture below can pin a single Pod line.
		waitForSinglePod(t, ns, "app="+name)
		// waitForPodRecreated/waitForSinglePod only check the replacement Pod's name/count, not
		// its readiness -- under concurrent cluster load its Running/Ready transition can lag
		// well behind that, and the fixture below pins the Deployment as fully Available, so wait
		// for that explicitly rather than racing the kubelet.
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Available",
			"deployment/"+name, "-n", ns, "--timeout=5m").Run())
		waitForVPAPodsMatched(t, ns, name)

		cmdTest{
			args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/vpa-workload-reverse-match.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"vpa/" + name, "-n", ns, "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/vpa-standalone.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("Crossplane XR composes namespaced children and surfaces their health", func(t *testing.T) {
		// Crossplane core plus the two Composition Functions it needs must actually reconcile
		// to produce the XR's composed children, same "controller must actually run" reasoning
		// as the VPA subtest above.
		ensureCrossplane(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-crossplane-xr"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		applyManifest(t, "e2e-artifacts/crossplane-xstatusprobe.yaml")
		require.NoError(t, exec.Command("kubectl", "wait", "--for=condition=Established",
			"xrd/xstatusprobes.tests.kubectl-status.io", "--timeout=60s").Run())
		applyManifestInNamespace(t, "e2e-artifacts/crossplane-xr.yaml", ns)
		waitForInNamespace(t, "xstatusprobe/probe-a", "condition=Synced", ns)
		// The Deployment child is deliberately unschedulable (a nodeSelector no node can match),
		// so the XR itself never reaches Ready -- wait on the field kubectl-status actually reads
		// instead of a condition that will never flip.
		waitForCrossplaneComposedRefs(t, ns, "probe-a", 2)
		// Synced/resourceRefs land as soon as the render step runs, but the XR's own Responsive
		// condition and the composed Deployment's Progressing/Available conditions populate
		// slightly later via separate reconciles -- wait for all of them so the fixtures below
		// pin a stable message instead of racing a transient "Replicas: 0/1" kstatus summary.
		waitForInNamespace(t, "xstatusprobe/probe-a", "condition=Responsive", ns)
		waitForInNamespace(t, "deployment/probe-a-blocked", "condition=Progressing", ns)
		require.NoError(t, exec.Command("kubectl", "wait", "-n", ns,
			"--for=condition=PodScheduled=false", "pod", "-l", "app=probe-a-blocked", "--timeout=2m").Run())
		// kstatus (sigs.k8s.io/cli-utils/pkg/kstatus/status.ScheduleWindow) gives a Pod 15s from
		// its creationTimestamp before reporting Unschedulable as Failed rather than InProgress --
		// wait that out so the fixtures below pin the stable "Failed: Pod could not be scheduled"
		// message instead of racing the transient one.
		waitForPodScheduleWindow(t, ns, "app=probe-a-blocked")

		// Only the live-query-dependent branches belong here: default mode's KubeGetFirst lookup
		// (populating each composed child's compact health) and --deep's IncludeRenderableObject
		// inline. Shallow rendering and Composition.tmpl make no live queries at all -- both are
		// already covered by the offline artifacts (tests/artifacts/crossplane-*).
		cmdTest{
			args:            []string{"xstatusprobe/probe-a", "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/crossplane-xr.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"xstatusprobe/probe-a", "-n", ns, "--deep", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/crossplane-xr-deep.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("PersistentVolumeClaim fetches its StorageClass and surfaces a non-default binding mode and volume expansion", func(t *testing.T) {
		// Issue #669: PersistentVolumeClaim.tmpl previously only printed the storage class name
		// as a string, never fetching the object -- so provisioning-relevant fields like
		// volumeBindingMode (which explains a claim staying Pending until a Pod is scheduled)
		// were invisible. This exercises the live KubeGetFirst fetch, which --shallow/--local
		// (and thus every offline artifact test) makes a no-op.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-storageclass"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

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
	t.Run("PersistentVolumeClaim surfaces its ReadWriteOncePod holder and a scheduling conflict", func(t *testing.T) {
		// Issue #669: a ReadWriteOncePod claim allows only one non-terminal Pod to use it at a
		// time -- enforced by the kube-scheduler's built-in VolumeRestrictions plugin, no CSI
		// driver involved, so this is fully deterministic on minikube's default scheduler.
		// Before this, PersistentVolumeClaim.tmpl gave no indication of which Pod currently
		// holds the claim, nor any explicit signal when a second Pod is stuck behind it.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-rwop"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		// No storageClassName -- picks up the cluster's default class (Immediate binding), so
		// the claim binds to a real PV before any Pod exists.
		pvcName := "e2e-rwop-pvc"
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

		podSpec := func(pvcName string) corev1.PodSpec {
			return corev1.PodSpec{
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
			}
		}

		holderName := "e2e-rwop-holder"
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: holderName},
			Spec:       podSpec(pvcName),
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods(ns).Delete(context.TODO(), holderName, metav1.DeleteOptions{})
		})
		waitForInNamespace(t, "pod/"+holderName, "condition=Ready", ns)

		cmdTest{
			args:            []string{"pvc/" + pvcName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pvc-rwop-holder.regex",
		}.assert(t, nil, opts...)

		// A second Pod referencing the same ReadWriteOncePod claim can't be scheduled while the
		// first is non-terminal -- the scheduler's VolumeRestrictions plugin rejects it and
		// records that in the Pod's own PodScheduled condition, which is what
		// rwop_holder_diagnosis keys off to avoid guessing at the cause of an unrelated pending
		// Pod.
		conflictName := "e2e-rwop-conflict"
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: conflictName},
			Spec:       podSpec(pvcName),
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods(ns).Delete(context.TODO(), conflictName, metav1.DeleteOptions{})
		})
		waitForInNamespace(t, "pod/"+conflictName,
			`jsonpath={.status.conditions[?(@.type=="PodScheduled")].status}=False`, ns)

		cmdTest{
			args:            []string{"pvc/" + pvcName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pvc-rwop-blocked.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("PersistentVolumeClaim renders an explicit conflict when two non-terminal Pods are scheduled against one ReadWriteOncePod claim", func(t *testing.T) {
		// Issue #669: rwop_holder_diagnosis must never pick one Pod arbitrarily when more than
		// one non-terminal Pod is scheduled against the same RWOP claim -- it has to render an
		// explicit conflict instead. The kube-scheduler's VolumeRestrictions plugin normally
		// prevents this from happening for real (see the subtest above), so to exercise the
		// conflict branch deterministically we set spec.nodeName at Pod creation time, which
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
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

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
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

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
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

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
	t.Run("Pod surfaces a bound PV's zone-restricting nodeAffinity when it can't be scheduled", func(t *testing.T) {
		// Issue #738: pod_storage_locality resolves a Pod's PVC-backed volume to its bound PV and
		// surfaces the PV's spec.nodeAffinity when the Pod itself can't be scheduled -- a fact
		// PersistentVolume.tmpl, StorageClass.tmpl, and Pod.tmpl never connected before. There's
		// no live cluster mechanism that reliably produces a real zone-restricted CSI PV on
		// minikube (its hostpath storage-provisioner isn't zone-aware), so -- same "create
		// directly against the API" trick the VolumeAttachment/RWOP-conflict subtests above use
		// -- we hand-craft a PV with a nodeAffinity requirement no real minikube Node label
		// satisfies, and a PVC statically bound to it via spec.volumeName (bypassing dynamic
		// provisioning). The kube-scheduler's VolumeBinding plugin still evaluates a bound PVC's
		// PV nodeAffinity against candidate Nodes for real, so the consuming Pod below stays
		// genuinely Pending -- not a simulated state.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pv-zone"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		pvName := "e2e-pv-zone-restricted"
		_, err = clientset.CoreV1().PersistentVolumes().Create(context.TODO(), &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: pvName},
			Spec: corev1.PersistentVolumeSpec{
				Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: "/tmp/e2e-pv-zone-restricted"},
				},
				NodeAffinity: &corev1.VolumeNodeAffinity{
					Required: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "topology.kubernetes.io/zone",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"e2e-no-such-zone"},
							}},
						}},
					},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().PersistentVolumes().Delete(context.TODO(), pvName, metav1.DeleteOptions{})
		})

		// An explicit "" (not nil) opts this PVC out of the default-StorageClass admission
		// controller, which would otherwise stamp the cluster's default class onto it and make
		// static binding to our classless PV fail on a class mismatch.
		noClass := ""
		pvcName := "e2e-pvc-zone-restricted"
		_, err = clientset.CoreV1().PersistentVolumeClaims(ns).Create(context.TODO(), &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: ns},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
				},
				VolumeName:       pvName,
				StorageClassName: &noClass,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		waitForInNamespace(t, "pvc/"+pvcName, "jsonpath={.status.phase}=Bound", ns)

		podName := "e2e-pod-zone-restricted"
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Labels: map[string]string{"app": podName}},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "main", Image: "busybox", Command: []string{"sleep", "infinity"},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				}},
				Volumes: []corev1.Volume{{
					Name:         "data",
					VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}},
				}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods(ns).Delete(context.TODO(), podName, metav1.DeleteOptions{})
		})
		waitForInNamespace(t, "pod/"+podName,
			`jsonpath={.status.conditions[?(@.type=="PodScheduled")].status}=False`, ns)
		// Fixture pins kstatus's own summary line, which only reports Unschedulable as Failed
		// once the Pod is past its 15s ScheduleWindow -- same wait the Crossplane XR subtest
		// above uses for the same reason.
		waitForPodScheduleWindow(t, ns, "app="+podName)

		cmdTest{
			args:            []string{"pod/" + podName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-pv-zone-restricted.regex",
		}.assert(t, nil, opts...)

		cmdTest{
			args:            []string{"pv/" + pvName, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pv-zone-restricted-standalone.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("Pod hedges on an unbound WaitForFirstConsumer PVC's allowedTopologies", func(t *testing.T) {
		// Issue #738: an unbound claim against a WaitForFirstConsumer class with
		// allowedTopologies has no PV yet to cross-check, so pod_storage_locality must hedge
		// ("isn't zone-pinned yet ... may still constrain") instead of asserting a zone. Reuses
		// the same custom StorageClass pattern as the "PersistentVolumeClaim fetches its
		// StorageClass" subtest above, adding allowedTopologies to it.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pv-zone-wfc"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Namespaces().Delete(context.TODO(), ns, metav1.DeleteOptions{})
		})

		storageClasses, err := clientset.StorageV1().StorageClasses().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, storageClasses.Items, "expected minikube's default storage-provisioner addon to have registered a StorageClass")
		provisioner := storageClasses.Items[0].Provisioner

		scName := "e2e-wfc-topologies"
		bindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		_, err = clientset.StorageV1().StorageClasses().Create(context.TODO(), &storagev1.StorageClass{
			ObjectMeta:        metav1.ObjectMeta{Name: scName},
			Provisioner:       provisioner,
			VolumeBindingMode: &bindingMode,
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

		pvcName := "e2e-wfc-topologies-pvc"
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

		podName := "e2e-pod-wfc-topologies"
		_, err = clientset.CoreV1().Pods(ns).Create(context.TODO(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Labels: map[string]string{"app": podName}},
			Spec: corev1.PodSpec{
				// An unsatisfiable nodeSelector unrelated to storage guarantees this Pod's own
				// PodScheduled=False deterministically, without racing whether minikube's
				// topology-unaware hostpath provisioner would otherwise happily bind the
				// WaitForFirstConsumer claim once the scheduler picks a node for it -- the
				// node-selector filter rejects every Node before scheduling ever reaches volume
				// binding, so the claim also stays reliably unbound.
				NodeSelector: map[string]string{"e2e-no-such-label": "true"},
				Containers: []corev1.Container{{
					Name: "main", Image: "busybox", Command: []string{"sleep", "infinity"},
					VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
				}},
				Volumes: []corev1.Volume{{
					Name:         "data",
					VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}},
				}},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			clientset.CoreV1().Pods(ns).Delete(context.TODO(), podName, metav1.DeleteOptions{})
		})
		waitForInNamespace(t, "pod/"+podName,
			`jsonpath={.status.conditions[?(@.type=="PodScheduled")].status}=False`, ns)
		waitForPodScheduleWindow(t, ns, "app="+podName)

		cmdTest{
			args:            []string{"pod/" + podName, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/pod-wfc-topologies-unbound.regex",
		}.assert(t, nil, opts...)
	})
	t.Run("pod nodeSelector key no NodePool declares surfaces a Karpenter incompatibility, a satisfiable one stays silent", func(t *testing.T) {
		// Kept out of TestE2EParallel's pool, same reasoning as the PV-zone/WFC-topologies
		// subtests above: the fixtures below pin kube-scheduler's exact "0/N nodes are
		// available" message, which only holds for this minikube cluster's real node count --
		// running alongside TestE2EParallel's createBadNode-based subtests would transiently add
		// an extra (fake) Node and change that count out from under this assertion.
		//
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
