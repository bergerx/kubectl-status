// All exported template methods in this file should have a check if includes are enabled.

package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/cmd/get"
	"k8s.io/kubectl/pkg/polymorphichelpers"
)

func (r RenderableObject) KubeGet(namespace string, args ...string) (out []RenderableObject) {
	if !r.RenderOptions().IncludesEnabled() {
		return
	}
	klog.V(5).InfoS("processing KubeGetFirst", "r", r, "namespace", namespace, "args", args)
	resourceInfos, err := r.engine.getResourceQueryResults(namespace, args).Infos()
	if err != nil {
		klog.V(3).ErrorS(err, "ignoring resource error", "r", r, "namespace", namespace, "args", args)
	}
	return r.getCreationTimestampSortedRenderableObjects(resourceInfos)
}

func (r RenderableObject) getCreationTimestampSortedRenderableObjects(resourceInfos []*resource.Info) []RenderableObject {
	var out []RenderableObject
	runtimeObjList := make([]runtime.Object, len(resourceInfos))
	for i := range resourceInfos {
		runtimeObjList[i] = resourceInfos[i].Object
	}
	_ = get.NewRuntimeSorter(runtimeObjList, ".metadata.creationTimestamp").Sort()
	for _, obj := range runtimeObjList {
		unstructuredObj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		nr := r.newRenderableObject(unstructuredObj)
		klog.V(5).InfoS("KubeGet matched object", "object", nr)
		out = append(out, nr)
	}
	return out
}

// KubeGetFirst returns a new RenderableObject with a nil Object when no object found.
func (r RenderableObject) KubeGetFirst(namespace string, args ...string) RenderableObject {
	nr := r.newRenderableObject(nil)
	if !r.RenderOptions().IncludesEnabled() {
		return nr
	}
	klog.V(5).InfoS("called template method KubeGetFirst",
		"r", r, "namespace", namespace, "args", args)
	resourceInfos, err := r.engine.getResourceQueryResults(namespace, args).Infos()
	if err != nil {
		klog.V(3).ErrorS(err, "getResourceQueryResults failed",
			"r", r, "namespace", namespace, "args", args)
		return nr
	}
	if len(resourceInfos) >= 1 {
		first := resourceInfos[0]
		unstructuredObj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(first.Object)
		nr.Object = unstructuredObj
	} else {
		klog.V(3).InfoS("KubeGetFirst returning empty",
			"r", r, "namespace", namespace, "args", args)
	}
	return nr
}

// KubeGetByLabelsMap returns results similar to this:
//     > kubectl get -n {namespace} {resourceType} -l {labels_key=label_val,...}
func (r RenderableObject) KubeGetByLabelsMap(namespace, resourceType string, labels map[string]interface{}) (out []RenderableObject) {
	if !r.RenderOptions().IncludesEnabled() {
		return
	}
	klog.V(5).InfoS("called template method KubeGetFirst",
		"r", r, "namespace", namespace, "labels", labels)
	var labelPairs []string
	for k, v := range labels {
		labelPairs = append(labelPairs, fmt.Sprintf("%s=%s", k, v))
	}
	selector := strings.Join(labelPairs, ",")
	resourceResult := r.engine.newBuilder().
		NamespaceParam(namespace).
		ResourceTypeOrNameArgs(true, resourceType).
		LabelSelectorParam(selector).
		Do()
	resourceInfos, err := resourceResult.Infos()
	if err != nil {
		klog.V(3).ErrorS(err, "error querying labels",
			"r", r, "namespace", namespace, "labels", labels)
		return
	}
	return r.getCreationTimestampSortedRenderableObjects(resourceInfos)
}

func (r RenderableObject) KubeGetEvents() RenderableObject {
	nr := r.newRenderableObject(nil)
	if !r.RenderOptions().IncludesEnabled() {
		return nr
	}
	klog.V(5).InfoS("called KubeGetEvents", "r", r)
	clientSet, _ := r.engine.kubernetesClientSet()
	events, err := clientSet.CoreV1().Events(r.GetNamespace()).Search(scheme.Scheme, &r)
	if err != nil {
		klog.V(3).ErrorS(err, "error getting events", "r", r)
		return nr
	}
	unstructuredEvents, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&events)
	nr.Object = unstructuredEvents
	return nr
}

