package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func runRolloutSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	t.Run("deployment rollout with --include-rollout-diffs shows the diff between revisions", func(t *testing.T) {
		t.Parallel()
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-rollout-diff"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

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
		rolloutCmd := kubectlCmd(t, "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
		output, err := rolloutCmd.CombinedOutput()
		t.Logf("rollout status for %s: %s", name, output)
		require.NoError(t, err)

		// The order in which the two ReplicaSet revisions are diffed (and so which side
		// gets "-" vs "+") isn't guaranteed, so the fixture alternates both directions.
		cmdTest{
			args:                    []string{"deployment/" + name, "-n", ns, "--include-rollout-diffs", "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath:         "e2e-artifacts/rollout-diff.regex",
			retryStdoutRegexFor:     30 * time.Second,
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
			t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
			require.NoError(t, err)
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
			t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
			require.NoError(t, err)
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
		t.Run("statefulset rollback recovery trap (#169)", func(t *testing.T) {
			// Reproduces the kubernetes/kubernetes#67250 trap: an updated Pod that never becomes
			// Ready blocks the rollout even after spec.template is reverted to the known-good
			// image, because the StatefulSet controller keeps waiting on that specific Pod. Only
			// deleting the Pod itself unsticks it. Relies on the defaulted RollingUpdate strategy
			// and OrderedReady Pod management -- the trap needs both.
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-statefulset-rollback-trap"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
			require.NoError(t, err)
			name := "e2e-rollouts-statefulset-rollback-trap"
			one := int32(1)
			goodImage := "nginx:1.27"
			sts := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: appsv1.StatefulSetSpec{
					Replicas:    &one,
					ServiceName: name,
					Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: goodImage}}},
					},
				},
			}
			_, err = clientset.AppsV1().StatefulSets(ns).Create(context.TODO(), sts, metav1.CreateOptions{})
			require.NoError(t, err)
			defer clientset.AppsV1().StatefulSets(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
			waitForPodReadyInNamespace(t, ns, name+"-0")

			// Break the rollout: the replacement Pod for ordinal 0 gets stuck on the bad image.
			live, err := clientset.AppsV1().StatefulSets(ns).Get(context.TODO(), name, metav1.GetOptions{})
			require.NoError(t, err)
			live.Spec.Template.Spec.Containers[0].Image = badImage
			_, err = clientset.AppsV1().StatefulSets(ns).Update(context.TODO(), live, metav1.UpdateOptions{})
			require.NoError(t, err)
			waitForContainerWaitingReasonInNamespace(t, "pod/"+name+"-0", "app", "ImagePullBackOff", ns)
			live, err = clientset.AppsV1().StatefulSets(ns).Get(context.TODO(), name, metav1.GetOptions{})
			require.NoError(t, err)
			stuckRevision := live.Status.UpdateRevision

			// Revert the template to the known-good image WITHOUT deleting the stuck Pod -- the
			// trap: the Pod is still there, still unready, still on the now-orphaned bad revision.
			live.Spec.Template.Spec.Containers[0].Image = goodImage
			_, err = clientset.AppsV1().StatefulSets(ns).Update(context.TODO(), live, metav1.UpdateOptions{})
			require.NoError(t, err)
			waitForStatefulSetUpdateRevisionChange(t, ns, name, stuckRevision)

			trapOpts := combineOpts(opts, []func(*plugin.RenderConfig){
				func(cfg *plugin.RenderConfig) { cfg.StatefulSetRollbackTrapThreshold = 2 * time.Second },
			})
			cmdTest{
				args:            []string{"statefulset/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-statefulset-rollback-trap.regex",
			}.assert(t, nil, trapOpts...)

			// Deleting only the blocking Pod recreates it on the target revision and recovers.
			require.NoError(t, clientset.CoreV1().Pods(ns).Delete(context.TODO(), name+"-0", metav1.DeleteOptions{}))
			waitForPodReadyInNamespace(t, ns, name+"-0")
			cmdTest{
				args:            []string{"statefulset/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
				stdoutRegexPath: "e2e-artifacts/rollouts-statefulset-rollback-trap-recovered.regex",
			}.assert(t, nil, opts...)
		})
		t.Run("daemonset", func(t *testing.T) {
			opts := combineOpts(hackOpts, viperTestHackOpts())
			ns := "e2e-rollouts-blocked-daemonset"
			_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
			t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
			require.NoError(t, err)
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
			t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
			require.NoError(t, err)
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
			t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
			require.NoError(t, err)
			name := "e2e-rollouts-three-revisions"
			applyManifestInNamespace(t, "e2e-artifacts/rollouts-three-revisions.yaml", ns)
			waitForInNamespace(t, "deployment/"+name, "condition=Available", ns)

			out, err := kubectlCmd(t, "set", "image", "deployment/"+name, "nginx=nginx:1.26", "-n", ns).CombinedOutput()
			require.NoError(t, err, string(out))
			rolloutCmd := kubectlCmd(t, "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
			output, err := rolloutCmd.CombinedOutput()
			t.Logf("rollout status for %s (nginx:1.26): %s", name, output)
			require.NoError(t, err)
			waitForSinglePod(t, ns, "app="+name)

			out, err = kubectlCmd(t, "set", "image", "deployment/"+name, "nginx=nginx:1.27", "-n", ns).CombinedOutput()
			require.NoError(t, err, string(out))
			rolloutCmd = kubectlCmd(t, "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=4m")
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
	t.Run("a rollout that can't fit in the namespace ResourceQuota reports the headroom shortfall (#658)", func(t *testing.T) {
		t.Parallel()
		// The quota only has room for 2 of the 3 replicas, so the Deployment is stuck with a
		// ReplicaFailure and the headroom check explains which resource ran out. Needs a live
		// cluster: the quota lives on a separate object that only a KubeGet can reach.
		opts := combineOpts(hackOpts, viperTestHackOpts())
		ns := "e2e-quota-headroom"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		_, err = clientset.CoreV1().ResourceQuotas(ns).Create(context.TODO(), &corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "compute", Namespace: ns},
			Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
				corev1.ResourceRequestsMemory: resource.MustParse("512Mi"),
			}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		name := "e2e-quota-headroom"
		three := int32(3)
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Replicas: &three,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:  "app",
						Image: "registry.k8s.io/pause:3.10",
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						}},
					}}},
				},
			},
		}
		_, err = clientset.AppsV1().Deployments(ns).Create(context.TODO(), dep, metav1.CreateOptions{})
		require.NoError(t, err)
		defer clientset.AppsV1().Deployments(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
		waitForInNamespace(t, "deployment/"+name, "condition=ReplicaFailure", ns)
		// The 2 Pods the quota does allow start out Pending, and the Deployment's own
		// readyReplicas lags behind their readiness, so wait on the field the render actually
		// prints rather than on the Pods.
		waitForInNamespace(t, "deployment/"+name, "jsonpath={.status.readyReplicas}=2", ns)

		cmdTest{
			args:            []string{"deployment/" + name, "-n", ns, "--include-events=false", "--include-managed-fields=false", "--v", "5"},
			stdoutRegexPath: "e2e-artifacts/quota-headroom-blocked-deployment.regex",
		}.assert(t, nil, opts...)
	})
}
