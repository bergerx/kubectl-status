package calicoselector

import "testing"

func TestParseAndEvaluate(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		labels   map[string]string
		want     bool
	}{
		{"empty selector matches everything", "", map[string]string{}, true},
		{"all() matches everything", "all()", map[string]string{}, true},
		{"global() matches everything", "global()", map[string]string{}, true},
		{"eq matches", "color == 'red'", map[string]string{"color": "red"}, true},
		{"eq mismatches", "color == 'red'", map[string]string{"color": "blue"}, false},
		{"eq absent label", "color == 'red'", map[string]string{}, false},
		{"ne matches when different", "color != 'red'", map[string]string{"color": "blue"}, true},
		{"ne matches when absent", "color != 'red'", map[string]string{}, true},
		{"ne false when equal", "color != 'red'", map[string]string{"color": "red"}, false},
		{"has present", "has(color)", map[string]string{"color": "red"}, true},
		{"has absent", "has(color)", map[string]string{}, false},
		{"not has present", "!has(color)", map[string]string{"color": "red"}, false},
		{"not has absent", "!has(color)", map[string]string{}, true},
		{"in set matches", "color in {'red', 'blue'}", map[string]string{"color": "blue"}, true},
		{"in set mismatches", "color in {'red', 'blue'}", map[string]string{"color": "green"}, false},
		{"in set absent label", "color in {'red', 'blue'}", map[string]string{}, false},
		{"not in set matches", "color not in {'red', 'blue'}", map[string]string{"color": "green"}, true},
		{"not in set absent label", "color not in {'red', 'blue'}", map[string]string{}, true},
		{"not in set false when present in set", "color not in {'red', 'blue'}", map[string]string{"color": "red"}, false},
		{"contains", "color contains 'ed'", map[string]string{"color": "red"}, true},
		{"starts with", "color starts with 're'", map[string]string{"color": "red"}, true},
		{"ends with", "color ends with 'ed'", map[string]string{"color": "red"}, true},
		{"and both true", "has(color) && has(tier)", map[string]string{"color": "red", "tier": "backend"}, true},
		{"and one false", "has(color) && has(tier)", map[string]string{"color": "red"}, false},
		{"or one true", "has(color) || has(tier)", map[string]string{"tier": "backend"}, true},
		{"or both false", "has(color) || has(tier)", map[string]string{}, false},
		{"negated group", "!(color == 'red')", map[string]string{"color": "blue"}, true},
		{"parens change precedence", "(has(a) || has(b)) && has(c)", map[string]string{"b": "1", "c": "1"}, true},
		{"double quoted strings", `color == "red"`, map[string]string{"color": "red"}, true},
		{"whitespace tolerant", "  has( color )  ", map[string]string{"color": "red"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := Parse(tt.selector)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tt.selector, err)
			}
			if got := sel.Evaluate(tt.labels); got != tt.want {
				t.Errorf("Parse(%q).Evaluate(%v) = %v, want %v", tt.selector, tt.labels, got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []string{
		"has(",
		"color ==",
		"color == 'red",
		"color in {'red'",
		"&&",
		"color == 'red' extra",
		"???",
	}
	for _, expr := range tests {
		t.Run(expr, func(t *testing.T) {
			if _, err := Parse(expr); err == nil {
				t.Errorf("Parse(%q) expected an error, got nil", expr)
			}
		})
	}
}

func TestString(t *testing.T) {
	sel, err := Parse("  has(color)  ")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sel.String(), "has(color)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	sel, err = Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sel.String(), "all()"; got != want {
		t.Errorf("String() for empty selector = %q, want %q", got, want)
	}
}

func TestNilSelector(t *testing.T) {
	var sel *Selector
	if sel.Evaluate(map[string]string{"a": "b"}) {
		t.Error("nil Selector should never match")
	}
	if sel.String() != "" {
		t.Error("nil Selector.String() should be empty")
	}
}
