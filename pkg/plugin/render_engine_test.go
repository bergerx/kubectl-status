package plugin

import (
	"testing"
)

func Test_getTemplate(t *testing.T) {
	t.Run("templates are parsable", func(t *testing.T) {
		_, err := getTemplate()
		if err != nil {
			t.Errorf("getTemplate() error = %v", err)
			return
		}
	})
}
