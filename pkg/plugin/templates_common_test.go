package plugin

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest/fake"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"

	"github.com/bergerx/kubectl-status/pkg/input"
)

func checkTemplate(t *testing.T, templateName string, obj map[string]interface{}, shouldContain string, useRenderable bool) {
	t.Helper()
	checkTemplateWithViper(t, templateName, obj, shouldContain, useRenderable, viper.New())
}

func checkTemplateWithViper(t *testing.T, templateName string, obj map[string]interface{}, shouldContain string, useRenderable bool, v *viper.Viper) {
	t.Helper()
	cfg := NewRenderConfig(v)
	tmpl, _ := getTemplate(cfg)
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, _ := input.NewResourceRepo(f, cfg.Viper)
	e, _ := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard(), cfg)
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
	cfg := NewRenderConfig(viper.New())
	tmpl, _ := getTemplate(cfg)
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, _ := input.NewResourceRepo(f, cfg.Viper)
	e, _ := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard(), cfg)
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

func runningPodWithNoMetricsObj() map[string]interface{} {
	return map[string]interface{}{
		"metadata": map[string]interface{}{
			"name":      "some-pod",
			"namespace": "test",
		},
		"spec": map[string]interface{}{
			"containers": []interface{}{
				map[string]interface{}{"name": "main", "image": "some-image"},
			},
		},
		"status": map[string]interface{}{
			"phase": "Running",
			"containerStatuses": []interface{}{
				map[string]interface{}{
					"name":  "main",
					"image": "some-image",
					"state": map[string]interface{}{"running": map[string]interface{}{"startedAt": "2024-01-01T00:00:00Z"}},
				},
			},
		},
	}
}

// metricsAPIServiceGVR mirrors the unexported schema.GroupVersionResource of the same name in
// pkg/input -- the apiregistration.k8s.io APIService that fronts metrics-server.
var metricsAPIServiceGVR = schema.GroupVersionResource{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}

// factoryWithHealthyMetricsServer builds a test factory whose dynamic client reports
// metrics-server's APIService as installed and Available, so KubeMetricsUnavailableReason
// returns "" -- letting tests exercise the "healthy but no data recorded for this object yet"
// case (#165 case 3) distinctly from "metrics-server itself is missing/unhealthy" (cases 1-2).
func factoryWithHealthyMetricsServer(t *testing.T) *cmdtesting.TestFactory {
	t.Helper()
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	apiService := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiregistration.k8s.io/v1",
		"kind":       "APIService",
		"metadata":   map[string]interface{}{"name": "v1beta1.metrics.k8s.io"},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Available", "status": "True"},
			},
		},
	}}
	f.FakeDynamicClient = fakedynamic.NewSimpleDynamicClientWithCustomListKinds(
		scheme.Scheme,
		map[schema.GroupVersionResource]string{metricsAPIServiceGVR: "APIServiceList"},
		apiService,
	)
	return f
}

func renderPodTemplate(t *testing.T, f *cmdtesting.TestFactory, obj map[string]interface{}) string {
	return renderPodTemplateWithViper(t, f, obj, viper.New())
}

func renderPodTemplateWithViper(t *testing.T, f *cmdtesting.TestFactory, obj map[string]interface{}, v *viper.Viper) string {
	t.Helper()
	cfg := NewRenderConfig(v)
	tmpl, _ := getTemplate(cfg)
	t.Cleanup(func() { f.Cleanup() })
	repo, err := input.NewResourceRepo(f, cfg.Viper)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard(), cfg)
	e.Template = *tmpl
	r := newRenderableObject(obj, e, repo)
	got, err := r.renderTemplate("Pod", r)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	return got
}

// TestPodContainersMetricsNotInstalledWarningTemplate covers issue #165 case 1: when
// metrics-server was never installed (the default fake test client has no APIService for it), a
// Running pod's Containers section should say so instead of silently omitting cpu/memory usage.
func TestPodContainersMetricsNotInstalledWarningTemplate(t *testing.T) {
	checkTemplate(t, "Pod", runningPodWithNoMetricsObj(), "not installed", true)
}

