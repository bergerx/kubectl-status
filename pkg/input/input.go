package input

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/viper"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/cmd/events"
	"k8s.io/kubectl/pkg/cmd/util"
)

// Object is the JSON compatible map[string]interface{} mostly used through unstructured.Unstructured.
type Object map[string]interface{}

func (u Object) creationTimestamp() string {
	m, ok := u["metadata"].(map[string]string)
	if !ok {
		return ""
	}
	return m["creationTimestamp"]
}

func (u Object) Unstructured() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: u}
}

type Objects []Object

func (u Objects) Len() int {
	return len(u)
}

func (u Objects) Less(i, j int) bool {
	return u[i].creationTimestamp() < u[j].creationTimestamp()
}

func (u Objects) Swap(i, j int) {
	u[i], u[j] = u[j], u[i]
}

func NewResourceRepo(factory util.Factory) (*ResourceRepo, error) {
	dynamicClient, err := factory.DynamicClient()
	if err != nil {
		return nil, err
	}
	kubernetesClientSet, err := factory.KubernetesClientSet()
	if err != nil {
		return nil, err
	}
	return &ResourceRepo{
		f:                     factory,
		dynamicClient:         dynamicClient,
		kubernetesClientSet:   kubernetesClientSet,
		nodeStatsSummaryCache: make(map[string]nodeStatsSummaryCacheEntry),
		objectsCache:          make(map[string]objectsCacheEntry),
		endpointSlicesCache:   make(map[string]endpointSlicesCacheEntry),
		ownerCache:            make(map[string]ownerCacheEntry),
	}, nil
}

type ResourceRepo struct {
	f                             util.Factory
	dynamicClient                 dynamic.Interface
	kubernetesClientSet           *kubernetes.Clientset
	nodeStatsSummaryCache         map[string]nodeStatsSummaryCacheEntry
	objectsCache                  map[string]objectsCacheEntry
	endpointSlicesCache           map[string]endpointSlicesCacheEntry
	allNamespacesPodMetricsCache  *objectsCacheEntry
	ownerCache                    map[string]ownerCacheEntry
	metricsUnavailableReasonCache *string
}

type nodeStatsSummaryCacheEntry struct {
	summary Object
	err     error
}

type objectsCacheEntry struct {
	objects Objects
	err     error
}

type endpointSlicesCacheEntry struct {
	list *discoveryv1.EndpointSliceList
	err  error
}

type ownerCacheEntry struct {
	object Object
	err    error
}

func (r *ResourceRepo) newBaseBuilder() *resource.Builder {
	builder := r.f.NewBuilder().
		NamespaceParam(viper.GetString("namespace")).
		DefaultNamespace().
		AllNamespaces(viper.GetBool("all-namepaces")).
		ContinueOnError().
		Unstructured().
		Flatten()
	if viper.GetBool("local") {
		builder = builder.
			FilenameParam(false, &resource.FilenameOptions{
				Filenames: viper.GetStringSlice("filename"),
				Recursive: viper.GetBool("recursive"),
			}).
			Local()
	}
	return builder
}

func (r *ResourceRepo) CLIQueryResults(args []string) *resource.Result {
	builder := r.newBaseBuilder().
		LabelSelectorParam(viper.GetString("selector")).
		FieldSelectorParam(viper.GetString("field-selector"))
	if !viper.GetBool("local") {
		builder = builder.
			FilenameParam(false, &resource.FilenameOptions{
				Filenames: viper.GetStringSlice("filename"),
				Recursive: viper.GetBool("recursive"),
			})
	}

	if !viper.GetBool("local") {
		builder = builder.ResourceTypeOrNameArgs(true, args...)
	}
	return builder.Do()
}

func (r *ResourceRepo) Objects(namespace string, args []string, labelSelector string) (Objects, error) {
	cacheKey := strings.Join([]string{namespace, strings.Join(args, "\x1f"), labelSelector}, "\x1e")
	if entry, ok := r.objectsCache[cacheKey]; ok {
		return entry.objects, entry.err
	}
	unstructuredObjects, err := r.objectsUncached(namespace, args, labelSelector)
	if r.objectsCache == nil {
		r.objectsCache = make(map[string]objectsCacheEntry)
	}
	r.objectsCache[cacheKey] = objectsCacheEntry{objects: unstructuredObjects, err: err}
	return unstructuredObjects, err
}