// KubeGetResourcesOwnedOf is meant to be called from templates.
// It returns a RenderableObject list for all resources which have provided kind or resource type with the current
// object listed in the ownerReferences.
func (r RenderableObject) KubeGetResourcesOwnedOf(resourceOrKind string) (out []RenderableObject) {
	if !r.RenderOptions().IncludesEnabled() {
		return
	}
	klog.V(5).InfoS("called template method KubeGetFirst", "r", r)
	restMapper, _ := r.engine.mappingFor(resourceOrKind)
	dynamicInterface, _ := r.engine.dynamicInterface()
	controllerRevisions, _ := dynamicInterface.
		Resource(restMapper.Resource).
		Namespace(r.GetNamespace()).
		List(context.TODO(), metav1.ListOptions{})
	for _, controllerRevision := range controllerRevisions.Items {
		if doesOwnerMatch(r.Unstructured, controllerRevision) {
			out = append(out, r.newRenderableObject(controllerRevision.Object))
		}
	}
	return
}

func doesOwnerMatch(owner, owned unstructured.Unstructured) bool {
	for _, ownerReference := range owner.GetOwnerReferences() {
		if ownerReference.UID == owned.GetUID() {
			return true
		}
	}
	return false
}

// KubeGetOwners returns the list of objects which are listed in the Owner references of an object.
func (r RenderableObject) KubeGetOwners() (out []RenderableObject) {
	if !r.RenderOptions().IncludesEnabled() {
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
		} else if gv.Group == "" {
			kindVersionGroup = fmt.Sprintf("%s.%s", owner.Kind, gv.Version)
			klog.V(5).InfoS("KubeGetOwners", "kindVersionGroup", kindVersionGroup)
		} else {
			kindVersionGroup = fmt.Sprintf("%s.%s.%s", owner.Kind, gv.Version, gv.Group)
			klog.V(5).InfoS("KubeGetOwners", "kindVersionGroup", kindVersionGroup)
		}
		out = append(out, r.KubeGetFirst(r.Namespace(), kindVersionGroup, owner.Name))
	}
	return out
}

func (r RenderableObject) KubeGetIngressesMatchingService(namespace, svcName string) (out []RenderableObject) {
	if !r.RenderOptions().IncludesEnabled() {
		return
	}
	klog.V(5).InfoS("called KubeGetIngressesMatchingService",
		"r", r, "namespace", namespace, "svcName", svcName)
	clientSet, _ := r.engine.kubernetesClientSet()
	// The old v1beta1 Ingress which will no longer served as of v1.22. Not implementing it.
	ingresses, err := clientSet.NetworkingV1().Ingresses(namespace).List(context.TODO(), metav1.ListOptions{})
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
		for _, path := range rule.HTTP.Paths {
			if path.Backend.Service.Name == svcName {
				return true
			}
		}
	}
	return false
}

