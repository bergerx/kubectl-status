// All exported template methods in this file should have a check if includes are enabled.

package plugin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/viper"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/polymorphichelpers"

	"github.com/bergerx/kubectl-status/pkg/input"
)

func (r RenderableObject) KubeGet(namespace string, args ...string) (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("processing KubeGet", "r", r, "namespace", namespace, "args", args)
	objects, err := r.repo.Objects(namespace, args, "")
	if err != nil {
		klog.V(3).ErrorS(err, "ignoring resource error", "r", r, "namespace", namespace, "args", args)
	}
	return r.objectsToRenderableObjects(objects)
}

func (r RenderableObject) objectsToRenderableObjects(objects input.Objects) (out []RenderableObject) {
	for _, obj := range objects {
		nr := r.newRenderableObject(obj)
		out = append(out, nr)
	}
	return out
}

// KubeGetFirst returns a new RenderableObject with a nil Object when no object found.
func (r RenderableObject) KubeGetFirst(namespace string, args ...string) RenderableObject {
	nr := r.newRenderableObject(nil)
	if viper.GetBool("shallow") {
		return nr
	}
	klog.V(5).InfoS("called template method KubeGetFirst",
		"namespace", namespace, "args", args)
	var err error
	nr.Object, err = r.repo.FirstObject(namespace, args, "")
	if err != nil {
		klog.V(3).ErrorS(err, "KubeGetFirst failed",
			"namespace", namespace, "args", args)
	}
	return nr
}

// KubeGetByLabelsMap returns results similar to this:
//
//	> kubectl get -n {namespace} {resourceType} -l {label_key=label_val,...}
func (r RenderableObject) KubeGetByLabelsMap(namespace, resourceType string, labels map[string]interface{}) (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called template method KubeGetByLabelsMap",
		"r", r, "namespace", namespace, "labels", labels)
	var labelPairs []string
	for k, v := range labels {
		labelPairs = append(labelPairs, fmt.Sprintf("%s=%s", k, v))
	}
	selector := strings.Join(labelPairs, ",")
	unstructuredObjects, err := r.repo.Objects(namespace, []string{resourceType}, selector)
	if err != nil {
		klog.V(3).ErrorS(err, "error querying labels",
			"r", r, "namespace", namespace, "labels", labels)
		return
	}
	return r.objectsToRenderableObjects(unstructuredObjects)
}

func (r RenderableObject) KubeGetEvents() RenderableObject {
	nr := r.newRenderableObject(nil)
	if viper.GetBool("shallow") {
		return nr
	}
	klog.V(5).InfoS("called KubeGetEvents", "r", r)
	eventList, err := r.repo.ObjectEvents(&r.Unstructured)
	if err != nil {
		klog.V(3).ErrorS(err, "error getting events", "r", r)
		return nr
	}
	unstructuredEvents, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&eventList)
	nr.Object = unstructuredEvents // TODO: convert input.Events to return Objects
	return nr
}

// KubeGetOwners returns the list of objects which are listed in the Owner references of an object.
func (r RenderableObject) KubeGetOwners() (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("KubeGetOwners called KubeGetOwners", "r", r)
	owners, err := r.repo.Owners(r.Object)
	if err != nil {
		klog.V(3).ErrorS(err, "error getting owners", "r", r)
	}
	return r.objectsToRenderableObjects(owners)
}

func (r RenderableObject) KubeGetIngressesMatchingService(namespace, svcName string) (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called KubeGetIngressesMatchingService",
		"r", r, "namespace", namespace, "svcName", svcName)
	ingresses, err := r.repo.Ingresses(namespace)
	if err != nil {
		klog.V(3).ErrorS(err, "error listing ingresses", "r", r, "namespace", namespace)
		return
	}
	for _, ing := range ingresses.Items {
		if doesIngressUseService(ing, svcName) {
			ing.SetGroupVersionKind(netv1.SchemeGroupVersion.WithKind("Ingress"))
			ingUnstructured, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&ing)
			out = append(out, r.newRenderableObject(ingUnstructured))
		}
	}
	return
}

func doesIngressUseService(ing netv1.Ingress, svcName string) bool {
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			if path.Backend.Service.Name == svcName {
				return true
			}
		}
	}
	return false
}