func (r *ResourceRepo) objectsUncached(namespace string, args []string, labelSelector string) (Objects, error) {
	builder := r.newBaseBuilder().
		NamespaceParam(namespace).
		ResourceTypeOrNameArgs(true, args...).
		LabelSelectorParam(labelSelector)
	infos, err := builder.Do().Infos()
	unstructuredObjects := Objects{}
	for _, info := range infos {
		unstructuredObj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(info.Object) // TODO: handle error
		unstructuredObjects = append(unstructuredObjects, unstructuredObj)
	}
	sort.Sort(unstructuredObjects)
	return unstructuredObjects, err
}

// AllNamespacesPodMetrics attempts a single cluster-wide list of PodMetrics (across every
// namespace), so that a render touching many namespaces/nodes only needs one metrics.k8s.io
// request instead of one per namespace. The attempt, and its outcome, is cached for the whole
// render (including across multiple nodes in the same query) so callers never retry it. If the
// cluster-wide list is denied (e.g. RBAC only grants namespace-scoped access), a warning is
// printed once and callers should fall back to per-namespace fetches, which may in turn be
// incomplete for namespaces the user also can't access.
func (r *ResourceRepo) AllNamespacesPodMetrics() (Objects, error) {
	if r.allNamespacesPodMetricsCache != nil {
		return r.allNamespacesPodMetricsCache.objects, r.allNamespacesPodMetricsCache.err
	}
	builder := r.newBaseBuilder().
		NamespaceParam("").
		AllNamespaces(true).
		ResourceTypeOrNameArgs(true, "PodMetrics")
	infos, err := builder.Do().Infos()
	var objects Objects
	if err != nil {
		klog.Warningf("kubectl-status: could not list pod metrics across all namespaces (%v); "+
			"falling back to per-namespace metrics queries, output may be partial if namespace-scoped access is also restricted", err)
	} else {
		for _, info := range infos {
			unstructuredObj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(info.Object) // TODO: handle error
			objects = append(objects, unstructuredObj)
		}
		sort.Sort(objects)
	}
	r.allNamespacesPodMetricsCache = &objectsCacheEntry{objects: objects, err: err}
	return objects, err
}

// metricsAPIServiceGVR is the apiregistration.k8s.io APIService that fronts metrics-server,
// equivalent to `kubectl get apiservice v1beta1.metrics.k8s.io`.
var metricsAPIServiceGVR = schema.GroupVersionResource{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}

// MetricsUnavailableReason reports why the metrics.k8s.io API (served by metrics-server) can't be
// used, or "" if it's healthy. It checks the APIService's Available condition directly -- the same
// signal `kubectl top` relies on to report "Metrics API not available" -- rather than a
// RESTMapper/discovery-based check, since discovery listings may still show a stale/cached group
// version while the backing service is down. This distinguishes two different problems a caller
// should word differently:
//   - the APIService object doesn't exist at all: metrics-server was never installed.
//   - the APIService exists but its Available condition is False: metrics-server is installed but
//     currently unhealthy (e.g. crashed, unreachable); the condition's own message/reason is
//     surfaced since it usually names the concrete problem (e.g. "no endpoints available").
//
// The result is cached since Pod/Node rendering checks this repeatedly.
func (r *ResourceRepo) MetricsUnavailableReason() string {
	if r.metricsUnavailableReasonCache != nil {
		return *r.metricsUnavailableReasonCache
	}
	reason := ""
	obj, err := r.DynamicObject(metricsAPIServiceGVR, "", "v1beta1.metrics.k8s.io")
	switch {
	case apierrors.IsNotFound(err):
		reason = "metrics-server is not installed"
	case err != nil:
		klog.V(3).ErrorS(err, "failed to check metrics-server APIService availability")
		reason = fmt.Sprintf("failed to check metrics-server availability: %v", err)
	default:
		reason = unavailableReasonFromAPIServiceConditions(obj)
	}
	r.metricsUnavailableReasonCache = &reason
	return reason
}

