package plugin

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/go-sprout/sprout"
	"github.com/go-sprout/sprout/group/hermetic"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/klog/v2"
)

//go:embed templates
var templatesFS embed.FS

// renderEngine provides methods to build kubernetes api queries from provided cli options.
// Also holds the parsed templates.
type renderEngine struct {
	ioStreams    genericiooptions.IOStreams
	cfg          *RenderConfig
	renderedUIDs uidSet
	templateSet  *templateSet
}

// templateSet holds the parsed embedded and user-overlay templates as two independent
// *template.Template trees, plus the lookup tables findTemplateName/resolveIncludeTree use to
// decide which tree a given template name should execute against.
//
// Before #809 both sets were parsed into a single *template.Template (embedded.ParseFS followed
// by a second .ParseGlob for ~/.kubectl-status/templates/*.tmpl), which put every name -- the
// per-Kind entry points and every internal shared partial (resource_ref, conditions_summary,
// ...) -- in one flat, mutable namespace: a user overlay file that happened to contain
// e.g. {{define "resource_ref"}} silently replaced that helper for every built-in template in
// the process, with no error or warning. Keeping the two trees fully separate (see
// buildTemplateSet) means a built-in Kind template's use of an internal helper resolves purely
// within embedded's own associated-template set -- something the user tree can never reach into,
// regardless of what it defines.
type templateSet struct {
	// embedded is parsed only from the templates this binary embeds (templatesFS) and is never
	// parsed into again after buildTemplateSet returns. Every built-in Kind template is always
	// executed against this tree, so its own `{{template "name" ...}}` calls to shared partials
	// resolve here and only here.
	embedded *template.Template
	// user is embedded.Clone() with the user overlay (~/.kubectl-status/templates/*.tmpl, if
	// any) parsed on top of the clone. Clone gives an independent copy of the associated
	// templates -- parsing into the clone can redefine a name (including one of the shared
	// partials) without that redefinition ever reaching the original embedded tree. Kept as a
	// full clone (rather than just the overlay's own content) so a user's own <Kind>.tmpl
	// override can still call every stable shared helper by name exactly like a built-in
	// template does.
	user *template.Template
	// kindNames is the set of top-level Kind-dispatch names: every embedded
	// pkg/plugin/templates/<Kind>.tmpl file whose basename is itself a defined template name
	// (the convention every shipped Kind template follows), plus "DefaultResource" (defined
	// inside common.tmpl, so it has no same-named file of its own). A name in this set is
	// resolved with the user override honored wherever it's referenced -- matching
	// findTemplateName's own dispatch and the handful of built-in
	// `{{ $.Include "<Kind>" $obj }}` calls documented in TEMPLATE-API.md (e.g.
	// matching_services' `Include "Service"`) -- regardless of which tree the calling template
	// happens to be executing in.
	kindNames map[string]bool
	// userDefinedNames is the set of names the raw user overlay files actually define,
	// determined from a standalone parse that never touches embedded or user. This is what lets
	// findTemplateName/resolveIncludeTree tell a genuine user override of a Kind template ("the
	// overlay itself defines this name") apart from a name the user tree merely inherited via
	// Clone from embedded (every embedded name, by construction of user).
	userDefinedNames map[string]bool
}

func newRenderEngine(streams genericiooptions.IOStreams, cfg *RenderConfig) (*renderEngine, error) {
	klog.V(5).InfoS("Creating new render engine instance...")
	ts, err := getTemplate(cfg)
	if err != nil {
		klog.V(3).ErrorS(err, "Error parsing templates")
		return nil, err
	}
	return &renderEngine{
		ioStreams:    streams,
		cfg:          cfg,
		renderedUIDs: make(uidSet),
		templateSet:  ts,
	}, nil
}

// templateFuncs builds the combined native sprout (hermetic group) and project funcMap every
// template tree (embedded and user) is parsed with.
func templateFuncs(cfg *RenderConfig) template.FuncMap {
	handler := sprout.New(sprout.WithGroups(hermetic.RegistryGroup()))
	sproutFuncMap := handler.Build()
	// env/expandEnv let a template read the process environment, which isn't needed by any
	// built-in template and would let a stray template dropped into ~/.kubectl-status/templates
	// leak env vars (e.g. cloud credentials) into rendered output.
	delete(sproutFuncMap, "env")
	delete(sproutFuncMap, "expandEnv")
	delete(sproutFuncMap, "expandenv")
	funcs := make(template.FuncMap, len(sproutFuncMap)+32)
	for name, fn := range sproutFuncMap {
		funcs[name] = fn
	}
	for name, fn := range cfg.funcMap() {
		funcs[name] = fn
	}
	return funcs
}

// We don't overlay templates dynamically, we use them all in all cases, this may be inefficient and changing this
// could be beneficial in the future. But we parse them all once and re-use again for all template executions.
func getTemplate(cfg *RenderConfig) (*templateSet, error) {
	klog.V(5).InfoS("Creating new template instance...")
	funcs := templateFuncs(cfg)
	return buildTemplateSet(funcs)
}

