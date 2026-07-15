package plugin

import (
	"testing"

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
