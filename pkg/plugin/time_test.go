package plugin

import (
	"testing"

	"github.com/spf13/viper"
)

func TestAgoSuffix(t *testing.T) {
	v := viper.New()
	cfg := NewRenderConfig(v)
	v.Set("absolute-time", false)
	if got := cfg.agoSuffix(); got != " ago" {
		t.Errorf("agoSuffix() = %q, want %q", got, " ago")
	}
	v.Set("absolute-time", true)
	if got := cfg.agoSuffix(); got != "" {
		t.Errorf("agoSuffix() = %q, want empty string", got)
	}
}

func TestForOrSince(t *testing.T) {
	v := viper.New()
	cfg := NewRenderConfig(v)
	v.Set("absolute-time", false)
	if got := cfg.forOrSince(); got != "for" {
		t.Errorf("forOrSince() = %q, want %q", got, "for")
	}
	v.Set("absolute-time", true)
	if got := cfg.forOrSince(); got != "since" {
		t.Errorf("forOrSince() = %q, want %q", got, "since")
	}
}