// buildTemplateSet parses the embedded templates and the optional user overlay
// (~/.kubectl-status/templates/*.tmpl) into two independent *template.Template trees -- see the
// templateSet doc comment for why they're no longer merged into one shared namespace.
func buildTemplateSet(funcs template.FuncMap) (*templateSet, error) {
	klog.V(5).InfoS("parsing templates from the embedded template fs ...")
	// Two patterns: root-level shared files (templates/common.tmpl) plus one level of
	// per-ecosystem subdirectories (templates/<group>/<Kind>.tmpl, templates/<group>/<group>_common.tmpl
	// -- see #807). ParseFS accepts multiple glob patterns; this does not touch the separate
	// user-override glob in parseUserOverlay below, which stays a flat ~/.kubectl-status/templates/*.tmpl.
	var embedded *template.Template
	var err error
	if templateCoverageEnabled() {
		// See template_coverage.go: only active under KUBECTL_STATUS_TEMPLATE_COVERAGE (the
		// Makefile's coverage targets), never in normal CLI use or the default `make test`.
		embedded, err = buildInstrumentedEmbedded(funcs)
	} else {
		embedded, err = template.New("templates").Funcs(funcs).ParseFS(templatesFS, "templates/*.tmpl", "templates/*/*.tmpl")
	}
	if err != nil {
		klog.V(3).ErrorS(err, "Error parsing some templates")
		return nil, err
	}

	kindNames, err := kindTemplateNames(embedded)
	if err != nil {
		klog.V(3).ErrorS(err, "Error determining Kind-dispatch template names")
		return nil, err
	}

	// user starts as an independent copy of embedded (see templateSet.user's doc comment), so
	// that even without any user overlay present, every stable shared helper and Kind template
	// remains callable from it exactly as it would from embedded.
	user, err := embedded.Clone()
	if err != nil {
		klog.V(3).ErrorS(err, "Error cloning embedded templates for the user overlay tree")
		return nil, err
	}

	userDefinedNames := parseUserOverlay(funcs, user)

	klog.V(5).InfoS("Finished parsing all embedded template fs files.")
	return &templateSet{
		embedded:         embedded,
		user:             user,
		kindNames:        kindNames,
		userDefinedNames: userDefinedNames,
	}, nil
}

// parseUserOverlay locates ~/.kubectl-status/templates/*.tmpl (if any), parses it into user (a
// clone of the embedded tree, mutated in place), and returns the set of names the raw overlay
// files themselves define.
//
// That name set is computed from a second, standalone parse of the very same files into a bare
// template that never shares any state with embedded or user: parsing the overlay directly onto
// user's clone (as required for a user's own Kind override to be able to call the inherited
// shared helpers) makes every embedded name look identical to a "user defined" one by the time
// it's done, since user already had them all via Clone. Only this separate bare parse can tell
// us which names the overlay actually provided.
func parseUserOverlay(funcs template.FuncMap, user *template.Template) map[string]bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		klog.V(3).ErrorS(err, "error getting user home dir, ignoring")
	}
	templatesDir := filepath.Join(homeDir, ".kubectl-status", "templates")
	templatePattern := filepath.Join(templatesDir, "*.tmpl")
	matches, _ := filepath.Glob(templatePattern)
	if len(matches) == 0 {
		return map[string]bool{}
	}

	probe, err := template.New("kubectl-status-user-overlay-probe").Funcs(funcs).ParseGlob(templatePattern)
	if err != nil {
		klog.V(1).ErrorS(err, "Error parsing user provided templates, ignoring user provided templates")
		return map[string]bool{}
	}
	userDefinedNames := make(map[string]bool, len(probe.Templates()))
	for _, t := range probe.Templates() {
		if t.Name() == "kubectl-status-user-overlay-probe" {
			continue
		}
		userDefinedNames[t.Name()] = true
	}

	if _, err := user.ParseGlob(templatePattern); err != nil {
		// Parsing the identical set of files that the probe parse (above) just parsed
		// successfully should not be able to fail here, but fail safe and drop the user
		// overlay entirely rather than leave userDefinedNames pointing at names user doesn't
		// actually have.
		klog.V(1).ErrorS(err, "Error parsing user provided templates, ignoring user provided templates")
		return map[string]bool{}
	}
	return userDefinedNames
}

// kindTemplateNames returns the set of top-level Kind-dispatch names -- every embedded
// pkg/plugin/templates/<Kind>.tmpl or pkg/plugin/templates/<group>/<Kind>.tmpl file whose
// basename is itself a defined template name (the convention every shipped Kind template
// follows, see TEMPLATE-API.md's "Kind templates" section), plus "DefaultResource" (defined
// inside common.tmpl, so it has no same-named file of its own). Shared-helper files like
// common.tmpl/policy/policy_report_common.tmpl don't define a template matching their own
// basename, so they're naturally excluded.
func kindTemplateNames(embedded *template.Template) (map[string]bool, error) {
	names := map[string]bool{"DefaultResource": true}
	// Two patterns, matching the two ParseFS patterns in buildTemplateSet: root-level files
	// plus one level of per-ecosystem subdirectories (see #807). fs.Glob only accepts a single
	// pattern per call, unlike ParseFS, hence the two separate calls merged below.
	for _, pattern := range []string{"templates/*.tmpl", "templates/*/*.tmpl"} {
		entries, err := fs.Glob(templatesFS, pattern)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			base := strings.TrimSuffix(filepath.Base(entry), ".tmpl")
			if embedded.Lookup(base) != nil {
				names[base] = true
			}
		}
	}
	return names, nil
}