func unavailableReasonFromAPIServiceConditions(apiService Object) string {
	conditions, _, _ := unstructured.NestedSlice(apiService, "status", "conditions")
	for _, c := range conditions {
		condition, ok := c.(map[string]interface{})
		if !ok || condition["type"] != "Available" {
			continue
		}
		if condition["status"] == "True" {
			return ""
		}
		if detail, _ := condition["message"].(string); detail != "" {
			return fmt.Sprintf("metrics-server is not available: %s", detail)
		}
		return "metrics-server is not available"
	}
	return "metrics-server is not available"
}

// Owners resolves the ownerReferences of obj to the objects they point at. References whose
// owner no longer exists are reported back as orphans, so callers can warn about them, rather
// than being silently dropped. Owners that can't be resolved for other reasons (e.g. missing
// permissions, unknown kind) are silently skipped, since that's not evidence the owner is gone.
func (r *ResourceRepo) Owners(obj Object) (owners Objects, orphans []metav1.OwnerReference, err error) {
	uobj := obj.Unstructured()
	namespace := uobj.GetNamespace()
	for _, owner := range uobj.GetOwnerReferences() {
		object, err := r.resolveOwner(namespace, owner)
		if err != nil {
			if apierrors.IsNotFound(err) {
				orphans = append(orphans, owner)
			} else {
				klog.V(3).InfoS("repo.Owners failed to resolve owner", "apiVersion", owner.APIVersion, "kind", owner.Kind, "name", owner.Name, "err", err)
			}
			continue
		}
		owners = append(owners, object)
	}
	return owners, orphans, nil
}

// resolveOwner fetches the object referenced by an ownerReference, using the dynamic client
// directly so that Kubernetes API errors (e.g. NotFound) reach the caller unwrapped. Cluster-scoped
// owners are looked up without a namespace, since the dynamic client is not scope-aware and would
// otherwise build a namespaced request URL for them and wrongly get back a NotFound.
func (r *ResourceRepo) resolveOwner(namespace string, owner metav1.OwnerReference) (Object, error) {
	mapping, err := r.ownerReferenceMapping(owner)
	if err != nil {
		return nil, err
	}
	if mapping.Scope.Name() != meta.RESTScopeNameNamespace {
		namespace = ""
	}
	cacheKey := strings.Join([]string{namespace, mapping.Resource.String(), owner.Name}, "\x1e")
	if entry, ok := r.ownerCache[cacheKey]; ok {
		return entry.object, entry.err
	}
	object, err := r.DynamicObject(mapping.Resource, namespace, owner.Name)
	if r.ownerCache == nil {
		r.ownerCache = make(map[string]ownerCacheEntry)
	}
	r.ownerCache[cacheKey] = ownerCacheEntry{object: object, err: err}
	return object, err
}

func (r *ResourceRepo) ownerReferenceMapping(owner metav1.OwnerReference) (*meta.RESTMapping, error) {
	gv, err := schema.ParseGroupVersion(owner.APIVersion)
	if err != nil {
		klog.V(3).InfoS("repo.ownerReferenceMapping failed parsing apiVersion", "apiVersion", owner.APIVersion)
		return r.mappingFor(owner.Kind)
	}
	if gv.Group == "" && gv.Version != "v1" {
		kindVersionGroup := fmt.Sprintf("%s.%s", owner.Kind, gv.Version)
		klog.V(5).InfoS("repo.ownerReferenceMapping", "kindVersionGroup", kindVersionGroup, "gv", gv)
		if mapping, err := r.mappingFor(kindVersionGroup); err == nil {
			return mapping, nil
		}
		// it's likely the ownerReference.apiVersion field doesn't have the group prefix, so we'll try without the version
		return r.mappingFor(owner.Kind)
	}
	kindVersionGroup := fmt.Sprintf("%s.%s.%s", owner.Kind, gv.Version, gv.Group)
	klog.V(5).InfoS("repo.ownerReferenceMapping", "kindVersionGroup", kindVersionGroup)
	return r.mappingFor(kindVersionGroup)
}

