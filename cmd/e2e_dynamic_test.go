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

// TestE2EDynamicManifests holds the scenarios that have a specific, stated reason not to be in
// TestE2EParallel's pool. It is the exception, not the default: a new subtest belongs in the pool
// (cmd/main_test.go) unless it trips one of that function's two criteria, and the reason goes in a
// comment on the subtest.
//
// The reasons that actually qualify are narrow, and all of them are about what the subtest does
// *to* its neighbours:
//   - it perturbs cluster-wide state they read (the metrics-server APIService subtest deletes the
//     APIService other renders depend on),
//   - it starves a shared dependency (the VPA subtest pegs a full CPU to give the recommender
//     something to act on, which on a single-node cluster takes metrics-server's readiness probe
//     down with it),
//   - it pins kube-scheduler's exact "0/N nodes are available" message (the PV-zone and
//     Karpenter-nodeSelector subtests below), which would drift if run alongside
//     TestE2EParallel's createBadNode-based subtests changing the node count.
//
// Several things that look disqualifying aren't. Needing a live cluster interaction the offline
// artifacts can't reach ($.KubeGetFirst, $.IncludeRenderableObject/$.Include) doesn't -- most of the
// pool needs one. Installing a real controller and waiting for it to reconcile doesn't either: the
// installers are shared onceInstallers serialized by installMu, so ensureX is safe from inside a
// t.Parallel() subtest, and the Flux scenario (runFluxSubtests, cmd/e2e_flux_test.go) is in the pool
// on exactly that basis. Nor does depending on metrics, as long as the subtest is a consumer of them
// rather than a threat to them. And how the objects come into being certainly doesn't, whatever the
// "DynamicManifests" name suggests: the subtests below build objects in Go, apply static YAML from
// tests/e2e-artifacts, and mutate objects the cluster already has, in no particular pattern.
//
// See #784, where the Flux scenario went from a standalone top-level test to a subtest here before
// anyone checked it against the pool's criteria -- which it met.
//
// See #832, where auditing this function's subtests against the criteria above found six with no
// actual reason to be here -- promoted to runStorageSubtests (cmd/e2e_storage_test.go),
// runCrossplaneSubtests (cmd/e2e_crossplane_test.go), and runHelmReleaseSubtests
// (cmd/e2e_helm_test.go). The PersistentVolumeClaim RWOP-holder subtest and the
// WaitForFirstConsumer-topologies subtest below weren't part of that audit's named list and haven't
// been individually confirmed against the pool's criteria -- don't assume they're exceptions for a
// stated reason the way the ones above are.
//
// The other two entry points exist for reasons this one can't absorb: TestE2EParallel owns the
// concurrent pool and the single e2eClients() setup its subtests share (see #719), and
// TestE2EAgainstVanillaCluster (cmd/e2e_vanilla_test.go) covers CLI error/usage paths that need no
// cluster dependency at all. Don't add a fourth.
func TestE2EDynamicManifests(t *testing.T) {
	e2eClusterTest(t)
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
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

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
							// Cap actual usage via CFS quota so "yes > /dev/null" can't peg a
							// full host core -- comfortably under the VPA's maxAllowed.cpu
							// (500m) below so it doesn't distort the recommendation, and still
							// ~30x the request so the recommender has a clear reason to bump it.
							Limits: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("300m"),
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
	t.Run("PersistentVolumeClaim surfaces its ReadWriteOncePod holder and a scheduling conflict", func(t *testing.T) {
		// Issue #669: a ReadWriteOncePod claim allows only one non-terminal Pod to use it at a
		// time -- enforced by the kube-scheduler's built-in VolumeRestrictions plugin, no CSI
		// driver involved, so this is fully deterministic on the cluster's default scheduler.
		// Before this, PersistentVolumeClaim.tmpl gave no indication of which Pod currently
		// holds the claim, nor any explicit signal when a second Pod is stuck behind it.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-rwop"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

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
	t.Run("Pod surfaces a bound PV's zone-restricting nodeAffinity when it can't be scheduled", func(t *testing.T) {
		// Issue #738: pod_storage_locality resolves a Pod's PVC-backed volume to its bound PV and
		// surfaces the PV's spec.nodeAffinity when the Pod itself can't be scheduled -- a fact
		// PersistentVolume.tmpl, StorageClass.tmpl, and Pod.tmpl never connected before. There's
		// no live cluster mechanism that reliably produces a real zone-restricted CSI PV on
		// the e2e cluster (kind's local-path provisioner isn't zone-aware), so -- same "create
		// directly against the API" trick the VolumeAttachment/RWOP-conflict subtests above use
		// -- we hand-craft a PV with a nodeAffinity requirement no real Node label
		// satisfies, and a PVC statically bound to it via spec.volumeName (bypassing dynamic
		// provisioning). The kube-scheduler's VolumeBinding plugin still evaluates a bound PVC's
		// PV nodeAffinity against candidate Nodes for real, so the consuming Pod below stays
		// genuinely Pending -- not a simulated state.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pv-zone"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

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
		// (runCrossplaneSubtests, cmd/e2e_crossplane_test.go) uses for the same reason.
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
		// StorageClass" subtest (runStorageSubtests, cmd/e2e_storage_test.go), adding
		// allowedTopologies to it.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-pv-zone-wfc"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		storageClasses, err := clientset.StorageV1().StorageClasses().List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, storageClasses.Items, "expected the cluster's default storage provisioner to have registered a StorageClass")
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
				// PodScheduled=False deterministically, without racing whether the cluster's
				// topology-unaware local-path provisioner would otherwise happily bind the
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
		// available" message, which only holds for this cluster's real node count --
		// running alongside TestE2EParallel's createBadNode-based subtests would transiently add
		// an extra (fake) Node and change that count out from under this assertion.
		//
		// No real Karpenter controller runs here (CRDs only, see ensureKarpenterCRDs), so neither
		// Pod below is ever actually provisioned for -- ordinary real-node scheduling failure
		// (no matching Node exists in this cluster either) is what keeps them Pending,
		// which is all that's needed to exercise the render path: it only reads the NodePool's
		// declared spec.requirements, never its status/conditions (never populated without a
		// reconciler) or whether a NodeClaim was actually created.
		ensureKarpenterCRDs(t)
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-karpenter-pod"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

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
