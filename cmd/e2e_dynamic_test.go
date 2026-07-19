package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
		// Crossplane core plus the two Composition Functions it needs (installed cluster-wide by
		// `make install-e2e-deps`) must actually reconcile to produce the XR's composed children,
		// same "controller must actually run" reasoning as the VPA subtest above.
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
}