func (r *ResourceRepo) FirstObject(namespace string, args []string, labelSelector string) (Object, error) {
	objects, err := r.Objects(namespace, args, labelSelector)
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("no objects found in namespace %s for '%s'", namespace, strings.Join(args, " "))
	}
	return objects[0], err
}

func (r *ResourceRepo) ObjectEvents(u *unstructured.Unstructured) (*corev1.EventList, error) {
	eventList, err := r.kubernetesClientSet.CoreV1().Events(u.GetNamespace()).SearchWithContext(context.TODO(), scheme.Scheme, u)
	if err != nil {
		klog.V(3).ErrorS(err, "error getting events", "r", r)
		return nil, err
	}
	sort.Sort(events.SortableEvents(eventList.Items))
	return eventList, nil
}

// PodContainerLogs returns up to tailLines lines of log output for the named container in the
// named pod. When previous is true it fetches logs from the container's previous (terminated)
// instance, equivalent to `kubectl logs --previous`.
func (r *ResourceRepo) PodContainerLogs(namespace, podName, containerName string, previous bool, tailLines int64) (string, error) {
	opts := &corev1.PodLogOptions{
		Container: containerName,
		Previous:  previous,
		TailLines: &tailLines,
	}
	data, err := r.kubernetesClientSet.CoreV1().Pods(namespace).GetLogs(podName, opts).DoRaw(context.TODO())
	if err != nil {
		return "", err
	}
	// Some container runtimes/kubelet versions respond 200 OK with this plain-text message
	// as the body (instead of a proper HTTP error) when the requested container instance's
	// logs are no longer available (e.g. garbage collected). Surface that as an error rather
	// than as log content.
	logs := string(data)
	if strings.HasPrefix(logs, "unable to retrieve container logs for ") {
		return "", fmt.Errorf("%s", strings.TrimSpace(logs))
	}
	return logs, nil
}

func (r *ResourceRepo) DynamicObject(gvr schema.GroupVersionResource, namespace string, name string) (Object, error) {
	u, err := r.dynamicClient.Resource(gvr).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return u.Object, nil
}

func (r *ResourceRepo) GVRFor(resourceOrKindArg string) (schema.GroupVersionResource, error) {
	mapping, err := r.mappingFor(resourceOrKindArg)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	return mapping.Resource, nil
}

// This is a modified copy of resource.Builder's mappingFor method.
func (r *ResourceRepo) mappingFor(resourceOrKindArg string) (*meta.RESTMapping, error) {
	fullySpecifiedGVR, groupResource := schema.ParseResourceArg(resourceOrKindArg)
	gvk := schema.GroupVersionKind{}
	restMapper, err := r.f.ToRESTMapper()
	if err != nil {
		return nil, err
	}

	if fullySpecifiedGVR != nil {
		gvk, _ = restMapper.KindFor(*fullySpecifiedGVR)
	}
	if gvk.Empty() {
		gvk, _ = restMapper.KindFor(groupResource.WithVersion(""))
	}
	if !gvk.Empty() {
		return restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	}

	fullySpecifiedGVK, groupKind := schema.ParseKindArg(resourceOrKindArg)
	if fullySpecifiedGVK == nil {
		gvk := groupKind.WithVersion("")
		fullySpecifiedGVK = &gvk
	}

	if !fullySpecifiedGVK.Empty() {
		if mapping, err := restMapper.RESTMapping(fullySpecifiedGVK.GroupKind(), fullySpecifiedGVK.Version); err == nil {
			return mapping, nil
		}
	}

	mapping, err := restMapper.RESTMapping(groupKind, gvk.Version)
	if err != nil {
		// if we error out here, it is because we could not match a resource or a kind
		// for the given argument. To maintain consistency with previous behavior,
		// announce that a resource type could not be found.
		// if the error is _not_ a *meta.NoKindMatchError, then we had trouble doing discovery,
		// so we should return the original error since it may help a user diagnose what is actually wrong
		if meta.IsNoMatchError(err) {
			return nil, fmt.Errorf("the server doesn't have a resource type %q", groupResource.Resource)
		}
		return nil, err
	}

	return mapping, nil
}

