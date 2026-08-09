package plugin

import (
	"testing"

	"github.com/spf13/viper"
)

func TestColorAgoAbsolute(t *testing.T) {
	v := viper.New()
	cfg := NewRenderConfig(v)
	v.Set("absolute-time", true)
	input := "2006-01-02T15:04:05Z"
	if got := cfg.colorAgo(input); got != input {
		t.Errorf("colorAgo(%q) = %q, want %q", input, got, input)
	}
}
