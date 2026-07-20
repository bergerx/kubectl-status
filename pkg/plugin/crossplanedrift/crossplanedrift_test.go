package crossplanedrift

import (
	"strings"
	"testing"
)

func driftPaths(entries []Entry) []string {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	return paths
}

func TestDiff_NotEligible(t *testing.T) {
	tests := []struct {
		name        string
		forProvider map[string]interface{}
		atProvider  map[string]interface{}
	}{
		{"both nil", nil, nil},
		{"empty forProvider", map[string]interface{}{}, map[string]interface{}{"region": "eu-west-1"}},
		{"empty atProvider", map[string]interface{}{"region": "eu-west-1"}, map[string]interface{}{}},
		{"nil atProvider", map[string]interface{}{"region": "eu-west-1"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Diff(tt.forProvider, tt.atProvider)
			if result.Eligible {
				t.Errorf("Diff() Eligible = true, want false")
			}
		})
	}
}

func TestDiff_EqualScalarAndNestedMap(t *testing.T) {
	forProvider := map[string]interface{}{
		"region": "eu-west-1",
		"tags":   map[string]interface{}{"environment": "production"},
	}
	atProvider := map[string]interface{}{
		"region": "eu-west-1",
		"tags":   map[string]interface{}{"environment": "production"},
		"arn":    "arn:aws:ec2:...", // extra observed-only field
	}
	result := Diff(forProvider, atProvider)
	if !result.Eligible {
		t.Fatalf("Diff() Eligible = false, want true")
	}
	if len(result.DriftEntries) != 0 {
		t.Errorf("DriftEntries = %v, want none", result.DriftEntries)
	}
	if result.InSyncCount != 2 {
		t.Errorf("InSyncCount = %d, want 2", result.InSyncCount)
	}
}

func TestDiff_DifferingScalar(t *testing.T) {
	forProvider := map[string]interface{}{"region": "eu-west-1"}
	atProvider := map[string]interface{}{"region": "us-east-1"}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 1 {
		t.Fatalf("DriftEntries = %v, want 1 entry", result.DriftEntries)
	}
	e := result.DriftEntries[0]
	if e.Path != "region" || e.Desired != "eu-west-1" || e.Observed != "us-east-1" {
		t.Errorf("DriftEntries[0] = %+v, want region eu-west-1 -> us-east-1", e)
	}
}

func TestUnifiedDiff_NoDrift(t *testing.T) {
	result := Diff(map[string]interface{}{"region": "eu-west-1"}, map[string]interface{}{"region": "eu-west-1"})
	if got := result.UnifiedDiff(); got != "" {
		t.Errorf("UnifiedDiff() = %q, want empty for no drift", got)
	}
}

