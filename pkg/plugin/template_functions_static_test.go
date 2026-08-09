package plugin

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestStatefulSetRollbackTrapBlocker(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	longUnready := now.Add(-time.Hour).Format(time.RFC3339)
	justUnready := now.Add(-time.Second).Format(time.RFC3339)
	defaultParams := statefulSetRollbackTrapParams{
		namePrefix:     "web-",
		replicas:       3,
		updateRevision: "web-good",
		threshold:      10 * time.Minute,
		now:            now,
	}
	// pod builds a StatefulSet-owned Pod as the trap detection sees it: named with an ordinal,
	// labelled with the revision it was created on, and with a Ready condition.
	pod := func(name, revision, ready, lastTransitionTime string) RenderableObject {
		return RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              name,
				"labels":            map[string]interface{}{"controller-revision-hash": revision},
				"creationTimestamp": longUnready,
			},
			"status": map[string]interface{}{"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": ready, "lastTransitionTime": lastTransitionTime},
			}},
		}}}
	}
	terminating := func(pod RenderableObject) RenderableObject {
		pod.Object["metadata"].(map[string]interface{})["deletionTimestamp"] = longUnready
		return pod
	}

	tests := []struct {
		name      string
		pods      []RenderableObject
		params    statefulSetRollbackTrapParams
		wantPod   string
		wantFound bool
	}{
		{
			name:      "Pod stuck unready on an outdated revision is the trap",
			pods:      []RenderableObject{pod("web-0", "web-bad", "False", longUnready)},
			wantPod:   "web-0",
			wantFound: true,
		},
		{
			name: "reports the lowest-ordinal unready Pod, the one the sync is stopped at",
			pods: []RenderableObject{
				pod("web-2", "web-bad", "False", longUnready),
				pod("web-1", "web-bad", "False", longUnready),
			},
			wantPod:   "web-1",
			wantFound: true,
		},
		{
			name: "no trap while a lower-ordinal Pod is already on the target revision",
			pods: []RenderableObject{
				// The rollout is stopped at web-0, and deleting web-2 wouldn't get it going.
				pod("web-0", "web-good", "False", longUnready),
				pod("web-2", "web-bad", "False", longUnready),
			},
		},
		{name: "no trap when every Pod is Ready", pods: []RenderableObject{pod("web-0", "web-bad", "True", longUnready)}},
		{name: "no trap when the Pod already reached the target revision", pods: []RenderableObject{pod("web-0", "web-good", "False", longUnready)}},
		{name: "no trap without a revision label", pods: []RenderableObject{pod("web-0", "", "False", longUnready)}},
		{name: "no trap before the unready threshold, still ordinary startup latency", pods: []RenderableObject{pod("web-0", "web-bad", "False", justUnready)}},
		{name: "no trap for an already terminating Pod, the controller is awaiting its deletion", pods: []RenderableObject{terminating(pod("web-0", "web-bad", "False", longUnready))}},
		{
			name:   "no trap below the partition, such a Pod is recreated on the current revision",
			pods:   []RenderableObject{pod("web-0", "web-bad", "False", longUnready)},
			params: statefulSetRollbackTrapParams{namePrefix: "web-", replicas: 3, partition: 1, updateRevision: "web-good", threshold: 10 * time.Minute, now: now},
		},
		{
			name:   "no trap for a condemned Pod outside the replica range",
			pods:   []RenderableObject{pod("web-3", "web-bad", "False", longUnready)},
			params: defaultParams,
		},
		{name: "no trap for a Pod of another workload sharing the selector", pods: []RenderableObject{pod("web-canary-0", "web-bad", "False", longUnready)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := tt.params
			if params.namePrefix == "" {
				params = defaultParams
			}
			gotPod, gotRevision, gotFound := statefulSetRollbackTrapBlocker(tt.pods, params)
			if gotFound != tt.wantFound {
				t.Fatalf("statefulSetRollbackTrapBlocker() found = %v, want %v", gotFound, tt.wantFound)
			}
			if !tt.wantFound {
				return
			}
			if gotPod.Name() != tt.wantPod {
				t.Errorf("statefulSetRollbackTrapBlocker() pod = %q, want %q", gotPod.Name(), tt.wantPod)
			}
			if gotRevision != "web-bad" {
				t.Errorf("statefulSetRollbackTrapBlocker() revision = %q, want %q", gotRevision, "web-bad")
			}
		})
	}
}
