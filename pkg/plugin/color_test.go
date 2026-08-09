package plugin

import (
	"testing"

	"github.com/spf13/viper"
)

func TestColorAgoAbsolute(t *testing.T) {
	v := viper.New()
	cfg := NewRenderConfig(v)
	v.Set("absolute-time", true)
	input := "2006-01-02T15:04:05Z"
	if got := cfg.colorAgo(input); got != input {
		t.Errorf("colorAgo(%q) = %q, want %q", input, got, input)
	}
}

func TestResourceRef(t *testing.T) {
	tests := []struct {
		name            string
		kind            interface{}
		refName         interface{}
		namespace       interface{}
		callerNamespace interface{}
		nameSuffix      interface{}
		want            string
	}{
		{
			name:    "bare kind/name, no namespace or suffix",
			kind:    "Pod",
			refName: "my-pod",
			want:    "Pod/my-pod",
		},
		{
			name:            "namespace matching caller namespace is dropped",
			kind:            "Secret",
			refName:         "my-secret",
			namespace:       "ns1",
			callerNamespace: "ns1",
			want:            "Secret/my-secret",
		},
		{
			name:            "namespace differing from caller namespace is appended",
			kind:            "Secret",
			refName:         "my-secret",
			namespace:       "ns1",
			callerNamespace: "ns2",
			want:            "Secret/my-secret -n ns1",
		},
		{
			name:      "namespace appended when no caller namespace given",
			kind:      "Node",
			refName:   "node-1",
			namespace: "ignored-for-cluster-scoped",
			want:      "Node/node-1 -n ignored-for-cluster-scoped",
		},
		{
			name:      "namespace \"default\" is still appended",
			kind:      "Service",
			refName:   "svc",
			namespace: "default",
			want:      "Service/svc -n default",
		},
		{
			name:       "nameSuffix glued onto name ahead of namespace",
			kind:       "Service",
			refName:    "svc",
			namespace:  "ns1",
			nameSuffix: ":8080",
			want:       "Service/svc:8080 -n ns1",
		},
		{
			name:    "nil kind/name degrade to empty segments instead of erroring",
			kind:    nil,
			refName: nil,
			want:    "/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resourceRef(tt.kind, tt.refName, tt.namespace, tt.callerNamespace, tt.nameSuffix)
			if got != tt.want {
				t.Fatalf("resourceRef() = %q, want %q", got, tt.want)
			}
		})
	}
}
