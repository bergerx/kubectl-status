package plugin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactRender(t *testing.T) {
	testArtifacts, _ := filepath.Glob("../../test-artifacts/*yaml")
	for _, testArtifact := range testArtifacts {
		testName := strings.TrimSuffix(filepath.Base(testArtifact), filepath.Ext(testArtifact))
		t.Run(testName, func(t *testing.T) {
			_, err := renderFile(testArtifact)
			if err != nil {
				t.Errorf("%v", err)
			}
		})
	}
}
