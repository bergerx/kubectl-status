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

func TestHorizontalPodAutoscalerTemplate(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want string
	}{
		{
			name: "no lastScaleTime should not crash (issue #548)",
			obj: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":              "my-hpa",
					"creationTimestamp": "2024-01-01T00:00:00Z",
				},
				"spec": map[string]interface{}{
					"maxReplicas": 10,
					"scaleTargetRef": map[string]interface{}{
						"kind": "Deployment",
						"name": "my-app",
					},
				},
				"status": map[string]interface{}{
					"currentReplicas": 3,
					"desiredReplicas": 3,
				},
			},
			want: "Replicas",
		},
		{
			name: "resource metric with utilization",
			obj: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":              "my-hpa",
					"creationTimestamp": "2024-01-01T00:00:00Z",
				},
				"spec": map[string]interface{}{
					"maxReplicas": 10,
					"scaleTargetRef": map[string]interface{}{
						"kind": "Deployment",
						"name": "my-app",
					},
					"metrics": []interface{}{
						map[string]interface{}{
							"type": "Resource",
							"resource": map[string]interface{}{
								"name": "cpu",
								"target": map[string]interface{}{
									"type":               "Utilization",
									"averageUtilization": 80,
								},
							},
						},
					},
				},
				"status": map[string]interface{}{
					"currentReplicas": 3,
					"desiredReplicas": 3,
					"currentMetrics": []interface{}{
						map[string]interface{}{
							"type": "Resource",
							"resource": map[string]interface{}{
								"name": "cpu",
								"current": map[string]interface{}{
									"averageUtilization": 45,
									"averageValue":       "450m",
								},
							},
						},
					},
				},
			},
			want: "cpu",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkTemplate(t, "HorizontalPodAutoscaler", tt.obj, tt.want, true)
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
		}, {
			name: "owner reference points to an object that no longer exists",
			obj: map[string]interface{}{
				"metadata": map[string]interface{}{
					"namespace": "test",
					"ownerReferences": []interface{}{
						map[string]interface{}{
							"apiVersion": "apps/v1",
							"controller": true,
							"kind":       "ReplicaSet",
							"name":       "coredns-558bd4d5db",
						},
					},
				},
			},
			want: "Orphan",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkTemplate(t, "owners", tt.obj, tt.want, true)
		})
	}
}

// renderTemplateForTest mirrors checkTemplate's setup but returns the rendered string
// directly, so callers can assert both presence and absence of substrings.
func renderTemplateForTest(t *testing.T, templateName string, obj map[string]interface{}) string {
	t.Helper()
	tmpl, _ := getTemplate()
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, _ := input.NewResourceRepo(f)
	e, _ := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard())
	e.Template = *tmpl
	r := newRenderableObject(map[string]interface{}{}, e, repo)
	got, err := r.renderTemplate(templateName, obj)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	return got
}

func TestContainerStatusSummaryImagePullBackoffHintTemplate(t *testing.T) {
	waitingImagePullBackOff := map[string]interface{}{
		"name":  "main",
		"image": "some-image",
		"state": map[string]interface{}{
			"waiting": map[string]interface{}{
				"reason": "ImagePullBackOff",
			},
		},
	}
	tests := []struct {
		name            string
		obj             map[string]interface{}
		wantContains    string
		wantNotContains string
	}{
		{
			name: "no imagePullSecrets on the Pod hints at likely cause",
			obj: map[string]interface{}{
				"containerStatus": waitingImagePullBackOff,
				"containerSpec":   map[string]interface{}{},
			},
			wantContains: "no imagePullSecrets on this Pod",
		}, {
			name: "imagePullSecrets present and healthy shows no hint",
			obj: map[string]interface{}{
				"containerStatus":      waitingImagePullBackOff,
				"containerSpec":        map[string]interface{}{},
				"podImagePullSecrets":  []interface{}{map[string]interface{}{"name": "some-secret"}},
				"podPullSecretsBroken": false,
			},
			wantNotContains: "imagePullSecrets",
		}, {
			name: "imagePullSecrets present but broken correlates with the pull failure",
			obj: map[string]interface{}{
				"containerStatus":      waitingImagePullBackOff,
				"containerSpec":        map[string]interface{}{},
				"podImagePullSecrets":  []interface{}{map[string]interface{}{"name": "some-secret"}},
				"podPullSecretsBroken": true,
			},
			wantContains: "this Pod's imagePullSecrets have problems",
		}, {
			name: "imagePullPolicy Never suppresses the hint even with no imagePullSecrets",
			obj: map[string]interface{}{
				"containerStatus": waitingImagePullBackOff,
				"containerSpec":   map[string]interface{}{"imagePullPolicy": "Never"},
			},
			wantNotContains: "imagePullSecrets",
		}, {
			name: "unrelated waiting reason shows no hint",
			obj: map[string]interface{}{
				"containerStatus": map[string]interface{}{
					"name":  "main",
					"image": "some-image",
					"state": map[string]interface{}{
						"waiting": map[string]interface{}{
							"reason": "CrashLoopBackOff",
						},
					},
				},
				"containerSpec": map[string]interface{}{},
			},
			wantNotContains: "imagePullSecrets",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderTemplateForTest(t, "container_status_summary", tt.obj)
			if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
				t.Errorf("got = %q, want contains %q", got, tt.wantContains)
			}
			if tt.wantNotContains != "" && strings.Contains(got, tt.wantNotContains) {
				t.Errorf("got = %q, want not contains %q", got, tt.wantNotContains)
			}
		})
	}
}

func TestPodImagePullSecretMissingTemplate(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "some-pod",
			"namespace": "test",
		},
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{"name": "main", "image": "some-image"},
			},
			"imagePullSecrets": []interface{}{
				map[string]interface{}{"name": "does-not-exist"},
			},
		},
		"status": map[string]interface{}{},
	}
	checkTemplate(t, "Pod", obj, "Secret/does-not-exist doesn't exist, but it's referenced in Pod's imagePullSecrets.", true)
}