// treeFor returns the user tree when the raw user overlay itself defines a template under name,
// otherwise the embedded tree. Never consults anything user inherited only via Clone.
func (ts *templateSet) treeFor(name string) *template.Template {
	if ts.userDefinedNames[name] {
		return ts.user
	}
	return ts.embedded
}

// findTemplateName picks the template to render an object with, and which of the two trees
// (embedded or user overlay) to execute it against. Kind alone doesn't uniquely identify a
// resource type -- e.g. Gateway API and Istio both define a Kind=Gateway in different API
// groups -- so a "<Kind>.<group>" template is preferred when one exists, falling back to the
// bare kind name (which is also what every template not part of such a collision is named), and
// finally to DefaultResource. See https://github.com/bergerx/kubectl-status/issues/789.
//
// A name is only resolved against the user tree when the raw user overlay itself defines that
// exact name (ts.userDefinedNames) -- never merely because the clone-based user tree happens to
// have inherited it from embedded. See #809.
func (ts *templateSet) findTemplateName(kind, group string) (tree *template.Template, name string) {
	if group != "" {
		if qualified := kind + "." + group; ts.embedded.Lookup(qualified) != nil || ts.userDefinedNames[qualified] {
			return ts.treeFor(qualified), qualified
		}
	}
	if ts.embedded.Lookup(kind) != nil || ts.userDefinedNames[kind] {
		return ts.treeFor(kind), kind
	}
	return ts.treeFor("DefaultResource"), "DefaultResource"
}

// findSummaryTemplateName picks the compact one-line summary template for a Kind, following the
// same "<Kind>.<group>" (for a Kind name that collides across API groups) then bare "<Kind>"
// preference findTemplateName uses for the full view -- just with a ".summary" suffix, e.g.
// "Job.summary" or, for a colliding Kind, "Gateway.networking.istio.io.summary". Checked against
// both embedded and the user overlay (ts.userDefinedNames), so a user override or a template
// shipped only for a custom CRD kind is discovered exactly like a "<Kind>.tmpl" override is.
//
// Unlike findTemplateName, there is no generic fallback name here -- ok is false when neither
// form is defined, and the caller (RenderableObject.HealthSummary) falls back to
// generic_health_summary itself. See https://github.com/bergerx/kubectl-status/issues/826.
func (ts *templateSet) findSummaryTemplateName(kind, group string) (name string, ok bool) {
	if group != "" {
		if qualified := kind + "." + group + ".summary"; ts.embedded.Lookup(qualified) != nil || ts.userDefinedNames[qualified] {
			return qualified, true
		}
	}
	if plain := kind + ".summary"; ts.embedded.Lookup(plain) != nil || ts.userDefinedNames[plain] {
		return plain, true
	}
	return "", false
}

// resolveIncludeTree decides which tree an explicit `.Include name data` call (or the
// IncludeRenderableObject primitive, via its own fresh render()) should execute name against,
// given current -- the tree the calling template's own top-level render already dispatched to
// (see RenderableObject.currentTree).
//
// Known Kind-dispatch names (ts.kindNames) always honor a genuine user override wherever
// they're referenced, matching findTemplateName and the handful of built-in
// `{{ $.Include "<Kind>" $obj }}` calls documented in TEMPLATE-API.md (e.g. matching_services'
// `Include "Service"`) -- a user's Service.tmpl override is expected to apply even when invoked
// from an unrelated built-in template. A "<Kind>.summary"/"<Kind>.<group>.summary" name (see
// findSummaryTemplateName, #826) gets the same treatment for the same reason:
// resource_health_summary is itself a built-in template executing in embedded, and needs to
// reach a user-provided summary for a Kind it has never heard of (e.g. a custom CRD listed by
// crossplane_composed_resources) exactly as readily as a built-in one.
//
// Every other name (the internal shared-partial names, plus anything a user's own override
// defines purely for its own use) resolves strictly within current. This is what keeps a user
// overlay's `{{define "resource_ref"}}` from ever reaching a built-in template's real
// resource_ref call: that call happens while current == embedded, so resolveIncludeTree never
// even consults userDefinedNames for it -- only a render that already originates from the user
// tree (i.e. the user's own Kind override, or something it itself Includes) can ever observe
// the user's own redefinition.
func (ts *templateSet) resolveIncludeTree(name string, current *template.Template) *template.Template {
	if ts.kindNames[name] || strings.HasSuffix(name, ".summary") {
		return ts.treeFor(name)
	}
	if current != nil {
		return current
	}
	return ts.embedded
}
