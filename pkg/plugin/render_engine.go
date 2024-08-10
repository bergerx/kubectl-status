package plugin

import (
	"embed"
	"os"
	"path/filepath"
	"text/template"

	"github.com/go-sprout/sprout"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/klog/v2"
)

//go:embed templates
var templatesFS embed.FS

// renderEngine provides methods to build kubernetes api queries from provided cli options.
// Also holds the parsed templates.
type renderEngine struct {
	ioStreams genericiooptions.IOStreams
	template.Template
}

func newRenderEngine(streams genericiooptions.IOStreams) (*renderEngine, error) {
	klog.V(5).InfoS("Creating new render engine instance...")
	tmpl, err := getTemplate()
	if err != nil {
		klog.V(3).ErrorS(err, "Error parsing templates")
		return nil, err
	}
	return &renderEngine{
		streams,
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
