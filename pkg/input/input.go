package input

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/viper"
	corev1 "k8s.io/api/core/v1"
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

func NewResourceRepo(factory util.Factory) *ResourceRepo {
	return &ResourceRepo{
		f: factory,
	}
}

type ResourceRepo struct {
	f util.Factory

	client    dynamic.Interface
	clientSet *kubernetes.Clientset
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
	clientSet, err := r.kubernetesClientSet()
	if err != nil {
		klog.V(3).ErrorS(err, "error getting events", "r", r)
		return nil, err
	}
	eventList, err := clientSet.CoreV1().Events(u.GetNamespace()).Search(scheme.Scheme, u)
	if err != nil {
		klog.V(3).ErrorS(err, "error getting events", "r", r)
		return nil, err
	}
	sort.Sort(events.SortableEvents(eventList.Items))
	return eventList, nil
}

func (r *ResourceRepo) kubernetesClientSet() (*kubernetes.Clientset, error) {
	if r.clientSet == nil {
		var err error
		r.clientSet, err = r.f.KubernetesClientSet()
		if err != nil {
			return nil, err
		}
	}
	return r.clientSet, nil
}

func (r *ResourceRepo) dynamicClient() (dynamic.Interface, error) {
	if r.client == nil {
		var err error
		r.client, err = r.f.DynamicClient()
		if err != nil {
			return nil, err
		}
	}
	return r.client, nil
}

func (r *ResourceRepo) DynamicObject(gvr schema.GroupVersionResource, namespace string, name string) (Object, error) {
	dynamicClient, err := r.dynamicClient()
	if err != nil {
		return nil, err
	}
	u, err := dynamicClient.Resource(gvr).Namespace(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(u.Object)
	if err != nil {
		return nil, err
	}
	return object, nil
}

func (r *ResourceRepo) DynamicObjects(gvr schema.GroupVersionResource, namespace string) (Objects, error) {
	dynamicClient, err := r.dynamicClient()
	if err != nil {
		return nil, err
	}
	unstructuredList, err := dynamicClient.Resource(gvr).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var objects Objects
	for _, unstructuredObj := range unstructuredList.Items {
		unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(unstructuredObj.Object)
		if err != nil {
			return nil, err
		}
		objects = append(objects, unstructuredObj)
	}
	return objects, nil
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
	clientSet, err := r.kubernetesClientSet()
	if err != nil {
		return nil, err
	}
	return clientSet.NetworkingV1().Ingresses(namespace).List(context.TODO(), metav1.ListOptions{})
}

func (r *ResourceRepo) Services(namespace string) (*corev1.ServiceList, error) {
	clientSet, err := r.kubernetesClientSet()
	if err != nil {
		return nil, err
	}
	return clientSet.CoreV1().Services(namespace).List(context.TODO(), metav1.ListOptions{})
}

func (r *ResourceRepo) Service(namespace, name string) (*corev1.Service, error) {
	clientSet, err := r.kubernetesClientSet()
	if err != nil {
		return nil, err
	}
	return clientSet.CoreV1().Services(namespace).Get(context.TODO(), name, metav1.GetOptions{})
}

func (r *ResourceRepo) Endpoints(namespace string) (*corev1.EndpointsList, error) {
	clientSet, err := r.kubernetesClientSet()
	if err != nil {
		return nil, err
	}
	return clientSet.CoreV1().Endpoints(namespace).List(context.TODO(), metav1.ListOptions{})
}

// KubeGetNodeStatsSummary returns this structure
// > kubectl get --raw /api/v1/nodes/{nodeName}/proxy/stats/summary
// The endpoint that this function uses will be disabled soon: https://github.com/kubernetes/kubernetes/issues/68522
func (r *ResourceRepo) KubeGetNodeStatsSummary(nodeName string) (Object, error) {
	clientSet, err := r.kubernetesClientSet()
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
	nodeStatsSummary := make(Object)
	err = json.Unmarshal(getBytes, &nodeStatsSummary)
	return nodeStatsSummary, err
}

func (r *ResourceRepo) NonTerminatedPodsOnTheNode(nodeName string) (Objects, error) {
	clientSet, _ := r.kubernetesClientSet()
	fieldSelector, err := fields.ParseSelector("spec.nodeName=" + nodeName +
		",status.phase!=" + string(corev1.PodSucceeded) +
		",status.phase!=" + string(corev1.PodFailed))
	if err != nil {
		klog.V(3).ErrorS(err, "Failed creating fieldSelector for non-terminated Pods on Node",
			"r", r, "nodeName", nodeName)
		return nil, err
	}
	nodeNonTerminatedPodsList, err := clientSet.CoreV1().
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
