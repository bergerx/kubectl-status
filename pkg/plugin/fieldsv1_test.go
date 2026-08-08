package plugin

import (
	"reflect"
	"testing"
)

func TestFieldsV1Paths(t *testing.T) {
	tests := []struct {
		name     string
		fieldsV1 map[string]interface{}
		want     []string
	}{
		{
			name:     "single nested field under spec",
			fieldsV1: map[string]interface{}{"f:spec": map[string]interface{}{"f:template": map[string]interface{}{".": struct{}{}}}},
			want:     []string{"spec.template"},
		},
		{
			name:     "leaf field under metadata",
			fieldsV1: map[string]interface{}{"f:metadata": map[string]interface{}{"f:annotations": map[string]interface{}{".": struct{}{}}}},
			want:     []string{"metadata.annotations"},
		},
		{
			name: "multiple siblings under status stop at status",
			fieldsV1: map[string]interface{}{"f:status": map[string]interface{}{
				"f:conditions": map[string]interface{}{},
				"f:phase":      map[string]interface{}{},
			}},
			want: []string{"status"},
		},
		{
			name: "mix of labels, template and conditions",
			fieldsV1: map[string]interface{}{
				"f:metadata": map[string]interface{}{"f:labels": map[string]interface{}{".": struct{}{}}},
				"f:spec":     map[string]interface{}{"f:template": map[string]interface{}{".": struct{}{}}},
				"f:status":   map[string]interface{}{"f:conditions": map[string]interface{}{}},
			},
			want: []string{"metadata.labels", "spec.template", "status.conditions"},
		},
		{
			name:     "single owned label descends into the label key",
			fieldsV1: map[string]interface{}{"f:metadata": map[string]interface{}{"f:labels": map[string]interface{}{"f:app": map[string]interface{}{}}}},
			want:     []string{"metadata.labels.app"},
		},
		{
			name: "single owned annotation descends into the annotation key, quoted since it contains dots",
			fieldsV1: map[string]interface{}{"f:metadata": map[string]interface{}{"f:annotations": map[string]interface{}{
				"f:deployment.kubernetes.io/revision": map[string]interface{}{},
			}}},
			want: []string{`metadata.annotations."deployment.kubernetes.io/revision"`},
		},
		{
			name: "multiple owned labels stop at metadata.labels",
			fieldsV1: map[string]interface{}{"f:metadata": map[string]interface{}{"f:labels": map[string]interface{}{
				"f:app":  map[string]interface{}{},
				"f:tier": map[string]interface{}{},
			}}},
			want: []string{"metadata.labels"},
		},
		{
			name:     "empty fieldsV1",
			fieldsV1: map[string]interface{}{},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fieldsV1Paths(tt.fieldsV1); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fieldsV1Paths() = %v, want %v", got, tt.want)
			}
		})
	}
}
