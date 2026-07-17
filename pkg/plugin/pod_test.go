package plugin

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest/fake"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"

	"github.com/bergerx/kubectl-status/pkg/input"
)

func renderContainerStatusSummary(t *testing.T, containerStatus map[string]interface{}) string {
	t.Helper()
	cfg := NewRenderConfig(viper.New())
	tmpl, err := getTemplate(cfg)
	if err != nil {
		t.Fatalf("getTemplate() error = %v", err)
	}
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, err := input.NewResourceRepo(f, cfg.Viper)
	if err != nil {
		t.Fatalf("NewResourceRepo() error = %v", err)
	}
	e, err := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard(), cfg)
	if err != nil {
		t.Fatalf("newRenderEngine() error = %v", err)
	}
	e.Template = *tmpl
	pod := newRenderableObject(map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]interface{}{"name": "test-pod", "namespace": "test"},
	}, e, repo)
	got, err := pod.renderTemplate("container_status_summary", map[string]interface{}{
		"pod":             pod,
		"containerStatus": containerStatus,
	})
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	return got
}

// TestContainerStatusSummaryTerminatedLogs covers the current-instance "Last failure logs" block:
// a clean (Completed) exit is never shown, a crash is always shown, regardless of container type.
func TestContainerStatusSummaryTerminatedLogs(t *testing.T) {
	tests := []struct {
		name          string
		reason        string
		wantLogsBlock bool
	}{
		{name: "successful completion", reason: "Completed", wantLogsBlock: false},
		{name: "crash", reason: "Error", wantLogsBlock: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderContainerStatusSummary(t, map[string]interface{}{
				"name":  "some-container",
				"image": "busybox:latest",
				"state": map[string]interface{}{
					"terminated": map[string]interface{}{
						"reason":    tt.reason,
						"exitCode":  1,
						"startedAt": "2026-07-16T18:10:29Z",
					},
				},
			})
			gotLogsBlock := strings.Contains(got, "Last failure logs:") || strings.Contains(got, "has no logs")
			if gotLogsBlock != tt.wantLogsBlock {
				t.Errorf("logs block present = %v, want %v; rendered = %q", gotLogsBlock, tt.wantLogsBlock, got)
			}
		})
	}
}

// TestContainerStatusSummaryPreviousInstanceLogs covers the lastState "Last failure logs" block: a
// container still waiting to retry always shows it (no recency gate, since it's the current
// unresolved problem), a recovered (running) container only shows it while recent, and a currently
// terminated container never triggers this block (the current-instance block already covers it).
func TestContainerStatusSummaryPreviousInstanceLogs(t *testing.T) {
	tests := []struct {
		name          string
		state         map[string]interface{}
		finishedAt    string
		wantLogsBlock bool
	}{
		{
			name:          "still waiting to retry (e.g. CrashLoopBackOff), old failure",
			state:         map[string]interface{}{"waiting": map[string]interface{}{"reason": "CrashLoopBackOff"}},
			finishedAt:    time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			wantLogsBlock: true,
		},
		{
			name:          "recovered (running), recent failure",
			state:         map[string]interface{}{"running": map[string]interface{}{"startedAt": time.Now().Format(time.RFC3339)}},
			finishedAt:    time.Now().Add(-10 * time.Minute).Format(time.RFC3339),
			wantLogsBlock: true,
		},
		{
			name:          "recovered (running), stale failure",
			state:         map[string]interface{}{"running": map[string]interface{}{"startedAt": time.Now().Format(time.RFC3339)}},
			finishedAt:    time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			wantLogsBlock: false,
		},
		{
			name: "currently terminated (current-instance block already covers this)",
			state: map[string]interface{}{"terminated": map[string]interface{}{
				"reason": "Error", "exitCode": 1, "startedAt": time.Now().Format(time.RFC3339),
			}},
			finishedAt:    time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			wantLogsBlock: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderContainerStatusSummary(t, map[string]interface{}{
				"name":  "some-container",
				"image": "busybox:latest",
				"state": tt.state,
				"lastState": map[string]interface{}{
					"terminated": map[string]interface{}{
						"reason":     "Error",
						"exitCode":   1,
						"startedAt":  "2026-07-16T18:09:00Z",
						"finishedAt": tt.finishedAt,
					},
				},
			})
			gotLogsBlock := strings.Contains(got, "Last failure logs:") || strings.Contains(got, "has no previous logs")
			if gotLogsBlock != tt.wantLogsBlock {
				t.Errorf("logs block present = %v, want %v; rendered = %q", gotLogsBlock, tt.wantLogsBlock, got)
			}
		})
	}
}
