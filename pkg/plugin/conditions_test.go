package plugin

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestIsStatusConditionHealthyUserProvidedTypes(t *testing.T) {
	resetUserAbnormalTrueConditionTypes := func() {
		userAbnormalTrueConditionTypesOnce = sync.Once{}
		userAbnormalTrueConditionTypes = userAbnormalTrueConditionTypeMatcher{}
	}
	t.Cleanup(resetUserAbnormalTrueConditionTypes)

	t.Run("no user file present", func(t *testing.T) {
		resetUserAbnormalTrueConditionTypes()
		t.Setenv("HOME", t.TempDir())
		condition := map[string]interface{}{"type": "CustomAbnormalTrue", "status": "True"}
		if !isStatusConditionHealthy(condition) {
			t.Errorf("expected condition type unknown to kubectl-status to follow the default 'True is healthy' polarity")
		}
	})

	t.Run("user provided condition type overrides default polarity", func(t *testing.T) {
		resetUserAbnormalTrueConditionTypes()
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".kubectl-status"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "# comment\n\nCustomAbnormalTrue\nAnotherOne\n"
		path := filepath.Join(home, ".kubectl-status", "abnormal-true-condition-types")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)

		trueCondition := map[string]interface{}{"type": "CustomAbnormalTrue", "status": "True"}
		if isStatusConditionHealthy(trueCondition) {
			t.Errorf("expected user provided abnormal-true condition type with status True to be unhealthy")
		}
		falseCondition := map[string]interface{}{"type": "CustomAbnormalTrue", "status": "False"}
		if !isStatusConditionHealthy(falseCondition) {
			t.Errorf("expected user provided abnormal-true condition type with status False to be healthy")
		}
	})

	t.Run("user provided suffix and prefix patterns", func(t *testing.T) {
		resetUserAbnormalTrueConditionTypes()
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".kubectl-status"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "*Problematic\nUnhealthy*\n"
		path := filepath.Join(home, ".kubectl-status", "abnormal-true-condition-types")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)

		suffixMatch := map[string]interface{}{"type": "DiskProblematic", "status": "True"}
		if isStatusConditionHealthy(suffixMatch) {
			t.Errorf("expected type matching the '*Problematic' suffix pattern with status True to be unhealthy")
		}
		prefixMatch := map[string]interface{}{"type": "UnhealthyDisk", "status": "True"}
		if isStatusConditionHealthy(prefixMatch) {
			t.Errorf("expected type matching the 'Unhealthy*' prefix pattern with status True to be unhealthy")
		}
		noMatch := map[string]interface{}{"type": "SomethingElse", "status": "True"}
		if !isStatusConditionHealthy(noMatch) {
			t.Errorf("expected type not matching any user pattern to keep the default 'True is healthy' polarity")
		}
	})
}
