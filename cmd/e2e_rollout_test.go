package main

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
}