func (r RenderableObject) KubeGetServicesMatchingLabels(namespace string, labels map[string]interface{}) (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called KubeGetServicesMatchingLabels", "r", r, "namespace", namespace, "labels", labels)
	castedLabels := make(map[string]string, len(labels))
	for k, v := range labels {
		castedLabels[k] = v.(string)
	}
	klog.V(5).InfoS("casted labels values into string", "r", r, "castedLabels", castedLabels)
	svcs, err := r.repo.Services(r.Namespace())
	if err != nil {
		klog.V(3).ErrorS(err, "error listing services", "r", r, "namespace", namespace)
		return out
	}
	for _, svc := range svcs.Items {
		if doesServiceMatchLabels(svc, castedLabels) {
			svc.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))
			svcUnstructured, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&svc)
			out = append(out, r.newRenderableObject(svcUnstructured))
		}
	}
	return out
}

func (r RenderableObject) KubeGetServicesMatchingPod(namespace, podName string) (out []RenderableObject) {
	out = make([]RenderableObject, 0)
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called KubeGetServicesMatchingPod", "r", r, "namespace", namespace, "podName", podName)
	endpoints, err := r.repo.Endpoints(r.Namespace())
	if err != nil {
		klog.V(3).ErrorS(err, "error listing endpoints", "r", r, "namespace", namespace)
		return
	}
	for _, ep := range endpoints.Items {
		matched := false
		for _, subset := range ep.Subsets {
			for _, address := range subset.Addresses {
				if address.TargetRef != nil && address.TargetRef.Kind == "Pod" && address.TargetRef.Name == podName {
					matched = true
				}
			}
		}
		if matched {
			svc, err := r.repo.Service(r.Namespace(), ep.Name)
			if err != nil {
				klog.V(3).ErrorS(err, "error getting matching service", "r", r, "namespace", namespace, "name", ep.Name)
				continue
			}
			svc.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))
			svcUnstructured, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&svc)
			out = append(out, r.newRenderableObject(svcUnstructured))
		}
	}
	return out
}

func doesServiceMatchLabels(svc corev1.Service, labels map[string]string) bool {
	if svc.Spec.Type == "ExternalName" {
		return false
	}
	return isSubset(svc.Spec.Selector, labels)
}

// Checks if "a" is subset of "b", returns false if "a" is empty.
func isSubset(a, b map[string]string) bool {
	if len(a) == 0 {
		return false
	}
	for k, v := range a {
		if v != b[k] {
			return false
		}
	}
	return true
}

func (r RenderableObject) KubeGetNodeStatsSummary(nodeName string) map[string]interface{} {
	if viper.GetBool("shallow") {
		return nil
	}
	klog.V(5).InfoS("called KubeGetNodeStatsSummary", "r", r, "node", nodeName)
	nodeStatsSummary, err := r.repo.KubeGetNodeStatsSummary(nodeName)
	if err != nil {
		klog.V(3).ErrorS(err, "failed to get node stats summary", "r", r, "node", nodeName)
		return nil
	}
	return nodeStatsSummary
}

// KubeGetNonTerminatedPodsOnNode returns details of all pods which are not in terminal status
func (r RenderableObject) KubeGetNonTerminatedPodsOnNode(nodeName string) (podList []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called KubeGetNonTerminatedPodsOnNode", "r", r, "node", nodeName)
	pods, err := r.repo.NonTerminatedPodsOnTheNode(nodeName)
	if err != nil {
		klog.V(3).ErrorS(err, "Failed getting non-terminated Pods for Node",
			"r", r, "nodeName", nodeName)
		return
	}
	return r.objectsToRenderableObjects(pods)
}

// KubeGetUnifiedDiffString generates a unified diff between given 2 resources and ignores several keys which are
// known to be creating noise in diff, see the removeFieldsThatCreateDiffNoise function to see which fields are being
// dropped.
func (r RenderableObject) KubeGetUnifiedDiffString(resourceOrKind, namespace, nameA, nameB string) string {
	if viper.GetBool("shallow") {
		return ""
	}
	klog.V(5).InfoS("called KubeGetUnifiedDiffString",
		"r", r, "resourceOrKind", resourceOrKind, "namespace", namespace, "nameA", nameA, "nameB", nameB)
	diff, err := r.kubeGetUnifiedDiffString(resourceOrKind, namespace, nameA, nameB)
	if err != nil {
		klog.V(3).ErrorS(err, "kubeGetUnifiedDiffString errored",
			"r", r, "resourceOrKind", resourceOrKind, "namespace", namespace, "nameA", nameA, "nameB", nameB)
	}
	return diff
}

