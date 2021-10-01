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

func GetResources(
	clientGetter *genericclioptions.ConfigFlags,
	namespace string,
	allNamespaces bool,
	enforceNamespace bool,
	filenames []string,
	selector string,
	fieldSelector string,
	args []string,
) ([]*resource.Info, error) {
	resourceResult := resource.
		NewBuilder(clientGetter).
		Unstructured().
		NamespaceParam(namespace).DefaultNamespace().AllNamespaces(allNamespaces).
		FilenameParam(enforceNamespace, &resource.FilenameOptions{Filenames: filenames}).
		LabelSelectorParam(selector).
		FieldSelectorParam(fieldSelector).
		ResourceTypeOrNameArgs(true, args...).
		ContinueOnError().
		Latest().
		Flatten().
		Do()
	err := resourceResult.Err()
	if err != nil {
		return nil, errors.WithMessage(err, "Failed during querying of resources")
	}
	resourceInfos, err := resourceResult.Infos()
	if err != nil {
		return nil, err
	}
	if len(resourceInfos) == 0 {
		if !allNamespaces && namespace != "" {
			fmt.Printf("No resources found in %s namespace\n", namespace)
		} else {
			fmt.Printf("No resources found.\n")
		}
	}
	return resourceInfos, nil
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

func RenderResource(restConfig *rest.Config, resourceInfo *resource.Info, clientSet *kubernetes.Clientset) error {
	obj := resourceInfo.Object
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&obj)
	if err != nil {
		return err
	}
	err = includeEvents(obj, clientSet, out)
	if err != nil {
		return err
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
