package plugin

import (
	"embed"
	"fmt"
	"github.com/Masterminds/sprig/v3"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"text/template"
)

//go:embed templates
var templatesFS embed.FS

// renderEngine provides methods to build kubernetes api queries from provided cli options.
// Also holds the parsed templates.
type renderEngine struct {
	Options
	template.Template
}

func newRenderEngine(options Options) (*renderEngine, error) {
	klog.V(5).InfoS("Creating new render engine instance...", "options", options)
	tmpl, err := getTemplate()
	if err != nil {
		klog.V(3).ErrorS(err, "Error parsing templates")
		return nil, err
	}
	return &renderEngine{
		options,
		*tmpl,
	}, nil
}

// We don't overlay templates dynamically, we use them all in all cases, this may be inefficient and changing this
// could be beneficial in the future. But we parse them all once and re-use again for all template executions.
func getTemplate() (*template.Template, error) {
	klog.V(5).InfoS("Creating new template instance...")
	tmpl := template.
		New("templates").
		Funcs(sprig.TxtFuncMap()).
		Funcs(funcMap())
	return parseTemplates(tmpl)
}

func parseTemplates(tmpl *template.Template) (*template.Template, error) {
	klog.V(5).InfoS("parsing templates from the embedded template fs ...")
	parsedTemplates, err := tmpl.ParseFS(templatesFS, "templates/*.tmpl")
	if err != nil {
		klog.V(3).ErrorS(err, "Error parsing some templates")
		return nil, err
	}
	klog.V(5).InfoS("Finished parsing all embedded template fs files.")
	return parsedTemplates, nil
}

// Use kind name if such template exists in templates, else returnDefaultResource
func findTemplateName(tmpl template.Template, kind string) string {
	if tmpl.Lookup(kind) == nil {
		return "DefaultResource"
	}
	return kind
}

// This is a modified copy of resource.Builder's mappingFor method.
func (e *renderEngine) mappingFor(resourceOrKindArg string) (*meta.RESTMapping, error) {
	fullySpecifiedGVR, groupResource := schema.ParseResourceArg(resourceOrKindArg)
	gvk := schema.GroupVersionKind{}
	restMapper, err := e.ToRESTMapper()
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

func (e renderEngine) namespace() (string, error) {
	namespace, _, err := e.ToRawKubeConfigLoader().Namespace()
	return namespace, err
}

func (e renderEngine) kubernetesClientSet() (*kubernetes.Clientset, error) {
	restConfig, err := e.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restConfig)
}

func (e renderEngine) dynamicInterface() (dynamic.Interface, error) {
	restConfig, err := e.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(restConfig)
}

// newBuilder returns an unstructured resource builder which uses the namespace from the cli parameters.
// This can be used to run further queries for related resources.
// The resulting builder will have the namespace used in the resource builder flags.
func (e renderEngine) newBuilder() *resource.Builder {
	namespace, _ := e.namespace()
	allNamespaces := e.ResourceBuilderFlags.AllNamespaces
	return resource.NewBuilder(e).
		NamespaceParam(namespace).
		DefaultNamespace().
		AllNamespaces(*allNamespaces).
		ContinueOnError().
		Unstructured().
		Latest().
		Flatten()
}

// getQueriedResources is the main entrypoint when using the cli.
func (e renderEngine) getQueriedResources(args []string) (*resource.Result, []*resource.Info, error) {
	filenameOptions := e.ResourceBuilderFlags.FileNameFlags.ToOptions()
	builder := e.newBuilder().
		FilenameParam(false, &filenameOptions).
		LabelSelectorParam(*e.ResourceBuilderFlags.LabelSelector).
		FieldSelectorParam(*e.ResourceBuilderFlags.FieldSelector).
		ResourceTypeOrNameArgs(true, args...).
		ContinueOnError()
	results := builder.Do()
	resourceInfos, err := results.Infos()
	return results, resourceInfos, err
}

func (e renderEngine) getResourceQueryResults(namespace string, args []string) *resource.Result {
	klog.V(5).InfoS("getResourceQueryResults", "namespace", namespace, "args", args)
	builder := e.newBuilder().NamespaceParam(namespace).DefaultNamespace().ResourceTypeOrNameArgs(true, args...)
	return builder.Do()
}