func (r RenderableObject) KubeGetServicesMatchingLabels(namespace string, labels map[string]interface{}) (out []RenderableObject) {
	if !r.RenderOptions().IncludesEnabled() {
		return
	}
	klog.V(5).InfoS("called KubeGetServicesMatchingLabels", "r", r, "namespace", namespace, "labels", labels)
	castedLabels := make(map[string]string, len(labels))
	for k, v := range labels {
		castedLabels[k] = v.(string)
	}
	klog.V(5).InfoS("casted labels values into string", "r", r, "castedLabels", castedLabels)
	clientSet, _ := r.engine.kubernetesClientSet()
	svcs, err := clientSet.CoreV1().Services(r.Namespace()).List(context.TODO(), metav1.ListOptions{})
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

func doesServiceMatchLabels(svc corev1.Service, labels map[string]string) bool {
	if svc.Spec.Type == "ExternalName" {
		return false
	}
	return isSubset(svc.Spec.Selector, labels)
}

// Checks if a is subset of b
func isSubset(a, b map[string]string) bool {
	for k, v := range a {
		if v != b[k] {
			return false
		}
	}
	return true
}

func (r RenderableObject) KubeGetNodeStatsSummary(nodeName string) map[string]interface{} {
	if !r.RenderOptions().IncludesEnabled() {
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
//    > kubectl get --raw /api/v1/nodes/{nodeName}/proxy/stats/summary
// The endpoint that this function uses will be disabled soon: https://github.com/kubernetes/kubernetes/issues/68522
func (r RenderableObject) kubeGetNodeStatsSummary(nodeName string) (map[string]interface{}, error) {
	clientSet, err := r.engine.kubernetesClientSet()
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
	if !r.RenderOptions().IncludesEnabled() {
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
	clientSet, _ := r.engine.kubernetesClientSet()
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
		nr := r.newRenderableObject(unstructuredPod)
		podList = append(podList, nr)
	}
	return podList, nil
}

// KubeGetUnifiedDiffString generates a unified diff between given 2 resources and ignores several keys which are
// known to be creating noise in diff, see the removeFieldsThatCreateDiffNoise function to see which fields are being
// dropped.
func (r RenderableObject) KubeGetUnifiedDiffString(resourceOrKind, namespace, nameA, nameB string) string {
	if !r.RenderOptions().IncludesEnabled() {
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
	controllerRevisionMapping, err := r.engine.mappingFor(resourceOrKind)
	if err != nil {
		klog.V(3).ErrorS(err, "failed to get mapping", "resourceOrKind", resourceOrKind)
		return "", err
	}
	gvr := controllerRevisionMapping.Resource
	dynamicClient, err := r.engine.dynamicInterface()
	if err != nil {
		klog.V(3).ErrorS(err, "failed to get dynamic client")
		return "", err
	}
	aKind, aBytes, aTime, err := getObjectDetailsForDiff(dynamicClient, gvr, namespace, nameA)
	if err != nil {
		return "", err
	}
	bKind, bBytes, bTime, err := getObjectDetailsForDiff(dynamicClient, gvr, namespace, nameB)
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

func getObjectDetailsForDiff(dynamicClient dynamic.Interface, gvr schema.GroupVersionResource, namespace string, name string) (string, []byte, time.Time, error) {
	obj, err := dynamicClient.Resource(gvr).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		klog.V(3).ErrorS(err, "failed to query object")
		return "", nil, time.Time{}, err
	}
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
	unstructured.RemoveNestedField(obj.Object, "spec", "replicas")                                                    // Deployment, Replicaset
	unstructured.RemoveNestedField(obj.Object, "revision")                                                            // ControllerRevision
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "deployment.kubernetes.io/revision")        // Deployment
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "deprecated.daemonset.template.generation") // DaemonSet
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
	// https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#pod-template-hash-label
	unstructured.RemoveNestedField(obj.Object, "metadata", "labels", "pod-template-hash")                     // Deployment
	unstructured.RemoveNestedField(obj.Object, "spec", "selector", "matchLabels", "pod-template-hash")        // Deployment
	unstructured.RemoveNestedField(obj.Object, "spec", "template", "metadata", "labels", "pod-template-hash") // Deployment
	// https://github.com/kubernetes/kubernetes/issues/47554#issuecomment-522924195
	unstructured.RemoveNestedField(obj.Object, "metadata", "labels", "controller.kubernetes.io/hash") // StatefulSet
	unstructured.RemoveNestedField(obj.Object, "metadata", "labels", "controller-revision-hash")      // DaemonSet
	// Well known helm hack to trigger workload rollouts:
	// https://helm.sh/docs/howto/charts_tips_and_tricks/#automatically-roll-deployments
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "checksum/config")
	unstructured.RemoveNestedField(obj.Object, "spec", "template", "metadata", "annotations", "checksum/config")
	unstructured.RemoveNestedField(obj.Object, "metadata", "annotations", "checksum/secret")
	unstructured.RemoveNestedField(obj.Object, "spec", "template", "metadata", "annotations", "checksum/secret")
	unstructured.RemoveNestedField(obj.Object, "status")
}

// TODO: write one test that verifies this
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
