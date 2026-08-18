package plugin

import (
	"fmt"
	"os"
	"testing"
	"text/template"

	"github.com/spf13/viper"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest/fake"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"

	"github.com/bergerx/kubectl-status/pkg/input"
)

// TestMain flushes this process's accumulated template coverage (see template_coverage.go) once
// after every test in the package has run. pkg/plugin's test binary is a separate OS process from
// cmd's -- see FlushTemplateCoverageProfile's doc for why each needs its own TestMain rather than
// sharing one flush call.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := FlushTemplateCoverageProfile(); err != nil {
		// A flush failure (e.g. a relative KUBECTL_STATUS_TEMPLATE_COVERAGE path -- see that
		// function's doc) must fail the run, not just log: a silent failure here was exactly how
		// this surfaced originally, as a much harder to diagnose failure two steps later in
		// `make template-cover-html`, well after `go test` itself had already reported success.
		fmt.Fprintln(os.Stderr, "template coverage: failed to write profile:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// renderKindWithTree renders kind against obj using an explicit embedded tree, bypassing
// getTemplate/buildTemplateSet's own coverage-enabled gate -- letting
// TestTemplateCoverageInstrumentationIsOutputTransparent compare a plain and an instrumented tree
// built from identical source in the same test process, regardless of whether
// KUBECTL_STATUS_TEMPLATE_COVERAGE happens to be set for this test run.
func renderKindWithTree(t *testing.T, cfg *RenderConfig, embedded *template.Template, kind string, obj map[string]interface{}) string {
	t.Helper()
	user, err := embedded.Clone()
	if err != nil {
		t.Fatalf("cloning tree: %v", err)
	}
	kindNames, err := kindTemplateNames(embedded)
	if err != nil {
		t.Fatalf("kindTemplateNames: %v", err)
	}
	ts := &templateSet{embedded: embedded, user: user, kindNames: kindNames, userDefinedNames: map[string]bool{}}

	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, _ := input.NewResourceRepo(f, cfg.Viper)
	e, err := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard(), cfg)
	if err != nil {
		t.Fatalf("newRenderEngine: %v", err)
	}
	e.templateSet = ts
	r := newRenderableObject(obj, e, repo)
	got, err := r.renderTemplate(kind, r)
	if err != nil {
		t.Fatalf("renderTemplate(%s): %v", kind, err)
	}
	return got
}

// TestTemplateCoverageInstrumentationIsOutputTransparent guards the riskiest part of
// template_coverage.go: that splicing a coverage marker before every coverable node's own opening
// delimiter never changes rendered output. It renders a representative set of Kinds through both a
// plain and an instrumented tree parsed from the same embedded source and asserts byte-identical
// output, specifically including the two patterns a design review surfaced as capable of breaking
// a naive (fixed-delimiter) marker: a non-dashed-delimiter branch with meaningful literal
// whitespace (Service.tmpl's ExternalName case) and a multi-line action (Pod.tmpl's
// imagePullSecrets handling, structurally similar to Node.tmpl's multi-line dict(...) call).
func TestTemplateCoverageInstrumentationIsOutputTransparent(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	funcs := templateFuncs(cfg)

	plain, err := template.New("templates").Funcs(funcs).ParseFS(templatesFS, "templates/*.tmpl", "templates/*/*.tmpl")
	if err != nil {
		t.Fatalf("plain parse: %v", err)
	}
	instrumented, err := buildInstrumentedEmbedded(funcs)
	if err != nil {
		t.Fatalf("instrumented parse: %v", err)
	}

	cases := []struct {
		name string
		kind string
		obj  map[string]interface{}
	}{
		{
			name: "ExternalName Service (non-dashed delimiter + literal whitespace)",
			kind: "Service",
			obj: map[string]interface{}{
				"kind":     "Service",
				"metadata": map[string]interface{}{"name": "svc", "namespace": "test"},
				"spec":     map[string]interface{}{"type": "ExternalName", "externalName": "example.com"},
			},
		},
		{
			name: "ClusterIP Service",
			kind: "Service",
			obj: map[string]interface{}{
				"kind":     "Service",
				"metadata": map[string]interface{}{"name": "svc2", "namespace": "test"},
				"spec":     map[string]interface{}{"type": "ClusterIP", "clusterIP": "10.0.0.1"},
			},
		},
		{
			name: "Pod with imagePullSecrets (multi-line action)",
			kind: "Pod",
			obj: map[string]interface{}{
				"kind":     "Pod",
				"metadata": map[string]interface{}{"name": "p", "namespace": "test"},
				"spec": map[string]interface{}{
					"containers":       []interface{}{map[string]interface{}{"name": "c", "image": "busybox"}},
					"imagePullSecrets": []interface{}{map[string]interface{}{"name": "regcred"}},
				},
				"status": map[string]interface{}{"phase": "Running"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := renderKindWithTree(t, cfg, plain, tc.kind, tc.obj)
			got := renderKindWithTree(t, cfg, instrumented, tc.kind, tc.obj)
			if got != want {
				t.Errorf("instrumentation changed rendered output for %s\n--- plain (%d bytes) ---\n%q\n--- instrumented (%d bytes) ---\n%q",
					tc.kind, len(want), want, len(got), got)
			}
		})
	}
}
