package plugin

import (
	"strings"
	"testing"
	"text/template"

	"github.com/spf13/viper"
)

// renderCompat renders tmpl against the production funcMap built by templateFuncs (the same
// native-sprout + compat-wrapper + alias map every template tree is parsed with), so these tests
// exercise the exact code paths templates run. cfg defaults to a fresh RenderConfig except where a
// test needs a specific config.
func renderCompat(t *testing.T, tmpl string, data any) string {
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

func TestTemplateFuncsSprigAliases(t *testing.T) {
	// Every sprig-name alias registered in templateFuncs must be present in the built funcMap and
	// resolve to the native sprout function it aliases (same underling fn pointer), so a template
	// written with a sprig name gets identical behavior.
	cfg := NewRenderConfig(viper.New())
	funcs := templateFuncs(cfg)

	aliases := map[string]string{
		"int":       "toInt",
		"atoi":      "toInt",
		"int64":     "toInt64",
		"float64":   "toFloat64",
		"toDecimal": "toOctal",
		"lower":     "toLower",
		"tolower":   "toLower",
		"lowercase": "toLower",
		"upper":     "toUpper",
		"toupper":   "toUpper",
		"uppercase": "toUpper",
		"title":     "toTitleCase",
		"titlecase": "toTitleCase",
		"camelcase": "toPascalCase",
		"snake":     "toSnakeCase",
		"snakecase": "toSnakeCase",
		"kebab":     "toKebabCase",
		"kebabcase": "toKebabCase",
		"swapcase":  "swapCase",
		"b64enc":    "base64Encode",
		"b64dec":    "base64Decode",
		"b32enc":    "base32Encode",
		"b32dec":    "base32Decode",
		"base":      "pathBase",
		"dir":       "pathDir",
		"ext":       "pathExt",
		"clean":     "pathClean",
		"isAbs":     "pathIsAbs",
		"ago":       "dateAgo",
		"toStrings": "strSlice",
	}
	for alias, target := range aliases {
		if _, ok := funcs[alias]; !ok {
			t.Errorf("expected sprig alias %q to be registered", alias)
			continue
		}
		if _, ok := funcs[target]; !ok {
			t.Errorf("native function %q (aliased by %q) not present", target, alias)
			continue
		}
	}
}

func TestCompatAliasesExecute(t *testing.T) {
	// Exercise a representative sample of the sprig-name aliases through the real funcMap to
	// confirm they behave like their native sprout counterparts (not just that they're present).
	cases := []struct {
		alias  string
		input  string
		want   string
	}{
		{"lower", "\"HELLO\"", "hello"},
		{"upper", "\"hello\"", "HELLO"},
		{"int64", "42", "42"},
		{"float64", "42", "42"},
		{"b64enc", "\"hi\"", "aGk="},
		{"toStrings", "\"a\"", "[a]"},
	}
	for _, tc := range cases {
		t.Run(tc.alias, func(t *testing.T) {
			if got := renderCompat(t, `{{ `+tc.input+` | `+tc.alias+` }}`, nil); got != tc.want {
				t.Errorf("%s(%s) = %q, want %q", tc.alias, tc.input, got, tc.want)
			}
		})
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

func TestCompatGet(t *testing.T) {
	data := map[string]any{"spec": map[string]any{"replicas": 3}}
	// native/sprout order: get key dict
	if got := renderCompat(t, `{{ get "spec" . }}`, data); got != "map[replicas:3]" {
		t.Errorf(`native-order get = %q`, got)
	}
	// sprig order: get dict key
	if got := renderCompat(t, `{{ get . "spec" }}`, data); got != "map[replicas:3]" {
		t.Errorf(`sprig-order get = %q`, got)
	}
	// missing key returns the empty string without erroring (sprigin swallowed)
	if got := renderCompat(t, `{{ get . "nope" }}`, data); got != "" {
		t.Errorf(`missing-key get = %q, want ""`, got)
	}
	// nil dict tolerated in either argument order
	if got := renderCompat(t, `{{ get . "replicas" }}`, nil); got != "" {
		t.Errorf(`nil-dict get = %q, want ""`, got)
	}
	if got := renderCompat(t, `{{ get "replicas" . }}`, nil); got != "" {
		t.Errorf(`nil-dict native get = %q, want ""`, got)
	}
}

func TestCompatHasKey(t *testing.T) {
	data := map[string]any{"spec": map[string]any{"replicas": 3}}
	// native/sprout order
	if got := renderCompat(t, `{{ hasKey "replicas" (get "spec" .) }}`, data); got != "true" {
		t.Errorf(`hasKey existing = %q`, got)
	}
	// sprig order: hasKey dict key
	if got := renderCompat(t, `{{ hasKey . "spec" }}`, data); got != "true" {
		t.Errorf(`sprig-order hasKey existing = %q`, got)
	}
	if got := renderCompat(t, `{{ hasKey . "missing" }}`, data); got != "false" {
		t.Errorf(`sprig-order hasKey missing = %q`, got)
	}
	// nil dict tolerated in either argument order -> false
	if got := renderCompat(t, `{{ hasKey . "spec" }}`, nil); got != "false" {
		t.Errorf(`nil-dict hasKey = %q`, got)
	}
	if got := renderCompat(t, `{{ hasKey "spec" . }}`, nil); got != "false" {
		t.Errorf(`nil-dict native hasKey = %q`, got)
	}
}

func TestCompatSet(t *testing.T) {
	// sprig order: set dict key value
	got := renderCompat(t, `{{ $d := dict "a" 1 }}{{ set $d "b" 2 }}{{ $d }}`, nil)
	if !strings.Contains(got, "b:2") {
		t.Errorf(`sprig-order set = %q, want dict with b:2`, got)
	}
}

func TestCompatUnset(t *testing.T) {
	// sprig order: unset dict key
	got := renderCompat(t, `{{ $d := dict "a" 1 "b" 2 }}{{ unset $d "a" }}{{ $d }}`, nil)
	if strings.Contains(got, "a:1") {
		t.Errorf(`sprig-order unset = %q, want key a removed`, got)
	}
}

func TestCompatPickOmit(t *testing.T) {
	// sprig order: pick dict key...
	got := renderCompat(t, `{{ $d := dict "a" 1 "b" 2 "c" 3 }}{{ pick $d "a" "c" }}`, nil)
	if !strings.Contains(got, "a:1") || !strings.Contains(got, "c:3") || strings.Contains(got, "b:2") {
		t.Errorf(`sprig-order pick = %q`, got)
	}
	// sprig order: omit dict key...
	got = renderCompat(t, `{{ $d := dict "a" 1 "b" 2 "c" 3 }}{{ omit $d "a" "c" }}`, nil)
	if strings.Contains(got, "a:1") || strings.Contains(got, "c:3") || !strings.Contains(got, "b:2") {
		t.Errorf(`sprig-order omit = %q`, got)
	}
}

func TestCompatDig(t *testing.T) {
	data := map[string]any{"install": map[string]any{"remediation": map[string]any{"retries": 5}}}
	// sprig order: dig key1 key2 default dict
	got := renderCompat(t, `{{ dig "install" "remediation" "retries" 0 (default dict .) }}`, data)
	if got != "5" {
		t.Errorf(`sprig-order dig found = %q, want 5`, got)
	}
	got = renderCompat(t, `{{ dig "install" "remediation" "nope" 0 (default dict .) }}`, data)
	if got != "0" {
		t.Errorf(`sprig-order dig default = %q, want 0`, got)
	}
	got = renderCompat(t, `{{ dig "missing" "path" 0 (default dict .) }}`, data)
	if got != "0" {
		t.Errorf(`sprig-order dig missing path default = %q, want 0`, got)
	}
}

func TestCompatAppendPrependSliceWithout(t *testing.T) {
	tmplAppend := `{{ $l := list 1 2 }}{{ append $l 3 }}`
	if got := renderCompat(t, tmplAppend, nil); !strings.Contains(got, "3") || !strings.Contains(got, "1") {
		t.Errorf(`sprig-order append = %q`, got)
	}
	// native order: append value list / append value | prepend ...
	if got := renderCompat(t, `{{ $l := list 1 2 }}{{ append 3 $l }}`, nil); !strings.Contains(got, "3") {
		t.Errorf(`native-order append = %q`, got)
	}
	tmplPrepend := `{{ $l := list 2 3 }}{{ prepend $l 1 }}`
	if got := renderCompat(t, tmplPrepend, nil); !strings.Contains(got, "1") {
		t.Errorf(`sprig-order prepend = %q`, got)
	}
	// native order (pipeline): slice 1 3 list
	tmplSlice := `{{ list "a" "b" "c" "d" | slice 1 3 }}`
	if got := renderCompat(t, tmplSlice, nil); got != "[b c]" {
		t.Errorf(`slice 1 3 = %q, want [b c]`, got)
	}
	// sprig order: slice list 1 3 (wrapper's manual branch)
	tmplSliceSprig := `{{ slice (list "a" "b" "c" "d") 1 3 }}`
	if got := renderCompat(t, tmplSliceSprig, nil); got != "[b c]" {
		t.Errorf(`sprig-order slice = %q, want [b c]`, got)
	}
	// out-of-range slice: sprout's bounds error is swallowed into an empty list (as
	// sprigin's SafeCall did) rather than aborting the template
	tmplSliceBad := `{{ list "a" "b" | slice 5 9 }}`
	if got := renderCompat(t, tmplSliceBad, nil); !strings.Contains(got, "[]") {
		t.Errorf(`out-of-range slice = %q, want empty list`, got)
	}
	// sprig order: without list key...
	tmplWithout := `{{ list "a" "b" "c" | without "b" }}`
	if got := renderCompat(t, tmplWithout, nil); !strings.Contains(got, "[a c]") {
		t.Errorf(`without = %q, want [a c]`, got)
	}
}

func TestCompatPickOmitNative(t *testing.T) {
	// native order: pick keys... dict, omit keys... dict
	tmplPick := `{{ $d := dict "a" 1 "b" 2 "c" 3 }}{{ pick "a" "c" $d }}`
	if got := renderCompat(t, tmplPick, nil); !strings.Contains(got, "a:1") || !strings.Contains(got, "c:3") || strings.Contains(got, "b:2") {
		t.Errorf(`native-order pick = %q`, got)
	}
	tmplOmit := `{{ $d := dict "a" 1 "b" 2 "c" 3 }}{{ omit "a" "c" $d }}`
	if got := renderCompat(t, tmplOmit, nil); strings.Contains(got, "a:1") || strings.Contains(got, "c:3") || !strings.Contains(got, "b:2") {
		t.Errorf(`native-order omit = %q`, got)
	}
}

func TestCompatToDate(t *testing.T) {
	// normal layout-first form
	if got := renderCompat(t, `{{ "2026-01-02" | toDate "2006-01-02" }}`, nil); !strings.Contains(got, "2026-01-02") {
		t.Errorf(`toDate parse = %q`, got)
	}
	// nil value tolerated (sprigin swallowed missing dates) -> zero time, no error
	toDate := templateFuncs(NewRenderConfig(viper.New()))["toDate"].(func(args ...any) (any, error))
	if _, err := toDate("2006-01-02", nil); err != nil {
		t.Errorf(`toDate nil: unexpected error %v`, err)
	}
	// empty string still surfaces sprout's own parse error rather than silently swallowing
	if _, err := toDate("2006-01-02", ""); err == nil {
		t.Errorf("toDate empty string: expected error, got nil")
	}
}

func TestCompatKindIsKindOf(t *testing.T) {
	// kindIs with a non-nil value
	got := renderCompat(t, `{{ kindIs "string" "hello" }}`, nil)
	if got != "true" {
		t.Errorf(`kindIs string = %q`, got)
	}
	// kindIs nil-safe -> false
	if got := renderCompat(t, `{{ kindIs "invalid" .missing }}`, map[string]any{}); got != "false" {
		t.Errorf(`kindIs nil = %q, want false`, got)
	}
	// kindOf with a value
	got = renderCompat(t, `{{ kindOf 42 }}`, nil)
	if got != "int" {
		t.Errorf(`kindOf int = %q`, got)
	}
	// kindOf nil -> "invalid"
	if got := renderCompat(t, `{{ kindOf .missing }}`, map[string]any{}); got != "invalid" {
		t.Errorf(`kindOf nil = %q, want invalid`, got)
	}
}

// TestToInt covers the numeric coercion helper used by the slice wrapper's sprig-order branch
// (and aliases like int/atoi): it must accept the numeric types text/template pipelines can hand
// it and fall back to 0 for anything uncoercible, matching sprig's lenient behavior.
func TestToInt(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int
	}{
		{"int", 7, 7},
		{"int64", int64(7), 7},
		{"int32", int32(7), 7},
		{"float64", 7.0, 7},
		{"float64 rounded down", 7.9, 7},
		{"float32", float32(7), 7},
		{"numeric string", "42", 42},
		{"garbage string", "abc", 0},
		{"uint", uint(7), 7},
		{"uint64", uint64(7), 7},
		{"int8 via reflect", int8(7), 7},
		{"bool falls back to zero", true, 0},
		{"nil falls back to zero", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toInt(tt.in); got != tt.want {
				t.Errorf("toInt(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestCompatBadArityDelegates(t *testing.T) {
	// Wrappers must fall through to the original native function when the argument count doesn't
	// match the sprig form, letting sprout return its own clear error rather than silently
	// producing wrong output. Render and assert an error is surfaced (not a panic).
	cfg := NewRenderConfig(viper.New())
	funcs := templateFuncs(cfg)
	tmpl := template.Must(template.New("t").Funcs(funcs).Parse(`{{ get "only-one-key" }}`))
	if err := tmpl.Execute(&strings.Builder{}, nil); err == nil {
		t.Fatal("expected arity error from get with a single argument")
	}
}

// TestCompatCallDegeneratePaths drives the reflect-based call helper through the branches a
// normal wrapper invocation never reaches: a typed-nil argument (hit via append's fall-through),
// an argument count larger than the wrapped function's fixed parameter count, and an argument
// whose type is neither assignable nor convertible to the wrapped parameter type. These used to
// panic through reflect; they must now surface a normal error and let the template fail gracefully
// instead of aborting the process.
func TestCompatCallDegeneratePaths(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	funcs := templateFuncs(cfg)
	run := func(t *testing.T, tmplStr string) (string, error) {
		t.Helper()
		parsed, err := template.New("t").Funcs(funcs).Parse(tmplStr)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		var b strings.Builder
		err = parsed.Execute(&b, nil)
		return b.String(), err
	}

	t.Run("typed nil argument", func(t *testing.T) {
		// append nil list: nil reaches call's a==nil branch (the wrapper's sprig-order
		// check fails because a nil first arg is not a list, so it falls through to call).
		_, err := run(t, `{{ $l := list 1 2 }}{{ append nil $l }}`)
		if err != nil {
			t.Fatalf("append with nil value: unexpected error %v", err)
		}
	})

	t.Run("wrong argument count", func(t *testing.T) {
		// append with three arguments exceeds its two fixed parameters after every one of
		// those is satisfied (all assignable), so it must exercise call's arity branch and
		// return an error, not panic.
		_, err := run(t, `{{ append 1 2 3 }}`)
		if err == nil {
			t.Fatal("expected arity error from append with three arguments")
		}
	})

	t.Run("incompatible argument type", func(t *testing.T) {
		// get "key" 123: the second arg (int) is not assignable/convertible to the
		// map[string]any parameter; must error, not panic.
		_, err := run(t, `{{ get "key" 123 }}`)
		if err == nil {
			t.Fatal("expected type error from get with a non-map second argument")
		}
	})
}