func (r RenderableObject) kubeGetUnifiedDiffString(resourceOrKind, namespace, nameA, nameB string) (string, error) {
	gvr, err := r.repo.GVRFor(resourceOrKind)
	if err != nil {
		klog.V(3).ErrorS(err, "failed to get mapping", "resourceOrKind", resourceOrKind)
		return "", err
	}
	aKind, aBytes, aTime, err := r.getObjectDetailsForDiff(gvr, namespace, nameA)
	if err != nil {
		return "", err
	}
	bKind, bBytes, bTime, err := r.getObjectDetailsForDiff(gvr, namespace, nameB)
	if err != nil {
		return "", err
	}
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(string(aBytes)),
		B:        difflib.SplitLines(string(bBytes)),
		FromFile: fmt.Sprintf("a %s/%s", aKind, nameA),
		ToFile:   fmt.Sprintf("b %s/%s", bKind, nameB),
		FromDate: fmt.Sprintf("%s (%s ago)", aTime.String(), ago(aTime)),
		ToDate:   fmt.Sprintf("%s (%s ago)", bTime.String(), ago(bTime)),
		Context:  3,
	}
	return difflib.GetUnifiedDiffString(diff)
}

func (r RenderableObject) getObjectDetailsForDiff(gvr schema.GroupVersionResource, namespace string, name string) (string, []byte, time.Time, error) {
	object, err := r.repo.DynamicObject(gvr, namespace, name)
	if err != nil {
		klog.V(2).ErrorS(err, "failed to query object", "gvr", gvr, "namespace", namespace, "name", name)
		return "", nil, time.Time{}, err
	}
	obj := object.Unstructured()
	creationTime := obj.GetCreationTimestamp().Time
	removeFieldsThatCreateDiffNoise(obj)
	objBytes, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		klog.V(3).ErrorS(err, "failed to parse object")
		return "", nil, time.Time{}, err
	}
	return obj.GetKind(), objBytes, creationTime, nil
}

func removeFieldsThatCreateDiffNoise(obj *unstructured.Unstructured) {
	obj.SetName("")
	obj.SetManagedFields(nil)
	obj.SetResourceVersion("")
	obj.SetUID("")
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetGeneration(0)
	unstructured.RemoveNestedField(obj.Object, "spec", "replicas")                                                     // Deployment, Replicaset
	unstructured.RemoveNestedField(obj.Object, "revision")                                                             // ControllerRevision
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "deployment.kubernetes.io/revision")         // Deployment
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "deployment.kubernetes.io/revision-history") // ReplicaSet
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "deprecated.daemonset.template.generation")  // DaemonSet
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "kapp.k14s.io/original")
	unstructured.RemoveNestedField(obj.Object, "metadata", "selfLink")
	// https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#pod-template-hash-label
	unstructured.RemoveNestedField(obj.Object, "metadata", "labels", "pod-template-hash")                     // Deployment
	unstructured.RemoveNestedField(obj.Object, "spec", "selector", "matchLabels", "pod-template-hash")        // Deployment
	unstructured.RemoveNestedField(obj.Object, "spec", "template", "metadata", "labels", "pod-template-hash") // Deployment
	// https://github.com/kubernetes/kubernetes/issues/47554#issuecomment-522924195
	unstructured.RemoveNestedField(obj.Object, "metadata", "labels", "controller.kubernetes.io/hash") // StatefulSet
	unstructured.RemoveNestedField(obj.Object, "metadata", "labels", "controller-revision-hash")      // DaemonSet

	unstructured.RemoveNestedField(obj.Object, "status")
}

// RolloutStatus returns a map[string]interface{} with 3 keys: done: bool, message, error: string
func (r RenderableObject) RolloutStatus(obj RenderableObject) map[string]interface{} {
	klog.V(5).InfoS("called RolloutStatus", "r", r, "obj", obj)
	groupKind := obj.GetObjectKind().GroupVersionKind().GroupKind()
	statusViewer, err := polymorphichelpers.StatusViewerFor(groupKind)
	if err != nil {
		klog.V(3).ErrorS(err, "cant get RolloutStatus for kind", "r", r, "obj", obj, "groupKind", groupKind)
		return nil
	}
	lastRevision := int64(0)
	message, done, err := statusViewer.Status(&obj, lastRevision)
	errString := ""
	if err != nil {
		klog.V(3).ErrorS(err, "cant get RolloutStatus ", "r", r, "obj", obj)
		errString = err.Error()
	}
	return map[string]interface{}{
		"done":    done,
		"message": strings.TrimSpace(message),
		"error":   strings.TrimSpace(errString),
	}
}
