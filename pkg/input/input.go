package input

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/cmd/util"
)

type ResourceRepo struct {
	f      util.Factory
	client dynamic.Interface
}

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

func NewResourceRepo(factory util.Factory) ResourceRepo {
	return ResourceRepo{
		f: factory,
	}
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

func (r *ResourceRepo) toRESTMapper() (meta.RESTMapper, error) {
	return r.f.ToRESTMapper()
}

func (r *ResourceRepo) KubernetesClientSet() (*kubernetes.Clientset, error) {
	return r.f.KubernetesClientSet()
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
	restMapper, err := r.toRESTMapper()
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
