package plugin

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/go-sprout/sprout"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/resource"
	"k8s.io/klog/v2"
	"k8s.io/kubectl/pkg/cmd/util"

	"github.com/bergerx/kubectl-status/pkg/input"
)

//go:embed templates
var templatesFS embed.FS

// renderEngine provides methods to build kubernetes api queries from provided cli options.
// Also holds the parsed templates.
type renderEngine struct {
	ioStreams genericiooptions.IOStreams
	repo      input.ResourceRepo
	template.Template
}

func newRenderEngine(f util.Factory, streams genericiooptions.IOStreams) (*renderEngine, error) {
	klog.V(5).InfoS("Creating new render engine instance...", "f", f)
	tmpl, err := getTemplate()
	repo := input.NewResourceRepo(f)
	if err != nil {
		klog.V(3).ErrorS(err, "Error parsing templates")
		return nil, err
	}
	return &renderEngine{
		streams,
		repo,
		*tmpl,
	}, nil
}

// We don't overlay templates dynamically, we use them all in all cases, this may be inefficient and changing this
// could be beneficial in the future. But we parse them all once and re-use again for all template executions.
func getTemplate() (*template.Template, error) {
	klog.V(5).InfoS("Creating new template instance...")
	tmpl := template.
		New("templates").
		Funcs(sprout.TxtFuncMap()).
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
	homeDir, err := os.UserHomeDir()
	if err != nil {
		klog.V(3).ErrorS(err, "error getting user home dir, ignoring")
	}
	templatesDir := filepath.Join(homeDir, ".kubectl-status", "templates")
	parsedTemplatesWithLocalTemplates, err := parsedTemplates.ParseGlob(filepath.Join(templatesDir, "*.tmpl"))
	if err != nil {
		klog.V(1).ErrorS(err, "Error parsing user provided templates, ignoring user provided templates")
	} else {
		parsedTemplates = parsedTemplatesWithLocalTemplates
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
	restMapper, err := e.repo.ToRESTMapper()
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

func (e renderEngine) getResourceQueryInfos(namespace string, args []string) ([]*resource.Info, error) {
	klog.V(5).InfoS("getResourceQueryInfos", "namespace", namespace, "args", args)
	return e.repo.ResourceInfos(namespace, args, "")
}