func TestUnifiedDiff_NestedPathAndScalar(t *testing.T) {
	forProvider := map[string]interface{}{
		"region": "eu-west-1",
		"tags":   map[string]interface{}{"environment": "production"},
	}
	atProvider := map[string]interface{}{
		"region": "eu-west-1",
		"tags":   map[string]interface{}{"environment": "staging"},
	}
	result := Diff(forProvider, atProvider)
	got := result.UnifiedDiff()
	for _, want := range []string{"--- spec.forProvider", "+++ status.atProvider", "-  environment: production", "+  environment: staging"} {
		if !strings.Contains(got, want) {
			t.Errorf("UnifiedDiff() = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "region") {
		t.Errorf("UnifiedDiff() = %q, want the in-sync region field left out of the diff", got)
	}
}

func TestUnifiedDiff_ExcludesRedactedFields(t *testing.T) {
	forProvider := map[string]interface{}{"dbPassword": "s3cr3t-old", "region": "eu-west-1"}
	atProvider := map[string]interface{}{"dbPassword": "s3cr3t-new", "region": "us-east-1"}
	result := Diff(forProvider, atProvider)
	got := result.UnifiedDiff()
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "REDACTED") {
		t.Errorf("UnifiedDiff() = %q, must not leak or even mention redacted fields", got)
	}
	if !strings.Contains(got, "-region: eu-west-1") || !strings.Contains(got, "+region: us-east-1") {
		t.Errorf("UnifiedDiff() = %q, want the non-redacted field's diff", got)
	}
	if got := result.RedactedPaths(); len(got) != 1 || got[0] != "dbPassword" {
		t.Errorf("RedactedPaths() = %v, want [dbPassword]", got)
	}
}

func TestUnifiedDiff_OnlyRedactedFieldsDriftedYieldsNoDiff(t *testing.T) {
	forProvider := map[string]interface{}{"dbPassword": "s3cr3t-old"}
	atProvider := map[string]interface{}{"dbPassword": "s3cr3t-new"}
	result := Diff(forProvider, atProvider)
	if got := result.UnifiedDiff(); got != "" {
		t.Errorf("UnifiedDiff() = %q, want empty when every drifted field is redacted", got)
	}
	if got := result.RedactedPaths(); len(got) != 1 || got[0] != "dbPassword" {
		t.Errorf("RedactedPaths() = %v, want [dbPassword]", got)
	}
}

func TestDiff_ExtraObservedFieldsNoDrift(t *testing.T) {
	forProvider := map[string]interface{}{"region": "eu-west-1"}
	atProvider := map[string]interface{}{"region": "eu-west-1", "vpcId": "vpc-123", "createdAt": "2024-01-01"}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 0 {
		t.Errorf("DriftEntries = %v, want none", result.DriftEntries)
	}
	if result.ObservedOnlyCount != 2 {
		t.Errorf("ObservedOnlyCount = %d, want 2", result.ObservedOnlyCount)
	}
}

func TestDiff_ConfiguredFieldAbsentFromObserved(t *testing.T) {
	forProvider := map[string]interface{}{"region": "eu-west-1", "cidrBlock": "10.0.0.0/16"}
	atProvider := map[string]interface{}{"region": "eu-west-1"}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 0 {
		t.Errorf("DriftEntries = %v, want none (absent path is NotObserved, not Drift)", result.DriftEntries)
	}
	if result.NotObservedCount != 1 {
		t.Errorf("NotObservedCount = %d, want 1", result.NotObservedCount)
	}
}

func TestDiff_MapKeyOrderingDoesNotMatter(t *testing.T) {
	forProvider := map[string]interface{}{
		"tags": map[string]interface{}{"a": "1", "b": "2", "c": "3"},
	}
	atProvider := map[string]interface{}{
		"tags": map[string]interface{}{"c": "3", "a": "1", "b": "2"},
	}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 0 {
		t.Errorf("DriftEntries = %v, want none", result.DriftEntries)
	}
}

func TestDiff_ScalarListOrderingIsSetCompared(t *testing.T) {
	forProvider := map[string]interface{}{"zones": []interface{}{"a", "b", "c"}}
	atProvider := map[string]interface{}{"zones": []interface{}{"c", "b", "a"}}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 0 {
		t.Errorf("DriftEntries = %v, want none (scalar list without duplicates compares as a set)", result.DriftEntries)
	}
}

func TestDiff_ScalarListSetComparisonNormalizesNumbers(t *testing.T) {
	forProvider := map[string]interface{}{"ports": []interface{}{int64(1), int64(2)}}
	atProvider := map[string]interface{}{"ports": []interface{}{float64(2), float64(1)}}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 0 {
		t.Errorf("DriftEntries = %v, want none (int64/float64 encodings of the same numbers must match in set comparison)", result.DriftEntries)
	}
}

func TestDiff_ScalarListWithDuplicatesComparesOrdered(t *testing.T) {
	forProvider := map[string]interface{}{"zones": []interface{}{"a", "a", "b"}}
	atProvider := map[string]interface{}{"zones": []interface{}{"a", "b", "a"}}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 1 {
		t.Fatalf("DriftEntries = %v, want 1 entry (duplicates disable set semantics)", result.DriftEntries)
	}
}

func TestDiff_ObjectListOrderingIsPositional(t *testing.T) {
	forProvider := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"port": int64(80)},
			map[string]interface{}{"port": int64(443)},
		},
	}
	atProvider := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"port": int64(443)},
			map[string]interface{}{"port": int64(80)},
		},
	}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 1 {
		t.Fatalf("DriftEntries = %v, want 1 entry (object lists compare positionally, not by key matching)", result.DriftEntries)
	}
	if result.DriftEntries[0].Path != "rules" {
		t.Errorf("DriftEntries[0].Path = %q, want %q", result.DriftEntries[0].Path, "rules")
	}
}

