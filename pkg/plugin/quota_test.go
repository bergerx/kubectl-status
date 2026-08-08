package plugin

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestEvictionHeadroomPercentThreshold(t *testing.T) {
	// nodefs.available<10%, actual disk currently 73.6% free (34.8GB/47.3GB): plenty of headroom.
	got := evictionHeadroom("10%", 34.8e9, 47.3e9, "B")
	if !got.OK || got.Tripped || got.AtRisk {
		t.Fatalf("evictionHeadroom() = %+v, want OK, not tripped, not at risk", got)
	}
	if got.Current != "74% free" {
		t.Errorf("Current = %q, want %q", got.Current, "74% free")
	}
}

func TestEvictionHeadroomPercentThresholdAtRisk(t *testing.T) {
	// threshold 10%, currently 12% free: above the threshold but within 1.5x of tripping it.
	got := evictionHeadroom("10%", 1.2, 10, "")
	if !got.OK || got.Tripped || !got.AtRisk {
		t.Fatalf("evictionHeadroom() = %+v, want OK, not tripped, at risk", got)
	}
}

func TestEvictionHeadroomPercentThresholdTripped(t *testing.T) {
	// threshold 10%, currently 5% free: already past the threshold.
	got := evictionHeadroom("10%", 0.5, 10, "")
	if !got.OK || !got.Tripped {
		t.Fatalf("evictionHeadroom() = %+v, want OK and tripped", got)
	}
}

func TestEvictionHeadroomAbsoluteThreshold(t *testing.T) {
	// memory.available<100Mi, node currently reports 11.4GB available: no total needed.
	got := evictionHeadroom("100Mi", 11.4e9, 0, "B")
	if !got.OK || got.Tripped || got.AtRisk {
		t.Fatalf("evictionHeadroom() = %+v, want OK, not tripped, not at risk", got)
	}
}

func TestEvictionHeadroomPercentThresholdWithoutTotalIsNotOK(t *testing.T) {
	// A percent threshold can't be normalized without knowing the resource's total capacity.
	got := evictionHeadroom("10%", 34.8e9, 0, "B")
	if got.OK {
		t.Fatalf("evictionHeadroom() = %+v, want OK=false when total is unknown", got)
	}
	if got.Threshold != "10%" {
		t.Errorf("Threshold = %q, want %q (raw threshold preserved for fallback display)", got.Threshold, "10%")
	}
}

func TestEvictionHeadroomUnparseableThresholdIsNotOK(t *testing.T) {
	got := evictionHeadroom("not-a-number", 1, 1, "")
	if got.OK {
		t.Fatalf("evictionHeadroom() = %+v, want OK=false for an unparseable threshold", got)
	}
}

func TestEvictionAnnotationTrippedDoesNotCorruptOnPercentSign(t *testing.T) {
	// Regression test: the message always contains a literal "%" from the threshold/current
	// percentages. evictionAnnotation must build it with Sprint, not Sprintf -- piping the composed
	// string back through a Sprintf-based colorer (as an earlier version of this code did via the
	// template's "bold" func) misparses "% " followed by a verb letter (T, f, s, ...) as a format
	// verb with a missing argument, e.g. "10% TRIPPED" corrupts to "10%!T(MISSING)RIPPED".
	got := evictionAnnotation("10%", 0.5, 10, "")
	want := " (10% TRIPPED: 5% free)"
	if got != want {
		t.Fatalf("evictionAnnotation() = %q, want %q", got, want)
	}
}

func TestEvictionAnnotationAtRisk(t *testing.T) {
	got := evictionAnnotation("10%", 1.2, 10, "")
	want := " (nearing 10%: 12% free)"
	if got != want {
		t.Fatalf("evictionAnnotation() = %q, want %q", got, want)
	}
}

func TestEvictionAnnotationEmptyWhenHealthy(t *testing.T) {
	got := evictionAnnotation("10%", 9.0, 10, "")
	if got != "" {
		t.Fatalf("evictionAnnotation() = %q, want empty string when not at risk", got)
	}
}