// TestPodContainersMetricsUnhealthyWarningTemplate covers issue #165 case 2: metrics-server is
// installed but its APIService reports Available=False, surfacing the condition's own message.
func TestPodContainersMetricsUnhealthyWarningTemplate(t *testing.T) {
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	apiService := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiregistration.k8s.io/v1",
		"kind":       "APIService",
		"metadata":   map[string]interface{}{"name": "v1beta1.metrics.k8s.io"},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Available", "status": "False", "message": "endpoints for service/metrics-server in \"kube-system\" have no addresses"},
			},
		},
	}}
	f.FakeDynamicClient = fakedynamic.NewSimpleDynamicClientWithCustomListKinds(
		scheme.Scheme,
		map[schema.GroupVersionResource]string{metricsAPIServiceGVR: "APIServiceList"},
		apiService,
	)
	got := renderPodTemplate(t, f, runningPodWithNoMetricsObj())
	if !strings.Contains(got, "endpoints for service/metrics-server") {
		t.Errorf("expected the APIService condition's message to be surfaced, got = %q", got)
	}
}

// TestPodContainersMetricsNoDataYetTemplate covers issue #165 case 3: metrics-server is healthy,
// but this specific Pod has no recorded usage yet (e.g. it was just created and hasn't been
// scraped). This should say so explicitly rather than staying silent or claiming metrics-server
// itself is unavailable.
func TestPodContainersMetricsNoDataYetTemplate(t *testing.T) {
	got := renderPodTemplate(t, factoryWithHealthyMetricsServer(t), runningPodWithNoMetricsObj())
	if !strings.Contains(got, "no metrics yet") {
		t.Errorf("expected a \"no metrics yet\" note, got = %q", got)
	}
	if strings.Contains(got, "not installed") || strings.Contains(got, "not available") {
		t.Errorf("expected no metrics-server-unavailable wording when metrics-server is healthy, got = %q", got)
	}
}

// TestPodContainersMetricsWarningSuppressedInShallowMode verifies that --shallow, which never
// queries the cluster for enrichment, doesn't misreport an unchecked metrics-server as missing.
func TestPodContainersMetricsWarningSuppressedInShallowMode(t *testing.T) {
	v := viper.New()
	v.Set("shallow", true)
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	got := renderPodTemplateWithViper(t, f, runningPodWithNoMetricsObj(), v)
	if strings.Contains(got, "not installed") || strings.Contains(got, "not available") || strings.Contains(got, "no metrics yet") {
		t.Errorf("expected no metrics-related note in shallow mode, got = %q", got)
	}
}

// TestNodePodDetailsMetricsNotInstalledWarningTemplate covers issue #165 case 1 for Node's
// detailed usage section: with metrics-server not installed (the default fake test client has no
// APIService for it), enabling the opt-in "include-node-detailed-usage" section should surface a
// warning instead of silently skipping cpu/mem/pods usage.
func TestNodePodDetailsMetricsNotInstalledWarningTemplate(t *testing.T) {
	v := viper.New()
	v.Set("include-node-detailed-usage", true)
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "some-node"},
		"status": map[string]interface{}{
			"allocatable": map[string]interface{}{
				"cpu":    "4",
				"memory": "16Gi",
				"pods":   "110",
			},
		},
	}
	checkTemplateWithViper(t, "node_pod_details", obj, "not installed", true, v)
}

