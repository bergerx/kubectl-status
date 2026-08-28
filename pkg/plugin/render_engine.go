package plugin

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"
	"time"

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
	// Add sprig-compatible aliases that templates depend on.
	// Native sprout uses different names; templates use sprig aliases.
	aliases := map[string]string{
		"int":        "toInt",
		"atoi":       "toInt",
		"int64":      "toInt64",
		"float64":    "toFloat64",
		"toDecimal":  "toOctal",
		"lower":      "toLower",
		"tolower":    "toLower",
		"lowercase":  "toLower",
		"upper":      "toUpper",
		"toupper":    "toUpper",
		"uppercase":  "toUpper",
		"title":      "toTitleCase",
		"titlecase":  "toTitleCase",
		"camelcase":  "toPascalCase",
		"snake":      "toSnakeCase",
		"snakecase":  "toSnakeCase",
		"kebab":      "toKebabCase",
		"kebabcase":  "toKebabCase",
		"swapcase":   "swapCase",
		"b64enc":     "base64Encode",
		"b64dec":     "base64Decode",
		"b32enc":     "base32Encode",
		"b32dec":     "base32Decode",
		"base":       "pathBase",
		"dir":        "pathDir",
		"ext":        "pathExt",
		"clean":      "pathClean",
		"isAbs":      "pathIsAbs",
		"ago":        "dateAgo",
		"toStrings":  "strSlice",
	}
	for alias, orig := range aliases {
		if fn, ok := sproutFuncMap[orig]; ok {
			sproutFuncMap[alias] = fn
		}
	}
	// Compat wrappers for functions that changed signature between sprig and sprout,
	// or that now error on nil where templates expect graceful handling.
	// These wrappers detect sprig-style call sites (dict first) vs sprout-style
	// (dict last via pipeline) and avoid hard errors that sprigin previously
	// swallowed via SafeCall.
	addCompatWrappers(sproutFuncMap)
	funcs := make(template.FuncMap, len(sproutFuncMap)+32)
	for name, fn := range sproutFuncMap {
		funcs[name] = fn
	}
	for name, fn := range cfg.funcMap() {
		funcs[name] = fn
	}
	return funcs
}

func isMap(v any) bool {
	if v == nil {
		return false
	}
	_, ok := v.(map[string]any)
	return ok
}

func isList(v any) bool {
	if v == nil {
		return false
	}
	rt := reflect.TypeOf(v)
	return rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	case string:
		var i int
		fmt.Sscan(n, &i)
		return i
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return int(rv.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return int(rv.Uint())
		case reflect.Float32, reflect.Float64:
			return int(rv.Float())
		}
		return 0
	}
}

