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
	discoveryv1 "k8s.io/api/discovery/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
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

// OwnersResult is the result of resolving an object's ownerReferences: the owner objects that
// could be found, plus any ownerReferences whose owner no longer exists (orphans).
type OwnersResult struct {
	Owners  []RenderableObject
	Orphans []metav1.OwnerReference
}

// KubeGetOwners resolves the Owner references of an object, returning both the owners that
// could be found and any ownerReferences left dangling because their owner no longer exists.
func (r RenderableObject) KubeGetOwners() (out OwnersResult) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("KubeGetOwners called KubeGetOwners", "r", r)
	owners, orphans, err := r.repo.Owners(r.Object)
	if err != nil {
		klog.V(3).ErrorS(err, "error getting owners", "r", r)
	}
	out.Owners = r.objectsToRenderableObjects(owners)
	out.Orphans = orphans
	return out
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

// KubeGetRoutesMatchingService lists Gateway API route resources (HTTPRoute, GRPCRoute, TCPRoute,
// UDPRoute, TLSRoute) whose spec.rules[].backendRefs[] reference the given Service. All 5 route
// kinds share the same rules[].backendRefs[].name shape, so a single implementation covers them.
func (r RenderableObject) KubeGetRoutesMatchingService(namespace, svcName string) (out []RenderableObject) {
	for _, resourceType := range []string{"httproutes", "grpcroutes", "tcproutes", "udproutes", "tlsroutes"} {
		out = append(out, r.kubeGetRoutesMatchingService(namespace, svcName, resourceType)...)
	}
	return
}

func (r RenderableObject) kubeGetRoutesMatchingService(namespace, svcName, resourceType string) (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called kubeGetRoutesMatchingService",
		"r", r, "namespace", namespace, "svcName", svcName, "resourceType", resourceType)
	objects, err := r.repo.Objects(namespace, []string{resourceType}, "")
	if err != nil {
		// Most clusters don't install the experimental route kinds (TCPRoute, UDPRoute, TLSRoute),
		// and some don't have Gateway API at all, so a missing CRD here is expected, not an error.
		klog.V(4).InfoS("failed to list routes, the CRD is likely not installed",
			"r", r, "namespace", namespace, "resourceType", resourceType, "err", err)
		return
	}
	for _, obj := range objects {
		if doesRouteUseService(obj, namespace, svcName) {
			out = append(out, r.newRenderableObject(obj))
		}
	}
	return
}

