package plugin

import (
	"strings"
	"testing"
	"text/template"

	"github.com/spf13/viper"
)

// renderWith renders tmpl against the production funcMap built by templateFuncs (the same
// native-sprout funcMap every template tree is parsed with), so these tests exercise the exact
// function layer templates run against.
func renderWith(t *testing.T, tmpl string, data any) string {
	t.Helper()
	cfg := NewRenderConfig(viper.New())
	funcs := templateFuncs(cfg)
	parsed, err := template.New("test").Funcs(funcs).Parse(tmpl)
	if err != nil {
		t.Fatalf("Parse(%s): %v", tmpl, err)
	}
	var b strings.Builder
	if err := parsed.Execute(&b, data); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return b.String()
}

func TestTemplateFuncsNativeFunctionsRegistered(t *testing.T) {
	// Every native sprout function the templates depend on after the sprig-to-sprout migration
	// must be present in the built funcMap.
	cfg := NewRenderConfig(viper.New())
	funcs := templateFuncs(cfg)
	for _, name := range []string{
		"toInt", "toInt64", "toFloat64", "toLower", "toUpper", "toTitleCase", "toDate",
		"toString", "strSlice", "get", "hasKey", "set", "unset", "pick", "omit", "dig",
		"append", "prepend", "slice", "without", "kindIs", "kindOf", "default", "dict",
		"list", "keys", "join", "ternary", "coalesce", "empty",
	} {
		if _, ok := funcs[name]; !ok {
			t.Errorf("native function %q not registered", name)
		}
	}
}

func TestTemplateFuncsNoSprigAliases(t *testing.T) {
	// The sprig compatibility aliases that described the pre-migration funcMap must no longer be
	// registered; templates now spell these natively (toInt, toLower, ...).
	cfg := NewRenderConfig(viper.New())
	funcs := templateFuncs(cfg)
	for _, name := range []string{
		"int", "atoi", "int64", "float64", "toDecimal", "lower", "tolower", "lowercase",
		"upper", "toupper", "uppercase", "title", "titlecase", "camelcase", "snake",
		"snakecase", "kebab", "kebabcase", "swapcase", "b64enc", "b64dec", "b32enc",
		"b32dec", "base", "dir", "ext", "clean", "isAbs", "ago", "toStrings",
	} {
		if _, ok := funcs[name]; ok {
			t.Errorf("sprig alias %q must not be registered after migration", name)
		}
	}
}

func TestTemplateFuncsRemovesEnvFunctions(t *testing.T) {
	// The hermetic group exposes env/expandEnv/expandenv which would let a stray user template
	// leak the process environment (e.g. cloud credentials) into rendered output; templateFuncs
	// must strip them.
	cfg := NewRenderConfig(viper.New())
	funcs := templateFuncs(cfg)
	for _, name := range []string{"env", "expandEnv", "expandenv"} {
		if _, ok := funcs[name]; ok {
			t.Errorf("function %q should have been removed from the funcMap", name)
		}
	}
}

