// All exported template methods in this file should have a check if includes are enabled.

package plugin

import (
	"context"
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
	"k8s.io/apimachinery/pkg/fields"
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
	objects, err := r.engine.repo.Objects(namespace, args, "")
	if err != nil {
		klog.V(3).ErrorS(err, "ignoring resource error", "r", r, "namespace", namespace, "args", args)
	}
	return r.getCreationTimestampSortedRenderableObjects(objects)
}

func (r RenderableObject) getCreationTimestampSortedRenderableObjects(objects input.Objects) []RenderableObject {
	var out []RenderableObject
	for _, obj := range objects {
		unstructuredObj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		nr := r.engine.newRenderableObject(unstructuredObj)
		klog.V(5).InfoS("KubeGet matched object", "object", nr)
		out = append(out, nr)
	}
	return out
}

// KubeGetFirst returns a new RenderableObject with a nil Object when no object found.
func (r RenderableObject) KubeGetFirst(namespace string, args ...string) RenderableObject {
	return r.engine.KubeGetFirst(namespace, args...)
}

// KubeGetFirst returns a new RenderableObject with a nil Object when no object found.
func (e *renderEngine) KubeGetFirst(namespace string, args ...string) RenderableObject {
	nr := e.newRenderableObject(nil)
	if viper.GetBool("shallow") {
		return nr
	}
	klog.V(5).InfoS("called template method KubeGetFirst",
		"namespace", namespace, "args", args)
	objects, err := e.repo.Objects(namespace, args, "")
	if err != nil {
		klog.V(3).ErrorS(err, "getResourceQueryInfos failed",
			"namespace", namespace, "args", args)
		return nr
	}
	if len(objects) >= 1 {
		first := objects[0]
		nr.Object = first
	} else {
		klog.V(3).InfoS("KubeGetFirst returning empty",
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
	unstructuredObjects, err := r.engine.repo.Objects(namespace, []string{resourceType}, selector)
	if err != nil {
		klog.V(3).ErrorS(err, "error querying labels",
			"r", r, "namespace", namespace, "labels", labels)
		return
	}
	return r.getCreationTimestampSortedRenderableObjects(unstructuredObjects)
}

func (r RenderableObject) KubeGetEvents() RenderableObject {
	nr := r.engine.newRenderableObject(nil)
	if viper.GetBool("shallow") {
		return nr
	}
	klog.V(5).InfoS("called KubeGetEvents", "r", r)
	eventList, err := r.engine.repo.ObjectEvents(&r.Unstructured)
	if err != nil {
		klog.V(3).ErrorS(err, "error getting events", "r", r)
		return nr
	}
	unstructuredEvents, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&eventList)
	nr.Object = unstructuredEvents
	return nr
}

// KubeGetResourcesOwnedOf is meant to be called from templates.
// It returns a RenderableObject list for all resources which have provided kind or resource type with the current
// object listed in the ownerReferences.
func (r RenderableObject) KubeGetResourcesOwnedOf(resourceOrKind string) (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called template method KubeGetResourcesOwnedOf", "r", r)
	objects, err := r.engine.repo.Objects(r.GetNamespace(), []string{resourceOrKind}, "")
	if err != nil {
		klog.V(2).InfoS("failed to get objects", "r", r, "resourceOrKind", resourceOrKind)
	}
	for _, object := range objects {
		if doesOwnerMatch(r.Unstructured.Object, object) {
			out = append(out, r.engine.newRenderableObject(object))
		}
	}
	return
}

func doesOwnerMatch(owner, owned input.Object) bool {
	for _, ownerReference := range owner.Unstructured().GetOwnerReferences() {
		if ownerReference.UID == owned.Unstructured().GetUID() {
			return true
		}
	}
	return false
}

// KubeGetOwners returns the list of objects which are listed in the Owner references of an object.
func (r RenderableObject) KubeGetOwners() (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("KubeGetOwners called KubeGetOwners", "r", r)
	owners := r.GetOwnerReferences()
	if len(owners) == 0 {
		klog.V(4).InfoS("KubeGetOwners Object has no owners", "r", r)
		return
	}
	for _, owner := range owners {
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		var kindVersionGroup string
		if err != nil {
			klog.V(3).InfoS("KubeGetOwners failed parsing apiVersion", "apiVersion", owner.APIVersion)
			kindVersionGroup = owner.Kind
			out = append(out, r.KubeGetFirst(r.Namespace(), kindVersionGroup, owner.Name))
		} else if gv.Group == "" && gv.Version != "v1" {
			kindVersionGroup = fmt.Sprintf("%s.%s", owner.Kind, gv.Version)
			klog.V(5).InfoS("KubeGetOwners", "kindVersionGroup", kindVersionGroup, "gv", gv)
			ownerWithVersion := r.KubeGetFirst(r.Namespace(), kindVersionGroup, owner.Name)
			if ownerWithVersion.Object == nil {
				// its likely the ownerReference.apiVersion field doesn't have the group prefix, so we'll try without the version
				ownerWithVersion = r.KubeGetFirst(r.Namespace(), owner.Kind, owner.Name)
			}
			out = append(out, ownerWithVersion)
		} else {
			kindVersionGroup = fmt.Sprintf("%s.%s.%s", owner.Kind, gv.Version, gv.Group)
			klog.V(5).InfoS("KubeGetOwners", "kindVersionGroup", kindVersionGroup)
			out = append(out, r.KubeGetFirst(r.Namespace(), kindVersionGroup, owner.Name))
		}
	}
	return out
}

func (r RenderableObject) KubeGetIngressesMatchingService(namespace, svcName string) (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called KubeGetIngressesMatchingService",
		"r", r, "namespace", namespace, "svcName", svcName)
	ingresses, err := r.engine.repo.Ingresses(namespace)
	if err != nil {
		klog.V(3).ErrorS(err, "error listing ingresses", "r", r, "namespace", namespace)
		return
	}
	for _, ing := range ingresses.Items {
		if doesIngressUseService(ing, svcName) {
			ing.SetGroupVersionKind(netv1.SchemeGroupVersion.WithKind("Ingress"))
			ingUnstructured, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&ing)
			out = append(out, r.engine.newRenderableObject(ingUnstructured))
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
	clientSet, _ := r.engine.repo.KubernetesClientSet()
	svcs, err := clientSet.CoreV1().Services(r.Namespace()).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		klog.V(3).ErrorS(err, "error listing services", "r", r, "namespace", namespace)
		return out
	}
	for _, svc := range svcs.Items {
		if doesServiceMatchLabels(svc, castedLabels) {
			svc.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))
			svcUnstructured, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&svc)
			out = append(out, r.engine.newRenderableObject(svcUnstructured))
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
	clientSet, _ := r.engine.repo.KubernetesClientSet()
	endpoints, err := clientSet.CoreV1().Endpoints(r.Namespace()).List(context.TODO(), metav1.ListOptions{})
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
			svc, err := clientSet.CoreV1().Services(r.Namespace()).Get(context.TODO(), ep.Name, metav1.GetOptions{})
			if err != nil {
				klog.V(3).ErrorS(err, "error getting matching service", "r", r, "namespace", namespace, "name", ep.Name)
				continue
			}
			svc.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))
			svcUnstructured, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&svc)
			out = append(out, r.engine.newRenderableObject(svcUnstructured))
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
	nodeStatsSummary, err := r.kubeGetNodeStatsSummary(nodeName)
	if err != nil {
		klog.V(3).ErrorS(err, "failed to get node stats summary", "r", r, "node", nodeName)
		return nil
	}
	return nodeStatsSummary
}