// aksKubeletConfigzObj mirrors a real AKS node's `kubectl get --raw
// /api/v1/nodes/{node}/proxy/configz` response (trimmed to the fields the
// "kubelet_configz_summary" template reads). It exercises fields that other fixtures/clusters seen
// so far didn't have set: eviction on pid.available, and kubeReserved without ephemeral-storage.
// The live proxy call itself isn't reachable in these offline tests, so the template is rendered
// directly against this static payload instead of going through KubeGetNodeConfigz.
func aksKubeletConfigzObj() map[string]interface{} {
	return map[string]interface{}{
		"kubeletconfig": map[string]interface{}{
			"evictionHard": map[string]interface{}{
				"memory.available":  "100Mi",
				"nodefs.available":  "10%",
				"nodefs.inodesFree": "5%",
				"pid.available":     "2000",
			},
			"containerLogMaxSize":         "50M",
			"containerLogMaxFiles":        float64(5),
			"containerLogMaxWorkers":      float64(1),
			"containerLogMonitorInterval": "10s",
			"kubeReserved": map[string]interface{}{
				"cpu":    "180m",
				"memory": "650Mi",
				"pid":    "1000",
			},
			"podPidsLimit":                    float64(-1),
			"cpuManagerPolicy":                "none",
			"memoryManagerPolicy":             "None",
			"topologyManagerPolicy":           "none",
			"shutdownGracePeriod":             "0s",
			"shutdownGracePeriodCriticalPods": "0s",
		},
	}
}

func TestKubeletConfigzSummaryTemplateAKSEvictionHard(t *testing.T) {
	got := renderConfigzSummary(t, aksKubeletConfigzObj())
	want := "eviction-hard: mem.available<100Mi nodefs.available<10% nodefs.inodesFree<5% pid.available<2000"
	if !strings.Contains(got, want) {
		t.Errorf("got = %q, want substring %q", got, want)
	}
}

func TestKubeletConfigzSummaryTemplateAKSContainerLogRotation(t *testing.T) {
	got := renderConfigzSummary(t, aksKubeletConfigzObj())
	want := "container log rotation: 50M cap, 5 files, 1 worker, 10s monitor"
	if !strings.Contains(got, want) {
		t.Errorf("got = %q, want substring %q", got, want)
	}
}

func TestKubeletConfigzSummaryTemplateAKSKubeReservedWithoutEphemeralStorage(t *testing.T) {
	got := renderConfigzSummary(t, aksKubeletConfigzObj())
	want := "kubeReserved: cpu:180m mem:650Mi pid:1000"
	if !strings.Contains(got, want) {
		t.Errorf("got = %q, want substring %q", got, want)
	}
	if strings.Contains(got, "systemReserved") {
		t.Errorf("expected no systemReserved line when absent from configz, got = %q", got)
	}
}

// TestKubeletConfigzSummaryTemplateDefaultPoliciesHidden asserts the manager-policy/pidsLimit/
// shutdownGracePeriod fields stay hidden when they're at their default values, matching the
// codebase's convention of only calling out settings that deviate from the norm.
func TestKubeletConfigzSummaryTemplateDefaultPoliciesHidden(t *testing.T) {
	got := renderConfigzSummary(t, aksKubeletConfigzObj())
	for _, unwanted := range []string{"podPidsLimit", "cpuManager", "memoryManager", "topologyManager", "shutdownGracePeriod"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("expected %q to stay hidden at default value, got = %q", unwanted, got)
		}
	}
}

func renderConfigzSummary(t *testing.T, configz map[string]interface{}) string {
	t.Helper()
	cfg := NewRenderConfig(viper.New())
	tmpl, _ := getTemplate(cfg)
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, _ := input.NewResourceRepo(f, cfg.Viper)
	e, _ := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard(), cfg)
	e.Template = *tmpl
	r := newRenderableObject(map[string]interface{}{}, e, repo)
	got, err := r.renderTemplate("kubelet_configz_summary", configz)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	return got
}

func managedResourceObj(forProvider, atProvider map[string]interface{}) map[string]interface{} {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "checkout-network"},
		"spec":     map[string]interface{}{},
		"status":   map[string]interface{}{},
	}
	if forProvider != nil {
		obj["spec"].(map[string]interface{})["forProvider"] = forProvider
	}
	if atProvider != nil {
		obj["status"].(map[string]interface{})["atProvider"] = atProvider
	}
	return obj
}

