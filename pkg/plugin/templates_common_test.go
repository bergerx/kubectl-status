package plugin

import (
	"strings"
	"testing"

	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest/fake"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"

	"github.com/bergerx/kubectl-status/pkg/input"
)

func checkTemplate(t *testing.T, templateName string, obj map[string]interface{}, shouldContain string, useRenderable bool) {
	t.Helper()
	tmpl, _ := getTemplate()
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, _ := input.NewResourceRepo(f)
	e, _ := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard())
	e.Template = *tmpl
	r := newRenderableObject(obj, e, repo)
	var objToPassTemplate interface{}
	if useRenderable {
		objToPassTemplate = r
	} else {
		objToPassTemplate = obj
	}
	got, err := r.renderTemplate(templateName, objToPassTemplate)
	if err != nil {
		t.Errorf("renderTemplate() error = %v", err)
		return
	}
	if !strings.Contains(got, shouldContain) {
		t.Errorf("template 'suspended' got = %v, shouldContain = %v", got, shouldContain)
		return
	}
}

func TestObservedGenerationSummaryTemplate(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want string
	}{
		{
			name: "neither generation or observed generation",
			obj:  map[string]interface{}{},
			want: "",
		}, {
			name: "has generation but no observed",
			obj: map[string]interface{}{
				"metadata": map[string]interface{}{
					"generation": 1,
				},
			},
			want: "",
		}, {
			name: "has matching generation and observed",
			obj: map[string]interface{}{
				"metadata": map[string]interface{}{
					"generation": 1,
				},
				"status": map[string]interface{}{
					"observedGeneration": 1,
				},
			},
			want: "",
		}, {
			name: "generation and observed are not matching",
			obj: map[string]interface{}{
				"metadata": map[string]interface{}{
					"generation": 2,
				},
				"status": map[string]interface{}{
					"observedGeneration": 1,
				},
			},
			want: "doesn't match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkTemplate(t, "observed_generation_summary", tt.obj, tt.want, true)
		})
	}
}

func TestSuspendTemplate(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want string
	}{
		{
			name: "replicas set to 0",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"replicas": 0,
				},
			},
			want: "Suspended",
		}, {
			name: "replicas set to 1",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{
					"replicas": 1,
				},
			},
			want: "",
		}, {
			name: "no replicas field should do nothing",
			obj: map[string]interface{}{
				"spec": map[string]interface{}{},
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkTemplate(t, "suspended", tt.obj, tt.want, true)
		})
	}
}

func TestOwnersTemplate(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want string
	}{
		{
			name: "has no owners",
			obj:  map[string]interface{}{},
			want: "",
		}, {
			name: "has empty owner references field",
			obj: map[string]interface{}{
				"metadata": map[string]interface{}{
					"ownerReferences": []map[string]interface{}{
						{},
					},
				},
			},
			want: "",
		}, {
			name: "has an owner reference",
			obj: map[string]interface{}{
				"metadata": map[string]interface{}{
					"ownerReferences": []map[string]interface{}{
						{
							"apiVersion":         "apps/v1",
							"blockOwnerDeletion": true,
							"controller":         true,
							"kind":               "ReplicaSet",
							"name":               "coredns-558bd4d5db",
						},
					},
				},
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkTemplate(t, "owners", tt.obj, tt.want, true)
		})
	}
}
