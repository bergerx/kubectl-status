package plugin

import (
	"testing"
)

func TestGetTemplate(t *testing.T) {
	t.Parallel()
	t.Run("templates are parsable", func(t *testing.T) {
		_, err := getTemplate()
		if err != nil {
			t.Errorf("getTemplate() error = %v", err)
			return
		}
	})
}