func TestManagedResourceDriftTemplate_NotAManagedResource(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "some-deployment"},
		"spec":     map[string]interface{}{"replicas": float64(1)},
	}
	got, err := renderManagedResourceDrift(t, obj, viper.New())
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if got != "" {
		t.Errorf("expected no drift output for a non-managed-resource object, got = %q", got)
	}
}

func TestManagedResourceDriftTemplate_NotYetObserved(t *testing.T) {
	obj := managedResourceObj(map[string]interface{}{"region": "eu-west-1"}, nil)
	got, err := renderManagedResourceDrift(t, obj, viper.New())
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "has not been observed yet") {
		t.Errorf("got = %q, want a not-yet-observed message", got)
	}
}

func TestManagedResourceDriftTemplate_InSync(t *testing.T) {
	obj := managedResourceObj(
		map[string]interface{}{"region": "eu-west-1"},
		map[string]interface{}{"region": "eu-west-1"},
	)
	got, err := renderManagedResourceDrift(t, obj, viper.New())
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "Drift: none across 1 configured fields") {
		t.Errorf("got = %q, want an in-sync summary", got)
	}
}

func TestManagedResourceDriftTemplate_DefaultDepthShowsUnifiedDiff(t *testing.T) {
	obj := managedResourceObj(
		map[string]interface{}{"region": "eu-west-1"},
		map[string]interface{}{"region": "us-east-1"},
	)
	got, err := renderManagedResourceDrift(t, obj, viper.New())
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "-region: eu-west-1") || !strings.Contains(got, "+region: us-east-1") {
		t.Errorf("got = %q, want a unified diff of the drifted field", got)
	}
}

func TestManagedResourceDriftTemplate_DeepAddsObservedOnlyCount(t *testing.T) {
	v := viper.New()
	v.Set("deep", true)
	obj := managedResourceObj(
		map[string]interface{}{"region": "eu-west-1"},
		map[string]interface{}{"region": "us-east-1", "arn": "arn:aws:ec2:eu-west-1:123456789012:vpc/vpc-1"},
	)
	got, err := renderManagedResourceDrift(t, obj, v)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "-region: eu-west-1") || !strings.Contains(got, "+region: us-east-1") {
		t.Errorf("got = %q, want the unified diff of the drifted field", got)
	}
	if !strings.Contains(got, "Observed-only fields: 1") {
		t.Errorf("got = %q, want the deep-only observed-only-fields count", got)
	}
}

func renderManagedResourceDrift(t *testing.T, obj map[string]interface{}, v *viper.Viper) (string, error) {
	t.Helper()
	cfg := NewRenderConfig(v)
	tmpl, _ := getTemplate(cfg)
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, _ := input.NewResourceRepo(f, cfg.Viper)
	e, _ := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard(), cfg)
	e.Template = *tmpl
	r := newRenderableObject(obj, e, repo)
	return r.renderTemplate("crossplane_managed_resource_drift", r)
}

func renderCrossplaneTemplate(t *testing.T, templateName string, obj map[string]interface{}) (string, error) {
	t.Helper()
	v := viper.New()
	cfg := NewRenderConfig(v)
	tmpl, _ := getTemplate(cfg)
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	t.Cleanup(func() { f.Cleanup() })
	repo, _ := input.NewResourceRepo(f, cfg.Viper)
	e, _ := newRenderEngine(genericiooptions.NewTestIOStreamsDiscard(), cfg)
	e.Template = *tmpl
	r := newRenderableObject(obj, e, repo)
	return r.renderTemplate(templateName, r)
}

func TestCompositionRefTemplate_V1ClaimOrClusterScopedXR(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "checkout-db"},
		"spec": map[string]interface{}{
			"compositionRef":          map[string]interface{}{"name": "postgres-xl"},
			"compositionRevisionRef":  map[string]interface{}{"name": "postgres-xl-abc123"},
			"compositionUpdatePolicy": "Manual",
		},
	}
	got, err := renderCrossplaneTemplate(t, "crossplane_composition_ref", obj)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	for _, want := range []string{"postgres-xl", "postgres-xl-abc123", "Manual"} {
		if !strings.Contains(got, want) {
			t.Errorf("got = %q, want substring %q", got, want)
		}
	}
}

