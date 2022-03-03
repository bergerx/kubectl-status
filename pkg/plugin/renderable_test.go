package plugin

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderableObject_StatusConditions(t *testing.T) {
	type fields struct {
		Unstructured unstructured.Unstructured
		engine       *renderEngine
	}
	tests := []struct {
		name           string
		fields         fields
		wantConditions []interface{}
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := RenderableObject{
				Unstructured: tt.fields.Unstructured,
				engine:       tt.fields.engine,
			}
			if gotConditions := r.StatusConditions(); !reflect.DeepEqual(gotConditions, tt.wantConditions) {
				t.Errorf("StatusConditions() = %v, want %v", gotConditions, tt.wantConditions)
			}
		})
	}
}