func (r *ResourceRepo) Ingresses(namespace string) (*netv1.IngressList, error) {
	return r.kubernetesClientSet.NetworkingV1().Ingresses(namespace).List(context.TODO(), metav1.ListOptions{})
}

func (r *ResourceRepo) Services(namespace string) (*corev1.ServiceList, error) {
	return r.kubernetesClientSet.CoreV1().Services(namespace).List(context.TODO(), metav1.ListOptions{})
}

func (r *ResourceRepo) Service(namespace, name string) (*corev1.Service, error) {
	return r.kubernetesClientSet.CoreV1().Services(namespace).Get(context.TODO(), name, metav1.GetOptions{})
}

func (r *ResourceRepo) EndpointSlices(namespace string) (*discoveryv1.EndpointSliceList, error) {
	if entry, ok := r.endpointSlicesCache[namespace]; ok {
		return entry.list, entry.err
	}
	list, err := r.kubernetesClientSet.DiscoveryV1().EndpointSlices(namespace).List(context.TODO(), metav1.ListOptions{})
	if r.endpointSlicesCache == nil {
		r.endpointSlicesCache = make(map[string]endpointSlicesCacheEntry)
	}
	r.endpointSlicesCache[namespace] = endpointSlicesCacheEntry{list: list, err: err}
	return list, err
}

// KubeGetNodeStatsSummary returns this structure
// > kubectl get --raw /api/v1/nodes/{nodeName}/proxy/stats/summary
// The endpoint that this function uses will be disabled soon: https://github.com/kubernetes/kubernetes/issues/68522
func (r *ResourceRepo) KubeGetNodeStatsSummary(nodeName string) (Object, error) {
	if entry, ok := r.nodeStatsSummaryCache[nodeName]; ok {
		return entry.summary, entry.err
	}
	nodeStatsSummary, err := r.kubeGetNodeStatsSummaryUncached(nodeName)
	if r.nodeStatsSummaryCache == nil {
		r.nodeStatsSummaryCache = make(map[string]nodeStatsSummaryCacheEntry)
	}
	r.nodeStatsSummaryCache[nodeName] = nodeStatsSummaryCacheEntry{summary: nodeStatsSummary, err: err}
	return nodeStatsSummary, err
}

func (r *ResourceRepo) kubeGetNodeStatsSummaryUncached(nodeName string) (Object, error) {
	getBytes, err := r.kubernetesClientSet.CoreV1().RESTClient().Get().
		Resource("nodes").
		SubResource("proxy").
		Name(nodeName).
		Suffix("stats/summary").
		DoRaw(context.TODO())
	if err != nil {
		return nil, err
	}
	nodeStatsSummary := make(Object)
	err = json.Unmarshal(getBytes, &nodeStatsSummary)
	return nodeStatsSummary, err
}

func (r *ResourceRepo) NonTerminatedPodsOnTheNode(nodeName string) (Objects, error) {
	fieldSelector, err := fields.ParseSelector("spec.nodeName=" + nodeName +
		",status.phase!=" + string(corev1.PodSucceeded) +
		",status.phase!=" + string(corev1.PodFailed))
	if err != nil {
		klog.V(3).ErrorS(err, "Failed creating fieldSelector for non-terminated Pods on Node",
			"r", r, "nodeName", nodeName)
		return nil, err
	}
	nodeNonTerminatedPodsList, err := r.kubernetesClientSet.CoreV1().
		Pods(""). // Search in all namespaces
		List(context.TODO(), metav1.ListOptions{FieldSelector: fieldSelector.String()})
	if err != nil {
		klog.V(3).ErrorS(err, "Failed getting non-terminated Pods for Node",
			"r", r, "nodeName", nodeName)
		return nil, err
	}
	pods := Objects{}
	for _, pod := range nodeNonTerminatedPodsList.Items {
		unstructuredPod, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
		pods = append(pods, unstructuredPod)
	}
	return pods, nil
}