func TestCompositionRefTemplate_V2Namespaced(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "checkout-db"},
		"spec": map[string]interface{}{
			"crossplane": map[string]interface{}{
				"compositionRef":          map[string]interface{}{"name": "postgres-xl"},
				"compositionUpdatePolicy": "Automatic",
			},
		},
	}
	got, err := renderCrossplaneTemplate(t, "crossplane_composition_ref", obj)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "postgres-xl") {
		t.Errorf("got = %q, want composition name", got)
	}
	if strings.Contains(got, "pinned") {
		t.Errorf("got = %q, Automatic policy must not show the Manual warning", got)
	}
}

func TestCompositionRefTemplate_AbsentWhenNoCrossplaneFields(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "some-deployment"},
		"spec":     map[string]interface{}{"replicas": float64(1)},
	}
	got, err := renderCrossplaneTemplate(t, "crossplane_composition_ref", obj)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if got != "" {
		t.Errorf("expected no output for a non-Crossplane object, got = %q", got)
	}
}

func TestManagedResourceDetailsTemplate_AnnotationsAndSpecFields(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "checkout-network",
			"annotations": map[string]interface{}{
				"crossplane.io/external-name":             "vpc-0123456789abcdef0",
				"crossplane.io/composition-resource-name": "network",
				"crossplane.io/external-create-pending":   "2026-06-01T10:00:00Z",
				"crossplane.io/external-create-succeeded": "2026-06-01T10:00:30Z",
			},
		},
		"spec": map[string]interface{}{
			"forProvider":        map[string]interface{}{"region": "eu-west-1"},
			"providerConfigRef":  map[string]interface{}{"name": "aws-prod"},
			"managementPolicies": []interface{}{"*"},
			"initProvider": map[string]interface{}{
				"tags": map[string]interface{}{"environment": "production"},
			},
		},
	}
	got, err := renderCrossplaneTemplate(t, "crossplane_managed_resource_details", obj)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	for _, want := range []string{
		"vpc-0123456789abcdef0",
		"network",
		"succeeded, took 30s",
		"aws-prod",
		"full control",
		"tags",
		"applied only at creation time",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("got = %q, want substring %q", got, want)
		}
	}
}

func TestManagedResourceDetailsTemplate_ExternalCreatePendingWithoutSucceeded(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "checkout-network",
			"annotations": map[string]interface{}{
				"crossplane.io/external-create-pending": "2026-06-01T10:00:00Z",
			},
		},
		"spec": map[string]interface{}{
			"forProvider": map[string]interface{}{"region": "eu-west-1"},
		},
	}
	got, err := renderCrossplaneTemplate(t, "crossplane_managed_resource_details", obj)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "External create") || !strings.Contains(got, "pending") {
		t.Errorf("got = %q, want a pending-without-succeeded warning", got)
	}
	if strings.Contains(got, "took") {
		t.Errorf("got = %q, must not compute a latency without a succeeded timestamp", got)
	}
}

func TestManagedResourceDetailsTemplate_PartialManagementPolicies(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "checkout-network"},
		"spec": map[string]interface{}{
			"forProvider":        map[string]interface{}{"region": "eu-west-1"},
			"managementPolicies": []interface{}{"Observe", "LateInitialize"},
		},
	}
	got, err := renderCrossplaneTemplate(t, "crossplane_managed_resource_details", obj)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "Observe, LateInitialize") {
		t.Errorf("got = %q, want the partial policy list displayed", got)
	}
	if strings.Contains(got, "full control") {
		t.Errorf("got = %q, partial policies must not show the full-control label", got)
	}
}

func TestManagedResourceDetailsTemplate_NotAManagedResource(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "some-deployment"},
		"spec":     map[string]interface{}{"replicas": float64(1)},
	}
	got, err := renderCrossplaneTemplate(t, "crossplane_managed_resource_details", obj)
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if got != "" {
		t.Errorf("expected no output for a non-managed-resource object, got = %q", got)
	}
}
