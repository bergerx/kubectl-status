package plugin

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// newTestRenderableObject builds a bare RenderableObject wrapping obj, with no engine/repo. Every
// function under test here (detectCrossplaneResource, detectGatekeeperConstraint, and the
// RenderableObject accessors they call: Spec/APIVersion) only reads r.Object, so this is enough
// -- unlike the template-rendering tests in templates_common_test.go, no engine, repo, or fake
// client is needed to unit test the detector functions themselves.
func newTestRenderableObject(obj map[string]interface{}) RenderableObject {
	return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
}

func TestDetectCrossplaneResource(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want bool
	}{
		{
			name: "v1 compositionRef",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"compositionRef": map[string]interface{}{"name": "postgres-xl"},
				},
			},
			want: true,
		},
		{
			name: "v1 compositionRevisionRef only",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"compositionRevisionRef": map[string]interface{}{"name": "postgres-xl-abc123"},
				},
			},
			want: true,
		},
		{
			name: "v1 compositionUpdatePolicy only",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"compositionUpdatePolicy": "Manual",
				},
			},
			want: true,
		},
		{
			name: "v2 namespaced compositionRef under spec.crossplane",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"crossplane": map[string]interface{}{
						"compositionRef": map[string]interface{}{"name": "postgres-xl"},
					},
				},
			},
			want: true,
		},
		{
			name: "v1 composed resourceRefs",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"resourceRefs": []interface{}{
						map[string]interface{}{"kind": "VPC", "name": "checkout-vpc"},
					},
				},
			},
			want: true,
		},
		{
			name: "v2 namespaced resourceRefs under spec.crossplane",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"crossplane": map[string]interface{}{
						"resourceRefs": []interface{}{
							map[string]interface{}{"kind": "VPC", "name": "checkout-vpc"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "resourceRefs present but shaped wrong (coincidental field name)",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"resourceRefs": "not-a-list",
				},
			},
			want: false,
		},
		{
			name: "resourceRefs is a list but entries aren't {kind,name} maps",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"resourceRefs": []interface{}{"just-a-string"},
				},
			},
			want: false,
		},
		{
			name: "non-empty forProvider (managed resource)",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"forProvider": map[string]interface{}{"region": "eu-west-1"},
				},
			},
			want: true,
		},
		{
			name: "empty forProvider map does not count",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"forProvider": map[string]interface{}{},
				},
			},
			want: false,
		},
		{
			name: "no spec at all",
			obj:  map[string]interface{}{},
			want: false,
		},
		{
			name: "vanilla spec with unrelated fields",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{"replicas": float64(1)},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRenderableObject(tt.obj)
			name, ok := detectCrossplaneResource(r)
			if ok != tt.want {
				t.Errorf("detectCrossplaneResource() ok = %v, want %v", ok, tt.want)
			}
			if ok && name != "crossplane_default_resource" {
				t.Errorf("detectCrossplaneResource() templateName = %q, want %q", name, "crossplane_default_resource")
			}
			if !ok && name != "" {
				t.Errorf("detectCrossplaneResource() templateName = %q, want empty when ok=false", name)
			}
		})
	}
}

func TestDetectGatekeeperConstraint(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		want       bool
	}{
		{"matching group", "constraints.gatekeeper.sh/v1beta1", true},
		{"different version, same group", "constraints.gatekeeper.sh/v1", true},
		{"unrelated group with similar looking spec", "example.com/v1", false},
		{"empty apiVersion", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := map[string]interface{}{"apiVersion": tt.apiVersion}
			r := newTestRenderableObject(obj)
			name, ok := detectGatekeeperConstraint(r)
			if ok != tt.want {
				t.Errorf("detectGatekeeperConstraint() ok = %v, want %v", ok, tt.want)
			}
			if ok && name != "gatekeeper_constraint_fallback" {
				t.Errorf("detectGatekeeperConstraint() templateName = %q, want %q", name, "gatekeeper_constraint_fallback")
			}
		})
	}
}

// TestDefaultResourceTemplates_RunsAllMatchingDetectors proves the registry isn't
// first-match-wins: an object could in principle trip more than one ecosystem's detector, and
// DefaultResourceTemplates must collect every match rather than stopping at the first.
func TestDefaultResourceTemplates_RunsAllMatchingDetectors(t *testing.T) {
	obj := map[string]interface{}{
		"apiVersion": "constraints.gatekeeper.sh/v1beta1",
		"spec": map[string]interface{}{
			"forProvider": map[string]interface{}{"region": "eu-west-1"},
		},
	}
	r := newTestRenderableObject(obj)
	got := r.DefaultResourceTemplates()
	want := []string{"crossplane_default_resource", "gatekeeper_constraint_fallback"}
	if len(got) != len(want) {
		t.Fatalf("DefaultResourceTemplates() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DefaultResourceTemplates()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDefaultResourceTemplates_NoMatches(t *testing.T) {
	obj := map[string]interface{}{
		"apiVersion": "apps/v1",
		"spec":       map[string]interface{}{"replicas": float64(1)},
	}
	r := newTestRenderableObject(obj)
	got := r.DefaultResourceTemplates()
	if len(got) != 0 {
		t.Errorf("DefaultResourceTemplates() = %v, want empty", got)
	}
}