// kubeGetNodeStatsSummary returns this structure
//
//	> kubectl get --raw /api/v1/nodes/{nodeName}/proxy/stats/summary
//
// The endpoint that this function uses will be disabled soon: https://github.com/kubernetes/kubernetes/issues/68522
func (r RenderableObject) kubeGetNodeStatsSummary(nodeName string) (map[string]interface{}, error) {
	clientSet, err := r.engine.repo.KubernetesClientSet()
	if err != nil {
		return nil, err
	}
	getBytes, err := clientSet.CoreV1().RESTClient().Get().
		Resource("nodes").
		SubResource("proxy").
		Name(nodeName).
		Suffix("stats/summary").
		DoRaw(context.TODO())
	if err != nil {
		return nil, err
	}
	nodeStatsSummary := make(map[string]interface{})
	err = json.Unmarshal(getBytes, &nodeStatsSummary)
	return nodeStatsSummary, err
}

// KubeGetNonTerminatedPodsOnNode returns details of all pods which are not in terminal status
func (r RenderableObject) KubeGetNonTerminatedPodsOnNode(nodeName string) (podList []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called KubeGetNonTerminatedPodsOnNode", "r", r, "node", nodeName)
	podList, err := r.kubeGetNonTerminatedPodsOnTheNode(nodeName)
	if err != nil {
		klog.V(3).ErrorS(err, "kubeGetNonTerminatedPodsOnTheNode failed",
			"r", r, "nodeName", nodeName)
	}
	return
}

func (r RenderableObject) kubeGetNonTerminatedPodsOnTheNode(nodeName string) (podList []RenderableObject, err error) {
	clientSet, _ := r.engine.repo.KubernetesClientSet()
	fieldSelector, err := fields.ParseSelector("spec.nodeName=" + nodeName +
		",status.phase!=" + string(corev1.PodSucceeded) +
		",status.phase!=" + string(corev1.PodFailed))
	if err != nil {
		klog.V(3).ErrorS(err, "Failed creating fieldSelector for non-terminated Pods on Node",
			"r", r, "nodeName", nodeName)
		return
	}
	nodeNonTerminatedPodsList, err := clientSet.CoreV1().
		Pods(""). // Search in all namespaces
		List(context.TODO(), metav1.ListOptions{FieldSelector: fieldSelector.String()})
	if err != nil {
		klog.V(3).ErrorS(err, "Failed getting non-terminated Pods for Node",
			"r", r, "nodeName", nodeName)
		return
	}
	for _, pod := range nodeNonTerminatedPodsList.Items {
		unstructuredPod, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
		nr := r.engine.newRenderableObject(unstructuredPod)
		podList = append(podList, nr)
	}
	return podList, nil
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
	gvr, err := r.engine.repo.GVRFor(resourceOrKind)
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
	object, err := r.engine.repo.DynamicObject(gvr, namespace, name)
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
