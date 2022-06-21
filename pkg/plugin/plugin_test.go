package plugin

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalRun(t *testing.T) {
	t.Parallel()
	testArtifacts, err := filepath.Glob("../../tests/artifacts/*.yaml")
	if err != nil {
		t.Errorf("Run() error = %v", err)
	}
	for _, testArtifact := range testArtifacts {
		testName := strings.TrimSuffix(filepath.Base(testArtifact), filepath.Ext(testArtifact))
		t.Run(testName, func(t *testing.T) {
			o := NewOptions()
			o.RenderOptions.Local = true
			o.FileNameFlags.Filenames = &[]string{testArtifact}
			if err := Run(context.TODO(), o, nil, io.Discard); err != nil {
				t.Errorf("Run() error = %v during rendering %s", err, filepath.Base(testArtifact))
			}
		})
	}
}