// unstructuredFromJSON decodes a manifest into the map form templates see. Written as JSON rather
// than as a Go map literal because quotaRolloutHeadroom converts its inputs to their API types,
// and that conversion is typed: replicas has to arrive as an int64, exactly as it does from the
// apiserver or from a -f manifest, which a hand-written map literal makes easy to get wrong.
func unstructuredFromJSON(t *testing.T, manifest string) map[string]interface{} {
	t.Helper()
	obj := map[string]interface{}{}
	if err := json.Unmarshal([]byte(manifest), &obj); err != nil {
		t.Fatalf("unmarshalling test manifest: %v", err)
	}
	return obj
}

// resourceQuotaJSON is a namespace quota tracking requests.memory, with hard and used given as
// quantity strings the way the quota controller writes them.
func resourceQuotaJSON(hard, used string) string {
	return fmt.Sprintf(`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
		"status":{"hard":{"requests.memory":%q},"used":{"requests.memory":%q}}}`, hard, used)
}

// deploymentJSON is a Deployment of replicas Pods, each requesting 256Mi, with status.replicas
// Pods currently existing and the default (25%) surge allowance.
func deploymentJSON(replicas, existing int, extraSpec string) string {
	return fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"test"},
		"spec":{"replicas":%d,%s"template":{"spec":{"containers":[{"name":"main","resources":{"requests":{"memory":"256Mi"}}}]}}},
		"status":{"replicas":%d}}`, replicas, extraSpec, existing)
}

func TestQuotaRolloutHeadroom(t *testing.T) {
	tests := []struct {
		name          string
		workload      string
		quotas        []string
		wantExtraPods int
		wantShortfall *quotaShortfall
	}{
		{
			// 4 replicas + 25% surge = 5 allowed, 4 exist, so one 256Mi Pod is pending and the
			// quota has 128Mi left for it.
			name:          "rolling update surge doesn't fit the remaining headroom",
			workload:      deploymentJSON(4, 4, ""),
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "256Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:          "no shortfall reported when the surge fits",
			workload:      deploymentJSON(4, 4, ""),
			quotas:        []string{resourceQuotaJSON("2Gi", "1Gi")},
			wantExtraPods: 1,
		},
		{
			// A blocked scale-up needs a Pod per missing replica, not just the surge one.
			name:          "need scales with the number of Pods still missing",
			workload:      deploymentJSON(4, 1, ""),
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 4,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "1Gi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:          "explicit maxSurge percentage is resolved against the replica count",
			workload:      deploymentJSON(4, 4, `"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxSurge":"50%"}},`),
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 2,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "512Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:          "Recreate strategy gets no surge allowance",
			workload:      deploymentJSON(4, 4, `"strategy":{"type":"Recreate"},`),
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 0,
		},
		{
			// A StatefulSet deletes a Pod before creating its replacement, so a rollout at full
			// replica count needs no extra headroom.
			name: "StatefulSet at full replica count needs no headroom",
			workload: `{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"name":"db","namespace":"test"},
				"spec":{"replicas":3,"template":{"spec":{"containers":[{"name":"main","resources":{"requests":{"memory":"256Mi"}}}]}}},
				"status":{"replicas":3}}`,
			quotas:        []string{resourceQuotaJSON("2Gi", "2Gi")},
			wantExtraPods: 0,
		},
		{
			name: "StatefulSet scale-up blocked by quota",
			workload: `{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"name":"db","namespace":"test"},
				"spec":{"replicas":3,"template":{"spec":{"containers":[{"name":"main","resources":{"requests":{"memory":"256Mi"}}}]}}},
				"status":{"replicas":2}}`,
			quotas:        []string{resourceQuotaJSON("2Gi", "2Gi")},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "256Mi", Free: "0", Used: "2Gi", Hard: "2Gi"},
		},
		{
			// Sidecars (restarting initContainers) run alongside the main containers so they add
			// up, which upstream's PodRequests handles -- 256Mi + 64Mi here.
			name: "sidecar requests count towards the Pod's total",
			workload: `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"test"},
				"spec":{"replicas":1,"template":{"spec":{
					"containers":[{"name":"main","resources":{"requests":{"memory":"256Mi"}}}],
					"initContainers":[{"name":"proxy","restartPolicy":"Always","resources":{"requests":{"memory":"64Mi"}}},
						{"name":"setup","resources":{"requests":{"memory":"128Mi"}}}]}}},
				"status":{"replicas":1}}`,
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "320Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:     "a pods count quota is measured in Pods, not bytes",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"status":{"hard":{"pods":"4"},"used":{"pods":"4"}}}`},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "pods", Need: "1", Free: "0", Used: "4", Hard: "4"},
		},
		{
			// Whether these Pods fall in the scope isn't decidable here, so the quota is skipped
			// rather than reported against.
			name:     "scoped quota is skipped",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"spec":{"scopes":["BestEffort"]},
				"status":{"hard":{"requests.memory":"2Gi"},"used":{"requests.memory":"2Gi"}}}`},
			wantExtraPods: 1,
		},
		{
			name:     "object count quota the Pods don't consume is skipped",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"objects","namespace":"test"},
				"status":{"hard":{"count/deployments.apps":"1"},"used":{"count/deployments.apps":"1"}}}`},
			wantExtraPods: 1,
		},
		{
			// status.used not yet reported for a tracked resource means unknown free capacity,
			// which must not be read as the full hard limit being available either way.
			name:     "resource missing from status.used is skipped",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"status":{"hard":{"requests.memory":"2Gi"},"used":{}}}`},
			wantExtraPods: 1,
		},
		{
			// The bare "memory"/"cpu" quota names are aliases of the requests form.
			name:     "bare memory quota name is treated as requests.memory",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"status":{"hard":{"memory":"2Gi"},"used":{"memory":"1920Mi"}}}`},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "memory", Need: "256Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name: "limits quota is compared against the Pod's limits, not its requests",
			workload: `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"test"},
				"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"main","resources":{
					"requests":{"memory":"256Mi"},"limits":{"memory":"512Mi"}}}]}}},
				"status":{"replicas":1}}`,
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"status":{"hard":{"limits.memory":"2Gi"},"used":{"limits.memory":"1920Mi"}}}`},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "limits.memory", Need: "512Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:          "a kind with no rollout of its own reports nothing",
			workload:      `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web","namespace":"test"}}`,
			quotas:        []string{resourceQuotaJSON("2Gi", "2Gi")},
			wantExtraPods: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var quotas []interface{}
			for _, quota := range tt.quotas {
				quotas = append(quotas, unstructuredFromJSON(t, quota))
			}
			got := quotaRolloutHeadroom(quotas, unstructuredFromJSON(t, tt.workload))
			if got.ExtraPods != tt.wantExtraPods {
				t.Errorf("quotaRolloutHeadroom() ExtraPods = %d, want %d", got.ExtraPods, tt.wantExtraPods)
			}
			if tt.wantShortfall == nil {
				if len(got.Quotas) != 0 {
					t.Fatalf("quotaRolloutHeadroom() Quotas = %+v, want none", got.Quotas)
				}
				return
			}
			if len(got.Quotas) != 1 || len(got.Quotas[0].Shortfalls) != 1 {
				t.Fatalf("quotaRolloutHeadroom() Quotas = %+v, want exactly one shortfall", got.Quotas)
			}
			if got.Quotas[0].Shortfalls[0] != *tt.wantShortfall {
				t.Errorf("quotaRolloutHeadroom() shortfall = %+v, want %+v", got.Quotas[0].Shortfalls[0], *tt.wantShortfall)
			}
		})
	}
}
