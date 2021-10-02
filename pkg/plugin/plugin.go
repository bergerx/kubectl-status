package plugin

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	_ "unsafe" // required for using go:linkname in the file

	"github.com/pkg/errors"
	"k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	kyaml "sigs.k8s.io/yaml"
)

type IngressBackendIssue struct {
	IssueType string
	Backend   v1beta1.IngressBackend
}

//go:linkname signame runtime.signame
func signame(sig uint32) string

func NewResourceStatusQuery(
	clientGetter *genericclioptions.ConfigFlags,
	namespace string,
	allNamespaces bool,
	enforceNamespace bool,
	filenames []string,
	selector string,
	fieldSelector string,
	args []string,
) *ResourceStatusQuery {
	return &ResourceStatusQuery{
		clientGetter,
		namespace,
		allNamespaces,
		enforceNamespace,
		filenames,
		selector,
		fieldSelector,
		args,
	}
}

type ResourceStatusQuery struct {
	clientGetter     *genericclioptions.ConfigFlags
	namespace        string
	allNamespaces    bool
	enforceNamespace bool
	filenames        []string
	selector         string
	fieldSelector    string
	args             []string
}

func (q ResourceStatusQuery) getResourceInfos(resourceResult *resource.Result) ([]*resource.Info, error) {
	err := resourceResult.Err()
	if err != nil {
		return nil, errors.WithMessage(err, "Failed during querying of resources")
	}
	resourceInfos, err := resourceResult.Infos()
	if err != nil {
		return nil, errors.WithMessage(err, "Failed getting resource  infos")
	}
	return resourceInfos, nil
}

func (q ResourceStatusQuery) getQueriedResources() ([]*resource.Info, error) {
	resourceInfos, err := q.getResourceInfos(resource.
		NewBuilder(q.clientGetter).
		Unstructured().
		NamespaceParam(q.namespace).DefaultNamespace().AllNamespaces(q.allNamespaces).
		FilenameParam(q.enforceNamespace, &resource.FilenameOptions{Filenames: q.filenames}).
		LabelSelectorParam(q.selector).
		FieldSelectorParam(q.fieldSelector).
		ResourceTypeOrNameArgs(true, q.args...).
		ContinueOnError().
		Latest().
		Flatten().
		Do())
	if err != nil {
		return nil, errors.WithMessage(err, "Failed getting resource infos")
	}
	if len(resourceInfos) == 0 {
		if !q.allNamespaces && q.namespace != "" {
			fmt.Printf("No resources found in %s namespace\n", q.namespace)
		} else {
			fmt.Printf("No resources found.\n")
		}
	}
	return resourceInfos, nil
}

func (q ResourceStatusQuery) RenderResourceInfos(resourceInfos []*resource.Info) []error {
	var allRenderErrs []error
	for _, resourceInfo := range resourceInfos {
		err := q.RenderResource(resourceInfo)
		if err != nil {
			allRenderErrs = append(allRenderErrs, err)
		}
	}
	return allRenderErrs
}

func (q ResourceStatusQuery) RenderQueriedResources() []error {
	resourceInfos, err := q.getQueriedResources()
	if err != nil {
		return []error{err}
	}
	return q.RenderResourceInfos(resourceInfos)
}

func (q ResourceStatusQuery) RenderOtherResources(namespace, kind, name string) []error {
	resourceInfos, err := q.getResourceInfos(resource.
		NewBuilder(q.clientGetter).
		Unstructured().
		NamespaceParam(namespace).
		ResourceTypeOrNameArgs(true, kind, name).
		ContinueOnError().
		Latest().
		Flatten().
		Do())
	if err != nil {
		return []error{err}
	}
	return q.RenderResourceInfos(resourceInfos)
}

func (q ResourceStatusQuery) GetResourcesQueryFunc() func(string, string, string) (interface{}, error) {
	return func(namespace, kind, name string) (interface{}, error) {
		resourceInfos, err := q.getResourceInfos(resource.
			NewBuilder(q.clientGetter).
			Unstructured().
			NamespaceParam(namespace).
			ResourceTypeOrNameArgs(true, kind, name).
			ContinueOnError().
			Latest().
			Flatten().
			Do())
		if err != nil {
			return nil, err
		}
		return q.getUnstructuredObj(resourceInfos)
	}
}
func (q ResourceStatusQuery) RenderResource(resourceInfo *resource.Info) error {
	out, err := q.getUnstructuredObj(resourceInfo.Object)
	if err != nil {
		return errors.WithMessage(err, "Failed getting unstructured object")
	}
	restConfig, err := q.clientGetter.ToRESTConfig()
	if err != nil {
		return errors.WithMessage(err, "Failed getting rest config")
	}
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return errors.WithMessage(err, "Failed getting clientset")
	}
	obj := resourceInfo.Object
	err = includeEvents(obj, clientSet, out)
	if err != nil {
		return errors.WithMessage(err, "Failed to include events")
	}
	kindInjectFuncMap := map[string][]func(obj runtime.Object, restConfig *rest.Config, out map[string]interface{}) error{
		"Node":        {includeNodeMetrics, includeNodeLease, includePodDetailsOnNode, includeNodeStatsSummary},
		"Pod":         {includePodMetrics}, // kubectl get --raw /api/v1/nodes/minikube/proxy/stats/summary --> .pods[] | select podRef | containers[] | select name
		"Service":     {includeEndpoint},
		"StatefulSet": {includeStatefulSetDiff},
		"Ingress":     {includeIngressServices},
	}
	functions := kindInjectFuncMap[obj.GetObjectKind().GroupVersionKind().Kind]
	for _, f := range functions {
		err = f(obj, restConfig, out)
		if err != nil {
			return err
		}
	}

	err = renderTemplateForMap(os.Stdout, out)
	if err != nil {
		return err
	}
	// Add a newline at the end of every template
	fmt.Println("")
	return nil
}

func (q ResourceStatusQuery) getUnstructuredObj(obj interface{}) (map[string]interface{}, error) {
	return runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
}

func RenderFile(manifestFilename string) (string, error) {
	var out map[string]interface{}
	manifestFile, _ := ioutil.ReadFile(manifestFilename)
	err := kyaml.Unmarshal(manifestFile, &out)
	if err != nil {
		return "", errors.WithMessage(err, "Failed getting JSON for object")
	}
	var output bytes.Buffer
	err = renderTemplateForMap(&output, out)
	if err != nil {
		return "", err
	}
	return output.String(), nil
}
