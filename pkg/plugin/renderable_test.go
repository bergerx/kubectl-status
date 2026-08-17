package plugin

import (
	"testing"
)

func TestProblematic(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want bool
	}{
		{
			name: "running ready pod is not problematic",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]interface{}{"name": "p", "namespace": "ns"},
				"status": map[string]interface{}{
					"phase": "Running",
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "True"},
					},
				},
			},
			want: false,
		},
		{
			name: "pending pod is problematic",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]interface{}{"name": "p", "namespace": "ns"},
				"status":     map[string]interface{}{"phase": "Pending"},
			},
			want: true,
		},
		{
			// kstatus treats a terminal Failed phase as Current -- the Pod reached the end state
			// its controller expects, however unsuccessful -- so this is deliberately false, not
			// a gap in Problematic(). Callers that already know a Pod is in a batch job's failed
			// list (see Job.tmpl) don't gate on Problematic for that reason.
			name: "failed pod is not problematic per kstatus",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]interface{}{"name": "p", "namespace": "ns"},
				"status":     map[string]interface{}{"phase": "Failed"},
			},
			want: false,
		},
		{
			name: "crash-looping running pod is problematic",
			obj: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata":   map[string]interface{}{"name": "p", "namespace": "ns"},
				"status": map[string]interface{}{
					"phase": "Running",
					"containerStatuses": []interface{}{
						map[string]interface{}{
							"name":         "app",
							"restartCount": int64(3),
							"state": map[string]interface{}{
								"waiting": map[string]interface{}{"reason": "CrashLoopBackOff"},
							},
						},
					},
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRenderableObject(tt.obj)
			if got := r.Problematic(); got != tt.want {
				t.Errorf("Problematic() = %v, want %v", got, tt.want)
			}
		})
	}
}