func TestDiff_SensitivePathIsRedacted(t *testing.T) {
	forProvider := map[string]interface{}{
		"auth": map[string]interface{}{"password": "hunter2", "accessKey": "AKIA..."},
	}
	atProvider := map[string]interface{}{
		"auth": map[string]interface{}{"password": "different", "accessKey": "AKIB..."},
	}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 2 {
		t.Fatalf("DriftEntries = %v, want 2 entries", result.DriftEntries)
	}
	for _, e := range result.DriftEntries {
		if !e.Redacted || e.Desired != "REDACTED" || e.Observed != "REDACTED" {
			t.Errorf("entry %q not redacted: %+v", e.Path, e)
		}
	}
}

func TestDiff_TruncatesToBoundDeterministically(t *testing.T) {
	forProvider := map[string]interface{}{}
	atProvider := map[string]interface{}{}
	for i := 0; i < 25; i++ {
		key := "field" + string(rune('a'+i))
		forProvider[key] = "desired"
		atProvider[key] = "observed"
	}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != maxDriftEntries {
		t.Fatalf("len(DriftEntries) = %d, want %d", len(result.DriftEntries), maxDriftEntries)
	}
	if !result.Truncated || result.MoreCount != 5 {
		t.Errorf("Truncated = %v, MoreCount = %d, want true, 5", result.Truncated, result.MoreCount)
	}
	paths := driftPaths(result.DriftEntries)
	for i := 1; i < len(paths); i++ {
		if paths[i-1] >= paths[i] {
			t.Errorf("DriftEntries not sorted at index %d: %v", i, paths)
			break
		}
	}
}

func TestDiff_NumericNormalization(t *testing.T) {
	forProvider := map[string]interface{}{"count": int64(1)}
	atProvider := map[string]interface{}{"count": float64(1.0)}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 0 {
		t.Errorf("DriftEntries = %v, want none (1 and 1.0 are equal after normalization)", result.DriftEntries)
	}
}

func TestLabel(t *testing.T) {
	tests := []struct {
		name               string
		syncedStatus       string
		managementPolicies []interface{}
		wantLabel          string
		wantAnnotation     string
	}{
		{"synced true, full control", "True", nil, "Observed difference", ""},
		{"synced false", "False", nil, "Drift", "reconcile failure; inspect Synced condition"},
		{"observe-only", "True", []interface{}{"Observe"}, "Observed difference", "Observe-only; Crossplane will not correct them"},
		{"observe-only takes priority over synced false", "False", []interface{}{"Observe"}, "Observed difference", "Observe-only; Crossplane will not correct them"},
		{"multiple policies is not observe-only", "True", []interface{}{"Observe", "Create"}, "Observed difference", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label, annotation := Label(tt.syncedStatus, tt.managementPolicies)
			if label != tt.wantLabel || annotation != tt.wantAnnotation {
				t.Errorf("Label() = (%q, %q), want (%q, %q)", label, annotation, tt.wantLabel, tt.wantAnnotation)
			}
		})
	}
}

func TestDiff_TypeSensitiveStringVersusNumber(t *testing.T) {
	forProvider := map[string]interface{}{"count": "1"}
	atProvider := map[string]interface{}{"count": int64(1)}
	result := Diff(forProvider, atProvider)
	if len(result.DriftEntries) != 1 {
		t.Errorf("DriftEntries = %v, want 1 entry (string \"1\" and number 1 must differ)", result.DriftEntries)
	}
}
