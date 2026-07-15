package plugin

import (
	"context"
	"embed"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"text/template"

	"github.com/go-sprout/sprout/sprigin"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/klog/v2"
)

//go:embed templates
var templatesFS embed.FS

var deprecationFilterOnce sync.Once

// setupDeprecationFilter installs, once for the life of the process, a slog handler that drops
// go-sprout's per-call deprecation-signature warnings and forwards anything else to os.Stderr.
//
// go-sprout (our sprig replacement) logs a WARN via slog on every call to a
// function whose signature changed during the sprig->sprout migration (e.g.
// get, hasKey, append). These fire once per template execution per call site,
// so they flood the output; sprigin gives no way to disable them other than
// filtering the global slog default handler. See
// https://github.com/bergerx/kubectl-status/issues/688 for migrating the
// templates to the new signatures, at which point this filter can go away.
//
// This used to reinstall a fresh handler (writing to that invocation's streams.ErrOut) on every
// newRenderEngine call. sprigin reads slog.Default() again on every single template function
// call, not just once when the funcMap is built, so two renders in flight at once (e.g. parallel
// e2e subtests) would race rebinding the global default and could route one render's output
// through another's ErrOut. The only thing this handler is known to ever emit is the
// deprecation-tagged warnings it drops (see #688), so installing it once and forwarding the
// (currently unreachable) non-deprecated case to os.Stderr rather than a per-render writer is
// behaviorally identical to before, without serializing renders against each other.
//
// The handler is built fresh (not by wrapping the pre-existing
// slog.Default().Handler()): the zero-value default handler bridges back
// into the standard "log" package's global, mutex-protected logger, and
// re-installing a wrapper around it as the new default causes that bridge
// to call back into itself, deadlocking on the mutex.
func setupDeprecationFilter() {
	deprecationFilterOnce.Do(func() {
		slog.SetDefault(slog.New(deprecationNoticeFilter{Handler: slog.NewTextHandler(os.Stderr, nil)}))
	})
}

// deprecationNoticeFilter drops log records tagged with a "notice"="deprecated"
// attribute. go-sprout's deprecated.SignatureWarn attaches that attribute via
// Logger.With(...) rather than as a Record attribute, so WithAttrs must be
// overridden explicitly here too: without it, Go's method promotion on the
// embedded slog.Handler returns the *inner* handler from WithAttrs, silently
// discarding this wrapper for every subsequent Handle call.
type deprecationNoticeFilter struct {
	slog.Handler
	drop bool
}

func isDeprecatedNoticeAttr(a slog.Attr) bool {
	return a.Key == "notice" && a.Value.Kind() == slog.KindString && a.Value.String() == "deprecated"
}

func (h deprecationNoticeFilter) Enabled(ctx context.Context, level slog.Level) bool {
	if h.drop {
		return false
	}
	return h.Handler.Enabled(ctx, level)
}

func (h deprecationNoticeFilter) Handle(ctx context.Context, r slog.Record) error {
	if h.drop {
		return nil
	}
	drop := false
	r.Attrs(func(a slog.Attr) bool {
		if isDeprecatedNoticeAttr(a) {
			drop = true
			return false
		}
		return true
	})
	if drop {
		return nil
	}
	return h.Handler.Handle(ctx, r)
}

func (h deprecationNoticeFilter) WithAttrs(attrs []slog.Attr) slog.Handler {
	drop := h.drop
	for _, a := range attrs {
		if isDeprecatedNoticeAttr(a) {
			drop = true
		}
	}
	return deprecationNoticeFilter{Handler: h.Handler.WithAttrs(attrs), drop: drop}
}

func (h deprecationNoticeFilter) WithGroup(name string) slog.Handler {
	return deprecationNoticeFilter{Handler: h.Handler.WithGroup(name), drop: h.drop}
}

// renderEngine provides methods to build kubernetes api queries from provided cli options.
// Also holds the parsed templates.
type renderEngine struct {
	ioStreams    genericiooptions.IOStreams
	cfg          *RenderConfig
	renderedUIDs uidSet
	template.Template
}

func newRenderEngine(streams genericiooptions.IOStreams, cfg *RenderConfig) (*renderEngine, error) {
	klog.V(5).InfoS("Creating new render engine instance...")
	setupDeprecationFilter()
	tmpl, err := getTemplate(cfg)
	if err != nil {
		klog.V(3).ErrorS(err, "Error parsing templates")
		return nil, err
	}
	return &renderEngine{
		ioStreams:    streams,
		cfg:          cfg,
		renderedUIDs: make(uidSet),
		Template:     *tmpl,
	}, nil
}

// We don't overlay templates dynamically, we use them all in all cases, this may be inefficient and changing this
// could be beneficial in the future. But we parse them all once and re-use again for all template executions.
func getTemplate(cfg *RenderConfig) (*template.Template, error) {
	klog.V(5).InfoS("Creating new template instance...")
	tmpl := template.
		New("templates").
		Funcs(sprigin.TxtFuncMap()).
		Funcs(cfg.funcMap())
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
	templatePattern := filepath.Join(templatesDir, "*.tmpl")
	matches, _ := filepath.Glob(templatePattern)
	if len(matches) > 0 {
		parsedTemplatesWithLocalTemplates, err := parsedTemplates.ParseGlob(templatePattern)
		if err != nil {
			klog.V(1).ErrorS(err, "Error parsing user provided templates, ignoring user provided templates")
		} else {
			parsedTemplates = parsedTemplatesWithLocalTemplates
		}
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