func doesRouteUseService(obj input.Object, routeNamespace, svcName string) bool {
	rules, found, _ := unstructured.NestedSlice(obj, "spec", "rules")
	if !found {
		return false
	}
	for _, ruleRaw := range rules {
		rule, ok := ruleRaw.(map[string]interface{})
		if !ok {
			continue
		}
		backendRefs, found, _ := unstructured.NestedSlice(rule, "backendRefs")
		if !found {
			continue
		}
		for _, refRaw := range backendRefs {
			ref, ok := refRaw.(map[string]interface{})
			if !ok {
				continue
			}
			if name, _ := ref["name"].(string); name != svcName {
				continue
			}
			if kind, _ := ref["kind"].(string); kind != "" && kind != "Service" {
				continue
			}
			if group, _ := ref["group"].(string); group != "" {
				continue
			}
			if refNamespace, _ := ref["namespace"].(string); refNamespace != "" && refNamespace != routeNamespace {
				continue
			}
			return true
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

// KubeGetPodDisruptionBudgetsMatchingLabels returns all PodDisruptionBudgets in namespace whose
// spec.selector matches the given label set. PDB spec.selector is a full metav1.LabelSelector
// (matchLabels + matchExpressions), unlike Service selectors, so this uses real
// LabelSelectorAsSelector/.Matches semantics rather than the isSubset helper. An empty selector
// (spec.selector: {}) is valid in policy/v1 and matches every pod in the namespace, unlike the
// removed policy/v1beta1 where it matched none -- policy/v1beta1 is no longer served as of
// Kubernetes 1.25, so that legacy case isn't handled here.
func (r RenderableObject) KubeGetPodDisruptionBudgetsMatchingLabels(namespace string, labels_ map[string]interface{}) (out []RenderableObject) {
	out = make([]RenderableObject, 0)
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called KubeGetPodDisruptionBudgetsMatchingLabels", "r", r, "namespace", namespace, "labels", labels_)
	castedLabels := make(map[string]string, len(labels_))
	for k, v := range labels_ {
		castedLabels[k] = fmt.Sprintf("%v", v)
	}
	targetSet := labels.Set(castedLabels)
	pdbs, err := r.repo.Objects(namespace, []string{"poddisruptionbudgets"}, "")
	if err != nil {
		klog.V(3).ErrorS(err, "error listing poddisruptionbudgets", "r", r, "namespace", namespace)
		return
	}
	for _, obj := range pdbs {
		selMap, found, err := unstructured.NestedMap(obj, "spec", "selector")
		if err != nil || !found {
			continue
		}
		ls := &metav1.LabelSelector{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selMap, ls); err != nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(ls)
		if err != nil {
			continue
		}
		if sel.Matches(targetSet) {
			out = append(out, r.newRenderableObject(obj))
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
	endpointSlices, err := r.repo.EndpointSlices(r.Namespace())
	if err != nil {
		klog.V(3).ErrorS(err, "error listing endpointslices", "r", r, "namespace", namespace)
		return
	}
	for _, eps := range endpointSlices.Items {
		matched := false
		for _, endpoint := range eps.Endpoints {
			if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" && endpoint.TargetRef.Name == podName {
				matched = true
				break
			}
		}
		if matched {
			svcName := eps.Labels[discoveryv1.LabelServiceName]
			if svcName == "" {
				continue
			}
			svc, err := r.repo.Service(r.Namespace(), svcName)
			if err != nil {
				klog.V(3).ErrorS(err, "error getting matching service", "r", r, "namespace", namespace, "name", svcName)
				continue
			}
			svc.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))
			svcUnstructured, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&svc)
			out = append(out, r.newRenderableObject(svcUnstructured))
		}
	}
	return out
}

// KubeGetEndpointSlicesForService returns EndpointSlices associated with the given service.
func (r RenderableObject) KubeGetEndpointSlicesForService(namespace, serviceName string) (out []RenderableObject) {
	if viper.GetBool("shallow") {
		return
	}
	klog.V(5).InfoS("called KubeGetEndpointSlicesForService", "r", r, "namespace", namespace, "serviceName", serviceName)
	selector := discoveryv1.LabelServiceName + "=" + serviceName
	objects, err := r.repo.Objects(namespace, []string{"endpointslices"}, selector)
	if err != nil {
		klog.V(3).ErrorS(err, "error listing endpointslices for service", "r", r, "namespace", namespace, "serviceName", serviceName)
		return
	}
	return r.objectsToRenderableObjects(objects)
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

// KubeGetPodMetrics returns the PodMetrics for the named pod. It first tries a single cluster-wide
// PodMetrics list, reused for every pod/node in the render (see AllNamespacesPodMetrics). If that's
// not available (e.g. RBAC only allows namespace-scoped access), it falls back to fetching
// PodMetrics for the whole namespace once per render, so that rendering multiple pods in the same
// namespace still only requires a single metrics.k8s.io request instead of one per pod.
func (r RenderableObject) KubeGetPodMetrics(namespace, name string) RenderableObject {
	if viper.GetBool("shallow") {
		return r.newRenderableObject(nil)
	}
	if allPodMetrics, err := r.repo.AllNamespacesPodMetrics(); err == nil {
		for _, obj := range allPodMetrics {
			u := unstructured.Unstructured{Object: obj}
			if u.GetNamespace() == namespace && u.GetName() == name {
				return r.newRenderableObject(obj)
			}
		}
		return r.newRenderableObject(nil)
	}
	for _, obj := range r.KubeGet(namespace, "PodMetrics") {
		if obj.Name() == name {
			return obj
		}
	}
	return r.newRenderableObject(nil)
}

// KubeGetNodeMetrics returns the NodeMetrics for the named node. It fetches NodeMetrics for the
// whole cluster once per render (via the KubeGet cache) so that rendering multiple nodes only
// requires a single metrics.k8s.io request instead of one per node.
func (r RenderableObject) KubeGetNodeMetrics(name string) RenderableObject {
	for _, obj := range r.KubeGet("", "NodeMetrics") {
		if obj.Name() == name {
			return obj
		}
	}
	return r.newRenderableObject(nil)
}

// KubeMetricsUnavailableReason reports why the metrics.k8s.io API (metrics-server) can't be used
// right now, or "" if it's healthy, so templates can tell users specifically whether it was never
// installed or is installed but unhealthy, instead of silently omitting the usage section either
// way. In shallow mode no cluster call is made (per this file's convention) and "" (available) is
// assumed, so callers don't spuriously warn about something that was never checked.
func (r RenderableObject) KubeMetricsUnavailableReason() string {
	if viper.GetBool("shallow") {
		return ""
	}
	return r.repo.MetricsUnavailableReason()
}

// KubeGetContainerLogs returns up to tailLines lines of log output for the named container in the
// named pod. When previous is true it fetches logs from the container's previous (terminated)
// instance, equivalent to `kubectl logs --previous`. Returns an empty string if there are no logs
// or the fetch fails.
func (r RenderableObject) KubeGetContainerLogs(namespace, podName, containerName string, previous bool, tailLines int) string {
	if viper.GetBool("shallow") {
		return ""
	}
	klog.V(5).InfoS("called KubeGetContainerLogs",
		"namespace", namespace, "podName", podName, "containerName", containerName, "previous", previous)
	logs, err := r.repo.PodContainerLogs(namespace, podName, containerName, previous, int64(tailLines))
	if err != nil {
		klog.V(3).ErrorS(err, "failed to get container logs",
			"namespace", namespace, "podName", podName, "containerName", containerName, "previous", previous)
		return ""
	}
	return strings.TrimRight(logs, "\n")
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