func addCompatWrappers(m template.FuncMap) {
	// Capture original funcs before overriding
	origGet := m["get"]
	origHasKey := m["hasKey"]
	origSet := m["set"]
	origUnset := m["unset"]
	origPick := m["pick"]
	origOmit := m["omit"]
	origDig := m["dig"]
	origAppend := m["append"]
	origPrepend := m["prepend"]
	origSlice := m["slice"]
	origWithout := m["without"]
	origToDate := m["toDate"]
	origKindIs := m["kindIs"]
	origKindOf := m["kindOf"]

	// Helpers to call underlying via reflection with nil handling
	call := func(fn any, args ...any) (any, error) {
		v := reflect.ValueOf(fn)
		in := make([]reflect.Value, len(args))
		for i, a := range args {
			if a == nil {
				// Pass typed nil for map/string cases
				// Infer expected type from function signature if possible
				expectedType := v.Type().In(i)
				in[i] = reflect.Zero(expectedType)
			} else {
				in[i] = reflect.ValueOf(a)
				// Ensure assignable: if types mismatch, try conversion
				if !in[i].Type().AssignableTo(v.Type().In(i)) {
					if in[i].Type().ConvertibleTo(v.Type().In(i)) {
						in[i] = in[i].Convert(v.Type().In(i))
					}
				}
			}
		}
		out := v.Call(in)
		if len(out) == 2 {
			var err error
			if !out[1].IsNil() {
				err = out[1].Interface().(error)
			}
			return out[0].Interface(), err
		}
		if len(out) == 1 {
			return out[0].Interface(), nil
		}
		return nil, nil
	}

	// Typed wrappers avoid reflection zero-value pitfalls for maps
	if origGet != nil {
		if typed, ok := origGet.(func(string, map[string]any) (any, error)); ok {
			m["get"] = func(args ...any) (any, error) {
				if len(args) != 2 {
					return call(origGet, args...)
				}
				if isMap(args[0]) && !isMap(args[1]) {
					if key, ok := args[1].(string); ok {
						if dict, ok := args[0].(map[string]any); ok {
							return typed(key, dict)
						}
					}
					return call(origGet, args[1], args[0])
				}
				if key, ok := args[0].(string); ok {
					if dict, ok := args[1].(map[string]any); ok {
						return typed(key, dict)
					}
					if args[1] == nil {
						return typed(key, nil)
					}
				}
				return call(origGet, args...)
			}
		} else {
			m["get"] = func(args ...any) (any, error) {
				if len(args) != 2 {
					return call(origGet, args...)
				}
				if isMap(args[0]) && !isMap(args[1]) {
					return call(origGet, args[1], args[0])
				}
				return call(origGet, args...)
			}
		}
	}
	if origHasKey != nil {
		if typed, ok := origHasKey.(func(string, map[string]any) (bool, error)); ok {
			m["hasKey"] = func(args ...any) (any, error) {
				if len(args) != 2 {
					return call(origHasKey, args...)
				}
				if isMap(args[0]) && !isMap(args[1]) {
					if key, ok := args[1].(string); ok {
						if dict, ok := args[0].(map[string]any); ok {
							return typed(key, dict)
						}
					}
					return call(origHasKey, args[1], args[0])
				}
				if key, ok := args[0].(string); ok {
					switch v := args[1].(type) {
					case map[string]any:
						return typed(key, v)
					case nil:
						return typed(key, nil)
					default:
						// Handle typed nil map via reflection
						if args[1] == nil {
							return typed(key, nil)
						}
					}
				}
				return call(origHasKey, args...)
			}
		} else {
			m["hasKey"] = func(args ...any) (any, error) {
				if len(args) != 2 {
					return call(origHasKey, args...)
				}
				if isMap(args[0]) && !isMap(args[1]) {
					return call(origHasKey, args[1], args[0])
				}
				return call(origHasKey, args...)
			}
		}
	}
	if origSet != nil {
		m["set"] = func(args ...any) (any, error) {
			if len(args) != 3 {
				return call(origSet, args...)
			}
			if isMap(args[0]) && !isMap(args[2]) {
				return call(origSet, args[1], args[2], args[0])
			}
			return call(origSet, args...)
		}
	}
	if origUnset != nil {
		m["unset"] = func(args ...any) (any, error) {
			if len(args) != 2 {
				return call(origUnset, args...)
			}
			if isMap(args[0]) && !isMap(args[1]) {
				return call(origUnset, args[1], args[0])
			}
			return call(origUnset, args...)
		}
	}
	if origPick != nil {
		m["pick"] = func(args ...any) (any, error) {
			if len(args) < 2 {
				return call(origPick, args...)
			}
			if isMap(args[0]) && !isMap(args[len(args)-1]) {
				dict := args[0]
				keys := args[1:]
				newArgs := append(keys, dict)
				return call(origPick, newArgs...)
			}
			return call(origPick, args...)
		}
	}
	if origOmit != nil {
		m["omit"] = func(args ...any) (any, error) {
			if len(args) < 2 {
				return call(origOmit, args...)
			}
			if isMap(args[0]) && !isMap(args[len(args)-1]) {
				dict := args[0]
				keys := args[1:]
				newArgs := append(keys, dict)
				return call(origOmit, newArgs...)
			}
			return call(origOmit, args...)
		}
	}
	if origDig != nil {
		m["dig"] = func(args ...any) (any, error) {
			// Sprig signature: dig key1 key2 ... default dict
			// Sprout signature: dig keys... dict (no default, uses pipeline)
			// Detect sprig form by checking if we have at least 3 args, last is map, and not all keys are strings
			// Simpler: if len>=3 and last is map, treat second-last as default if not all keys are maps/lists
			if len(args) < 3 {
				return call(origDig, args...)
			}
			dict, ok := args[len(args)-1].(map[string]any)
			if !ok {
				return call(origDig, args...)
			}
			// All args except last are potential keys + default. If any key parsing fails due to non-string
			// (like int default), it's sprig form. We'll implement sprig dig directly.
			keysRaw := args[:len(args)-1]
			// Try to see if last of keysRaw is intended as default (non-string or always)
			// Sprigin sprigDig always treats second-last as default
			defaultVal := keysRaw[len(keysRaw)-1]
			keysPart := keysRaw[:len(keysRaw)-1]
			// Validate keysPart are all strings
			keys := make([]string, 0, len(keysPart))
			allStrings := true
			for _, k := range keysPart {
				if s, ok := k.(string); ok {
					keys = append(keys, s)
				} else {
					allStrings = false
					break
				}
			}
			if !allStrings || len(keysPart) == 0 {
				// Fall back to sprout native
				return call(origDig, args...)
			}
			// Sprig dig: look up keys sequentially, return default if not found
			current := any(dict)
			for i, key := range keys {
				mm, ok := current.(map[string]any)
				if !ok {
					return defaultVal, nil
				}
				val, exists := mm[key]
				if !exists {
					return defaultVal, nil
				}
				if i == len(keys)-1 {
					if val == nil {
						return defaultVal, nil
					}
					return val, nil
				}
				current = val
			}
			return defaultVal, nil
		}
	}
	if origAppend != nil {
		m["append"] = func(args ...any) (any, error) {
			if len(args) != 2 {
				return call(origAppend, args...)
			}
			if isList(args[0]) && !isList(args[1]) {
				// sprig: append(list, value) -> sprout: append(value, list)
				return call(origAppend, args[1], args[0])
			}
			return call(origAppend, args...)
		}
	}
	if origPrepend != nil {
		m["prepend"] = func(args ...any) (any, error) {
			if len(args) != 2 {
				return call(origPrepend, args...)
			}
			if isList(args[0]) && !isList(args[1]) {
				return call(origPrepend, args[1], args[0])
			}
			return call(origPrepend, args...)
		}
	}
	if origSlice != nil {
		m["slice"] = func(args ...any) (res any, err error) {
			defer func() {
				if r := recover(); r != nil {
					res = []any{}
					err = nil
				}
			}()
			if len(args) >= 2 && isList(args[0]) && !isList(args[len(args)-1]) {
				// Sprig order: slice list start [end]
				listVal := reflect.ValueOf(args[0])
				if listVal.Kind() != reflect.Slice && listVal.Kind() != reflect.Array {
					return call(origSlice, args...)
				}
				length := listVal.Len()
				start := toInt(args[1])
				end := length
				if len(args) > 2 {
					end = toInt(args[2])
				}
				if start < 0 {
					start = 0
				}
				if start > length {
					return []any{}, nil
				}
				if end < start {
					return []any{}, nil
				}
				if end > length {
					end = length
				}
				// Direct slice without calling sprout to avoid panic on edge cases
				return listVal.Slice(start, end).Interface(), nil
			}
			// Sprout order: slice indices..., list
			res, err = call(origSlice, args...)
			if err != nil && strings.Contains(err.Error(), "out of") {
				return []any{}, nil
			}
			return res, err
		}
	}
	if origWithout != nil {
		m["without"] = func(args ...any) (any, error) {
			if len(args) < 2 {
				return call(origWithout, args...)
			}
			if isList(args[0]) && !isList(args[len(args)-1]) {
				list := args[0]
				omit := args[1:]
				newArgs := append(omit, list)
				return call(origWithout, newArgs...)
			}
			return call(origWithout, args...)
		}
	}
	if origToDate != nil {
		m["toDate"] = func(args ...any) (any, error) {
			// toDate is called as: value | toDate layout  => sprout: toDate(layout, value)
			// We already handle pipeline correctly without swapping, but need to tolerate
			// nil/missing values that previously were swallowed.
			if len(args) != 2 {
				return call(origToDate, args...)
			}
			// Detect sprig order vs sprout order: sprig had value first? No, toDate is
			// conversion func with layout first in both, pipeline passes layout first via
			// template semantics, so no swap needed. Just handle nil gracefully.
			layout, ok1 := args[0].(string)
			if !ok1 {
				// If first arg is not string, it might be the value (sprig order swap attempt)
				// Try swapped
				if s, ok := args[1].(string); ok {
					layout = s
					// value is args[0]
					if args[0] == nil {
						return time.Time{}, nil
					}
					valStr := fmt.Sprint(args[0])
					if valStr == "" || valStr == "<nil>" || valStr == "<no value>" {
						return time.Time{}, nil
					}
					return call(origToDate, layout, valStr)
				}
				return time.Time{}, fmt.Errorf("invalid value; expected string")
			}
			val := args[1]
			if val == nil {
				return time.Time{}, nil
			}
			valStr, ok2 := val.(string)
			if !ok2 {
				valStr = fmt.Sprint(val)
			}
			if valStr == "" {
				// Match sprout error for empty string but avoid "invalid value" for nil
				return call(origToDate, layout, valStr)
			}
			return call(origToDate, layout, valStr)
		}
	}
	if origKindIs != nil {
		m["kindIs"] = func(args ...any) (any, error) {
			if len(args) != 2 {
				return call(origKindIs, args...)
			}
			// kindIs(target, value) - target is string, value is any
			// If value is nil, return false without error (sprigin swallowed)
			if args[1] == nil {
				return false, nil
			}
			// Also handle if nil is typed nil interface inside map
			if reflect.ValueOf(args[1]).Kind() == reflect.Ptr && reflect.ValueOf(args[1]).IsNil() {
				return false, nil
			}
			return call(origKindIs, args...)
		}
	}
	if origKindOf != nil {
		m["kindOf"] = func(args ...any) (any, error) {
			if len(args) != 1 {
				return call(origKindOf, args...)
			}
			if args[0] == nil {
				return "invalid", nil
			}
			return call(origKindOf, args...)
		}
	}
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
