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
	}, nil
}

type ResourceRepo struct {
	f                            util.Factory
	dynamicClient                dynamic.Interface
	kubernetesClientSet          *kubernetes.Clientset
	nodeStatsSummaryCache        map[string]nodeStatsSummaryCacheEntry
	objectsCache                 map[string]objectsCacheEntry
	endpointSlicesCache          map[string]endpointSlicesCacheEntry
	allNamespacesPodMetricsCache *objectsCacheEntry
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

func (r *ResourceRepo) Owners(obj Object) (out Objects, err error) {
	uobj := obj.Unstructured()
	namespace := uobj.GetNamespace()
	owners := uobj.GetOwnerReferences()
	if len(owners) == 0 {
		klog.V(4).InfoS("KubeGetOwners Object has no owners", "r", r)
		return nil, fmt.Errorf("Object has no owners: %s", obj)
	}
	for _, owner := range owners {
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		var kindVersionGroup string
		if err != nil {
			klog.V(3).InfoS("repo.Owners failed parsing apiVersion", "apiVersion", owner.APIVersion)
			kindVersionGroup = owner.Kind
			object, err := r.FirstObject(namespace, []string{kindVersionGroup}, owner.Name)
			if err != nil {
				klog.V(3).InfoS("repo.Owners failed to get owner using Kind", "apiVersion", owner.APIVersion)
				continue
			}
			out = append(out, object)
			continue
		}
		if gv.Group == "" && gv.Version != "v1" {
			kindVersionGroup = fmt.Sprintf("%s.%s", owner.Kind, gv.Version)
			klog.V(5).InfoS("repo.Owners", "kindVersionGroup", kindVersionGroup, "gv", gv)
			ownerWithVersion, err := r.FirstObject(namespace, []string{kindVersionGroup, owner.Name}, "")
			if err != nil {
				klog.V(3).InfoS("repo.Owners failed to get owner using kind+version", "apiVersion", owner.APIVersion)
				continue
			}
			if ownerWithVersion == nil {
				// it's likely the ownerReference.apiVersion field doesn't have the group prefix, so we'll try without the version
				ownerWithVersion, err = r.FirstObject(namespace, []string{owner.Kind, owner.Name}, "")
				if err != nil {
					klog.V(3).InfoS("repo.Owners failed to get owner using kind+version", "apiVersion", owner.APIVersion)
					continue
				}
			}
			out = append(out, ownerWithVersion)
			continue
		}
		kindVersionGroup = fmt.Sprintf("%s.%s.%s", owner.Kind, gv.Version, gv.Group)
		klog.V(5).InfoS("repo.Owners", "kindVersionGroup", kindVersionGroup)
		object, err := r.FirstObject(namespace, []string{kindVersionGroup, owner.Name}, "")
		if err != nil {
			klog.V(3).InfoS("repo.Owners failed to get owner using kind+version+group", "apiVersion", owner.APIVersion)
			continue
		}
		out = append(out, object)
	}
	return out, nil
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
