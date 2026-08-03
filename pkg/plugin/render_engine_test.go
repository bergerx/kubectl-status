package plugin

import (
	"testing"
	"text/template"

	"github.com/spf13/viper"
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
	tmpl := template.Must(template.New("templates").Parse(
		`{{ define "Gateway" }}{{ end }}{{ define "Gateway.networking.istio.io" }}{{ end }}`))

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
			if got := findTemplateName(*tmpl, tt.kind, tt.group); got != tt.want {
				t.Errorf("findTemplateName(%q, %q) = %q, want %q", tt.kind, tt.group, got, tt.want)
			}
		})
	}
}
