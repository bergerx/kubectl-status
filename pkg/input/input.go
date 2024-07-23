package input

import (
	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kubectl/pkg/cmd/util"
)

type ResourceRepo struct {
	f util.Factory
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

func (r *ResourceRepo) ResourceInfos(namespace string, args []string, labelSelector string) ([]*resource.Info, error) {
	builder := r.newBaseBuilder().
		NamespaceParam(namespace).
		ResourceTypeOrNameArgs(true, args...).
		LabelSelectorParam(labelSelector)
	return builder.Do().Infos()
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
