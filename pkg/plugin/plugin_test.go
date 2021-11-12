package plugin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	//klog.InitFlags(nil)
	//flag.Set("v", "5")
	//flag.Parse()
	testArtifacts, _ := filepath.Glob("../../test-artifacts/*.yaml")
	for _, testArtifact := range testArtifacts {
		testName := strings.TrimSuffix(filepath.Base(testArtifact), filepath.Ext(testArtifact))
		t.Run(testName, func(t *testing.T) {
			o := NewOptions()
			o.RenderOptions.Local = true
			o.FileNameFlags.Filenames = &[]string{testArtifact}
			if err := Run(o, nil); err != nil {
				t.Errorf("Run() error = %v", err)
			}
		})
	}
}