func TestNativeCastsAndCase(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		want string
	}{
		{"toInt parses string", `{{ "42" | toInt }}`, "42"},
		{"toInt64", `{{ 7 | toInt64 }}`, "7"},
		{"toFloat64", `{{ 42 | toFloat64 }}`, "42"},
		{"toLower", `{{ "HELLO" | toLower }}`, "hello"},
		{"toUpper", `{{ "hello" | toUpper }}`, "HELLO"},
		{"toTitleCase", `{{ "hello world" | toTitleCase }}`, "Hello World"},
		{"toString of int", `{{ 42 | toString }}`, "42"},
		{"strSlice of strings", `{{ "a,b,c" | toString | splitList "," | strSlice }}`, "[a b c]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderWith(t, tc.tmpl, nil); got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestNativeMapFunctions(t *testing.T) {
	data := map[string]any{"spec": map[string]any{"replicas": 3}}
	// get key dict
	if got := renderWith(t, `{{ get "spec" . }}`, data); got != "map[replicas:3]" {
		t.Errorf("get existing = %q, want map[replicas:3]", got)
	}
	// get missing key -> empty string (native sprout Get returns "" for a missing key)
	if got := renderWith(t, `{{ get "nope" . }}`, data); got != "" {
		t.Errorf("get missing = %q, want empty string", got)
	}
	// hasKey key dict
	if got := renderWith(t, `{{ hasKey "spec" . }}`, data); got != "true" {
		t.Errorf("hasKey existing = %q", got)
	}
	if got := renderWith(t, `{{ hasKey "nope" . }}`, data); got != "false" {
		t.Errorf("hasKey missing = %q", got)
	}
	// set key value dict (mutates and returns the dict)
	if got := renderWith(t, `{{ set "b" 2 (dict "a" 1) }}`, nil); got != "map[a:1 b:2]" {
		t.Errorf("set = %q, want map[a:1 b:2]", got)
	}
	// unset key dict
	if got := renderWith(t, `{{ unset "a" (dict "a" 1 "b" 2) }}`, nil); got != "map[b:2]" {
		t.Errorf("unset = %q, want map[b:2]", got)
	}
	// pick keys... dict
	if got := renderWith(t, `{{ pick "a" "c" (dict "a" 1 "b" 2 "c" 3) }}`, nil); got != "map[a:1 c:3]" {
		t.Errorf("pick = %q, want map[a:1 c:3]", got)
	}
	// omit keys... dict
	if got := renderWith(t, `{{ omit "a" "c" (dict "a" 1 "b" 2 "c" 3) }}`, nil); got != "map[b:2]" {
		t.Errorf("omit = %q, want map[b:2]", got)
	}
}

func TestNativeDig(t *testing.T) {
	// dig keys... dict; missing intermediate/mid path returns nil (no error), so the caller
	// applies default itself.
	data := map[string]any{"install": map[string]any{"remediation": map[string]any{"retries": 5}}}
	if got := renderWith(t, `{{ get "install" . | dig "remediation" "retries" }}`, data); got != "5" {
		t.Errorf("dig found = %q, want 5", got)
	}
	if got := renderWith(t, `{{ dig "install" "remediation" "nope" . | default 0 }}`, data); got != "0" {
		t.Errorf("dig missing leaf with default = %q, want 0", got)
	}
	if got := renderWith(t, `{{ dig "install" "nope" . | default "fallback" }}`, data); got != "fallback" {
		t.Errorf("dig missing mid path with default = %q, want fallback", got)
	}
}

func TestNativeSliceFunctions(t *testing.T) {
	// append value list (native order)
	if got := renderWith(t, `{{ append 3 (list 1 2) }}`, nil); got != "[1 2 3]" {
		t.Errorf("append = %q, want [1 2 3]", got)
	}
	// prepend value list
	if got := renderWith(t, `{{ prepend 1 (list 2 3) }}`, nil); got != "[1 2 3]" {
		t.Errorf("prepend = %q, want [1 2 3]", got)
	}
	// slice start end list
	if got := renderWith(t, `{{ slice 1 3 (list "a" "b" "c" "d") }}`, nil); got != "[b c]" {
		t.Errorf("slice = %q, want [b c]", got)
	}
	// without elems... list
	if got := renderWith(t, `{{ without "b" (list "a" "b" "c") }}`, nil); got != "[a c]" {
		t.Errorf("without = %q, want [a c]", got)
	}
}

func TestNativeToDate(t *testing.T) {
	// layout-first, value last; identical to the pre-migration toDate ordering
	if got := renderWith(t, `{{ "2026-01-02T15:04:05Z" | toDate "2006-01-02T15:04:05Z" }}`, nil); got != "2026-01-02 15:04:05 +0000 UTC" {
		t.Errorf("toDate = %q, want 2026-01-02 15:04:05 +0000 UTC", got)
	}
}

func TestNativeKindFunctions(t *testing.T) {
	if got := renderWith(t, `{{ kindIs "string" "hello" }}`, nil); got != "true" {
		t.Errorf("kindIs string = %q, want true", got)
	}
	if got := renderWith(t, `{{ kindIs "map" "hello" }}`, nil); got != "false" {
		t.Errorf("kindIs map on string = %q, want false", got)
	}
	if got := renderWith(t, `{{ kindOf 42 }}`, nil); got != "int" {
		t.Errorf("kindOf int = %q, want int", got)
	}
}