package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/spf13/viper"
	"k8s.io/client-go/rest/fake"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"

	"github.com/bergerx/kubectl-status/pkg/input"
)

func TestGetTemplate(t *testing.T) {
	t.Run("templates are parsable", func(t *testing.T) {
		_, err := getTemplate(NewRenderConfig(viper.New()))
		if err != nil {
			t.Errorf("getTemplate() error = %v", err)
			return
		}
	})
}

func TestFindTemplateName(t *testing.T) {
	funcs := template.FuncMap{}
	embedded := template.Must(template.New("templates").Funcs(funcs).Parse(
		`{{ define "Gateway" }}{{ end }}{{ define "Gateway.networking.istio.io" }}{{ end }}`))
	user, err := embedded.Clone()
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	ts := &templateSet{
		embedded:         embedded,
		user:             user,
		kindNames:        map[string]bool{"Gateway": true, "DefaultResource": true},
		userDefinedNames: map[string]bool{},
	}

	tests := []struct {
		name  string
		kind  string
		group string
		want  string
	}{
		{"group-qualified template wins over bare kind", "Gateway", "networking.istio.io", "Gateway.networking.istio.io"},
		{"falls back to bare kind when no qualified template exists", "Gateway", "gateway.networking.k8s.io", "Gateway"},
		{"falls back to bare kind for the core group", "Gateway", "", "Gateway"},
		{"falls back to DefaultResource when neither exists", "Frobnicator", "example.io", "DefaultResource"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := ts.findTemplateName(tt.kind, tt.group); got != tt.want {
				t.Errorf("findTemplateName(%q, %q) = %q, want %q", tt.kind, tt.group, got, tt.want)
			}
		})
	}
}

// TestUserOverlayCannotHijackInternalHelper is the regression test for #809: a user overlay file
// that redefines an internal shared partial's name (resource_ref, in this repro) must never
// affect a built-in Kind template's use of the real helper, even though before #809 both were
// parsed into the same flat *template.Template namespace and a later ParseGlob call silently won.
//
// This exercises the actual production code path (buildTemplateSet -> templateSet.
// findTemplateName/resolveIncludeTree, the same functions renderEngine/RenderableObject use),
// not a standalone text/template toy snippet, by pointing HOME at a temp dir with a
// ~/.kubectl-status/templates/resource_ref.tmpl overlay that hijacks "resource_ref", then
// rendering a built-in "Pod" template that calls the real resource_ref internally
// (status_summary_line -> ... -> resource_ref via the object's owner reference, and directly via
// a minimal template asserting the exact hijack scenario from the issue).
func TestUserOverlayCannotHijackInternalHelper(t *testing.T) {
	tmpDir := t.TempDir()
	templatesDir := filepath.Join(tmpDir, ".kubectl-status", "templates")
	if err := os.MkdirAll(templatesDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	overlay := `{{ define "resource_ref" }}HIJACKED{{ end }}`
	if err := os.WriteFile(filepath.Join(templatesDir, "resource_ref.tmpl"), []byte(overlay), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("HOME", tmpDir)

	cfg := NewRenderConfig(viper.New())
	ts, err := getTemplate(cfg)
	if err != nil {
		t.Fatalf("getTemplate() error = %v", err)
	}

	// Sanity check: the overlay really was picked up, and it really does define
	// "resource_ref" in the user tree -- otherwise this test would pass for the wrong reason
	// (e.g. HOME not honored, or the overlay glob not matching).
	if !ts.userDefinedNames["resource_ref"] {
		t.Fatalf("test setup broken: overlay's resource_ref.tmpl was not picked up as a user-defined name")
	}
	var hijackedBuf bytes.Buffer
	if err := ts.user.ExecuteTemplate(&hijackedBuf, "resource_ref", nil); err != nil {
		t.Fatalf("user.ExecuteTemplate(resource_ref) error = %v", err)
	}
	if hijackedBuf.String() != "HIJACKED" {
		t.Fatalf("test setup broken: user tree's own resource_ref = %q, want HIJACKED", hijackedBuf.String())
	}

	// The actual regression check: rendering a built-in Pod (which was NOT overridden by the
	// user overlay, so it must always execute against the embedded tree) must still see the
	// real, embedded "resource_ref" -- not the overlay's "HIJACKED" -- via both call
	// conventions internal helpers use in this codebase: the native `{{template}}` action
	// (resolved entirely inside text/template, scoped to whichever *Template.ExecuteTemplate
	// was actually called) and the `.Include`/`$.Include` Go-level bridge (resolved via
	// templateSet.resolveIncludeTree).
	f := cmdtesting.NewTestFactory().WithNamespace("default")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, err := input.NewResourceRepo(f, cfg.Viper)
	if err != nil {
		t.Fatalf("NewResourceRepo() error = %v", err)
	}
	engine := &renderEngine{cfg: cfg, renderedUIDs: make(uidSet), templateSet: ts}
	pod := newRenderableObject(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": "hijack-test-pod", "namespace": "default"},
	}, engine, repo)

	got, err := pod.renderString()
	if err != nil {
		t.Fatalf("renderString() error = %v", err)
	}
	if strings.Contains(got, "HIJACKED") {
		t.Fatalf("built-in Pod render was hijacked by the user overlay's resource_ref override:\n%s", got)
	}

	// Directly exercise the exact repro shape from the issue: an embedded template that calls
	// resource_ref via `.Include`, executed against the embedded tree, must render the real
	// helper regardless of what the (separately parsed, isolated) user tree defines.
	viaInclude, err := pod.Include("resource_ref", map[string]interface{}{"kind": "Pod", "name": "hijack-test-pod"})
	if err != nil {
		t.Fatalf("Include(resource_ref) error = %v", err)
	}
	if strings.Contains(viaInclude, "HIJACKED") {
		t.Fatalf(".Include(\"resource_ref\", ...) from a built-in-tree render returned the hijacked overlay version: %q", viaInclude)
	}
}
