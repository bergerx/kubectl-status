package input

import (
	"sort"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/cmd/util"
)

type ResourceRepo struct {
	f util.Factory
}

type Object map[string]interface{}

func (u Object) creationTimestamp() string {
	m, ok := u["metadata"].(map[string]string)
	if !ok {
		return ""
	}
	return m["creationTimestamp"]
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

func (r *ResourceRepo) ToRESTMapper() (meta.RESTMapper, error) {
	return r.f.ToRESTMapper()
}

func (r *ResourceRepo) KubernetesClientSet() (*kubernetes.Clientset, error) {
	return r.f.KubernetesClientSet()
}

func (r *ResourceRepo) DynamicClient() (dynamic.Interface, error) {
	return r.f.DynamicClient()
}
