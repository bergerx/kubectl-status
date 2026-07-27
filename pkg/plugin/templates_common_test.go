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

// nodeUsageObj is a minimal Node for rendering the usage sub-templates against: enough allocatable
// for the percentage denominators, nothing else.
func nodeUsageObj() map[string]interface{} {
	return map[string]interface{}{
		"metadata": map[string]interface{}{"name": "some-node"},
		"status": map[string]interface{}{
			"allocatable": map[string]interface{}{
				"cpu":               "4",
				"memory":            "16Gi",
				"pods":              "110",
				"ephemeral-storage": "100Gi",
			},
		},
	}
}

// newNodeRenderable builds a RenderableObject over obj wired to the offline fake client. The Node
// sub-templates take the kubelet-api payloads as template arguments rather than fetching them
// themselves, so tests can render them against hand-built configz/stats payloads even though the
// live nodes/{name}/proxy calls they normally come from aren't reachable here.
func newNodeRenderable(t *testing.T, v *viper.Viper, obj map[string]interface{}) RenderableObject {
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
	return newRenderableObject(obj, e, repo)
}

func renderNodeSubTemplate(t *testing.T, r RenderableObject, name string, data interface{}) string {
	t.Helper()
	got, err := r.renderTemplate(name, data)
	if err != nil {
		t.Fatalf("renderTemplate(%q) error = %v", name, err)
	}
	return got
}

// renderNodePodDetails renders the whole usage section against the three sources it correlates:
// the Node object, kubelet configz and kubelet stats/summary's node entry. metrics-server is never
// available in these offline tests, so the metrics-server-backed clauses stay out of reach here
// and are covered by the live e2e fixture instead.
func renderNodePodDetails(t *testing.T, v *viper.Viper, kubeletconfig, stats map[string]interface{}) string {
	t.Helper()
	r := newNodeRenderable(t, v, nodeUsageObj())
	return renderNodeSubTemplate(t, r, "node_pod_details", map[string]interface{}{
		"r": r, "kubeletconfig": kubeletconfig, "stats": stats,
	})
}

// TestNodePodDetailsMetricsNotInstalledWarningTemplate covers issue #165 case 1 for Node's
// detailed usage section: with metrics-server not installed (the default fake test client has no
// APIService for it), enabling the opt-in "include-node-detailed-usage" section should surface a
// warning instead of silently skipping cpu/mem/pods usage.
func TestNodePodDetailsMetricsNotInstalledWarningTemplate(t *testing.T) {
	v := viper.New()
	v.Set("include-node-detailed-usage", true)
	got := renderNodePodDetails(t, v, nil, nil)
	if !strings.Contains(got, "not installed") {
		t.Errorf("got = %q, want a metrics-server-not-installed warning", got)
	}
}

// TestNodePodDetailsFoldsProcessesOntoPodsLine covers the pid side of "how many more things can
// this node run": the kubelet's rlimit counters share the pods line rather than getting a stats
// block of their own, and pid.available is annotated right where the process count already is.
func TestNodePodDetailsFoldsProcessesOntoPodsLine(t *testing.T) {
	v := viper.New()
	v.Set("include-node-detailed-usage", false)
	got := renderNodePodDetails(t, v,
		map[string]interface{}{"evictionHard": map[string]interface{}{"pid.available": "10"}},
		map[string]interface{}{"rlimit": map[string]interface{}{"curproc": 988.0, "maxpid": 1000.0}})
	if !strings.Contains(got, "processes 988/1k (99%, rlimit)") {
		t.Errorf("got = %q, want the process count on the pods line", got)
	}
	if !strings.Contains(got, "nearing 10: 12 free") {
		t.Errorf("got = %q, want a nearing-threshold annotation on the processes figure", got)
	}
}

// TestNodePodDetailsPutsFilesystemsUnderEphemeralStorage pins the ordering that makes the kubelet's
// filesystem stats readable: they sit under the ephemeral-storage line whose allocatable figure
// they're the live counterpart to, not in a separate section.
func TestNodePodDetailsPutsFilesystemsUnderEphemeralStorage(t *testing.T) {
	v := viper.New()
	v.Set("include-node-detailed-usage", false)
	got := renderNodePodDetails(t, v, nil, map[string]interface{}{
		"fs": map[string]interface{}{
			"usedBytes": 1.0e9, "capacityBytes": 10.0e9, "availableBytes": 9.0e9,
			"inodesUsed": 1.0e5, "inodes": 1.0e6, "inodesFree": 9.0e5,
		},
	})
	want := "\n  ephemeral-storage: 107.3GB\n    rootfs: 1GB/10GB, 9GB still free; 100k/1M inode, 900k inode still free"
	if !strings.Contains(got, want) {
		t.Errorf("got = %q, want substring %q", got, want)
	}
}

// TestNodeAddressesIncludesKubeletNetworkCounters checks the kubelet's interface counters land on
// the node's other network-facing field instead of a standalone stats block.
func TestNodeAddressesIncludesKubeletNetworkCounters(t *testing.T) {
	obj := map[string]interface{}{
		"metadata": map[string]interface{}{"name": "some-node"},
		"status": map[string]interface{}{
			"addresses": []interface{}{
				map[string]interface{}{"type": "InternalIP", "address": "10.0.0.4"},
			},
		},
	}
	r := newNodeRenderable(t, viper.New(), obj)
	got := renderNodeSubTemplate(t, r, "node_addresses", map[string]interface{}{
		"r": r,
		"network": map[string]interface{}{
			"rxBytes": 19.3e9, "txBytes": 7.9e9, "rxErrors": 0.0, "txErrors": 0.0,
		},
	})
	want := "\n  addresses: InternalIP=10.0.0.4\n    network: rx/tx 19.3GB/7.9GB, rx/tx errors 0/0"
	if got != want {
		t.Errorf("got = %q, want %q", got, want)
	}
}

// aksKubeletConfig mirrors a real AKS node's `kubectl get --raw
// /api/v1/nodes/{node}/proxy/configz` -> kubeletconfig (trimmed to the fields the Node templates
// read). It exercises fields that other fixtures/clusters seen so far didn't have set: eviction on
// pid.available, and kubeReserved without ephemeral-storage. The live proxy call itself isn't
// reachable in these offline tests, so templates are rendered directly against this static payload
// instead of going through KubeGetNodeConfigz.
func aksKubeletConfig() map[string]interface{} {
	return map[string]interface{}{
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
	}
}

// TestNodeStatsSummaryFsNoEvictionAnnotationWhenHealthy covers the common case: an eviction-hard
// threshold is configured, but the node isn't anywhere near it. There must be no separate
// "eviction-hard:" block and no annotation on the rootfs line either -- kubectl-status only calls
// out eviction risk where the raw free-space figure already sits, and only when it's actually
// close, per Node.tmpl's node_eviction_annotation doc comment.
func TestNodeStatsSummaryFsNoEvictionAnnotationWhenHealthy(t *testing.T) {
	got := renderNodeStatsSummaryFs(t, map[string]interface{}{
		"fs": map[string]interface{}{
			"usedBytes": 1.0e9, "capacityBytes": 10.0e9, "availableBytes": 9.0e9,
			"inodesUsed": 1.0e5, "inodes": 1.0e6, "inodesFree": 9.0e5,
		},
		"availableThreshold": "10%",
		"inodesThreshold":    "5%",
	})
	want := "1GB/10GB, 9GB still free; 100k/1M inode, 900k inode still free"
	if got != want {
		t.Errorf("got = %q, want %q", got, want)
	}
}

func TestNodeStatsSummaryFsAnnotatesWhenNearingEvictionThreshold(t *testing.T) {
	// threshold 10% free, currently 12% free (900MB/7.5GB): above the threshold but within 1.5x.
	got := renderNodeStatsSummaryFs(t, map[string]interface{}{
		"fs": map[string]interface{}{
			"usedBytes": 6.6e9, "capacityBytes": 7.5e9, "availableBytes": 0.9e9,
			"inodesUsed": 0.0, "inodes": 1.0, "inodesFree": 1.0,
		},
		"availableThreshold": "10%",
		"inodesThreshold":    "",
	})
	if !strings.Contains(got, "nearing 10%: 12% free") {
		t.Errorf("got = %q, want a nearing-threshold annotation on the free-space figure", got)
	}
}

func TestNodeStatsSummaryFsAnnotatesWhenEvictionThresholdTripped(t *testing.T) {
	// threshold 10% free, currently 5% free: already past it.
	got := renderNodeStatsSummaryFs(t, map[string]interface{}{
		"fs": map[string]interface{}{
			"usedBytes": 9.5e9, "capacityBytes": 10.0e9, "availableBytes": 0.5e9,
			"inodesUsed": 0.0, "inodes": 1.0, "inodesFree": 1.0,
		},
		"availableThreshold": "10%",
		"inodesThreshold":    "",
	})
	if !strings.Contains(got, "10% TRIPPED: 5% free") {
		t.Errorf("got = %q, want a tripped-threshold annotation on the free-space figure", got)
	}
}

func TestNodeStatsSummaryFsNoAnnotationWhenThresholdUnconfigured(t *testing.T) {
	// Even a node that's essentially out of disk gets no annotation when eviction-hard doesn't
	// set a threshold for this fs -- there's nothing to correlate the free-space figure against.
	got := renderNodeStatsSummaryFs(t, map[string]interface{}{
		"fs": map[string]interface{}{
			"usedBytes": 9.99e9, "capacityBytes": 10.0e9, "availableBytes": 0.01e9,
			"inodesUsed": 0.0, "inodes": 1.0, "inodesFree": 1.0,
		},
		"availableThreshold": "",
		"inodesThreshold":    "",
	})
	if strings.Contains(got, "nearing") || strings.Contains(got, "TRIPPED") {
		t.Errorf("got = %q, want no eviction annotation when threshold is unconfigured", got)
	}
}

// TestNodeStatsResourcesAnnotatesMemAvailable covers the memory line's "available" figure, the one
// eviction's memory.available threshold actually keys off.
func TestNodeStatsResourcesAnnotatesMemAvailable(t *testing.T) {
	r := newNodeRenderable(t, viper.New(), map[string]interface{}{})
	got := renderNodeSubTemplate(t, r, "node_stats_resources", map[string]interface{}{
		"data": map[string]interface{}{
			"memory": map[string]interface{}{
				"usageBytes": 1.0e9, "workingSetBytes": 1.0e9, "rssBytes": 1.0e9,
				"availableBytes": 0.05e9, "pageFaults": 0.0, "majorPageFaults": 0.0,
			},
		},
		"full":              false,
		"memAvailThreshold": "100Mi",
		"memAvailTotal":     0.0,
	})
	if !strings.Contains(got, "TRIPPED") {
		t.Errorf("got = %q, want a tripped annotation on the available-memory figure (50MB free < 100Mi threshold)", got)
	}
}

// sharedDiskFsStats is the overwhelmingly common node layout: imagefs is a directory on the root
// filesystem, so the kubelet reports the same capacity/free/inode figures for both and only the
// used ones differ.
func sharedDiskFsStats() map[string]interface{} {
	return map[string]interface{}{
		"fs": map[string]interface{}{
			"usedBytes": 37.5e9, "capacityBytes": 310.9e9, "availableBytes": 273.3e9,
			"inodesUsed": 314.9e3, "inodes": 39.1e6, "inodesFree": 38.8e6,
		},
		"imageFs": map[string]interface{}{
			"usedBytes": 22.0e9, "capacityBytes": 310.9e9, "availableBytes": 273.3e9,
			"inodesUsed": 214.9e3, "inodes": 39.1e6, "inodesFree": 38.8e6,
		},
	}
}

func TestNodeStatsFsCollapsesSharedRootfsAndImagefs(t *testing.T) {
	r := newNodeRenderable(t, viper.New(), map[string]interface{}{})
	got := renderNodeSubTemplate(t, r, "node_stats_fs", sharedDiskFsStats())
	want := "\n    rootfs/imagefs: 37.5GB/22GB used of 310.9GB, 273.3GB still free; 314.9k/214.9k of 39.1M inode, 38.8M inode still free"
	if got != want {
		t.Errorf("got = %q, want %q", got, want)
	}
}

// TestNodeStatsFsSplitsWhenImagefsIsASeparateDisk: the moment the two filesystems stop agreeing on
// capacity/free, collapsing them would hide a real difference, so they go back to a line each.
func TestNodeStatsFsSplitsWhenImagefsIsASeparateDisk(t *testing.T) {
	data := sharedDiskFsStats()
	data["imageFs"].(map[string]interface{})["capacityBytes"] = 100.0e9
	data["imageFs"].(map[string]interface{})["availableBytes"] = 78.0e9
	r := newNodeRenderable(t, viper.New(), map[string]interface{}{})
	got := renderNodeSubTemplate(t, r, "node_stats_fs", data)
	for _, want := range []string{
		"\n    rootfs: 37.5GB/310.9GB, 273.3GB still free",
		"\n    imagefs: 22GB/100GB, 78GB still free",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("got = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "rootfs/imagefs") {
		t.Errorf("expected separate lines for filesystems with different capacity, got = %q", got)
	}
}

// nearlyFullSharedDiskFsStats drops the shared filesystem to 8% free, past a 10% eviction
// threshold, so the collapsed line has something to annotate.
func nearlyFullSharedDiskFsStats() map[string]interface{} {
	data := sharedDiskFsStats()
	data["fs"].(map[string]interface{})["availableBytes"] = 24.0e9
	data["imageFs"].(map[string]interface{})["availableBytes"] = 24.0e9
	return data
}

// TestNodeStatsFsAnnotatesCollapsedLineOncePerIdenticalThreshold: nodefs and imagefs being the same
// disk under the same threshold is one fact, so the collapsed line states it once rather than
// printing the identical annotation twice.
func TestNodeStatsFsAnnotatesCollapsedLineOncePerIdenticalThreshold(t *testing.T) {
	data := nearlyFullSharedDiskFsStats()
	data["evictionHard"] = map[string]interface{}{"nodefs.available": "10%", "imagefs.available": "10%"}
	r := newNodeRenderable(t, viper.New(), map[string]interface{}{})
	got := renderNodeSubTemplate(t, r, "node_stats_fs", data)
	if n := strings.Count(got, "TRIPPED"); n != 1 {
		t.Errorf("got = %q, want the shared free-space figure annotated exactly once, got %d annotations", got, n)
	}
}

// TestNodeStatsFsAnnotatesCollapsedLineForEachDistinctThreshold: one shared disk can still sit
// under two different thresholds, and then both are worth saying.
func TestNodeStatsFsAnnotatesCollapsedLineForEachDistinctThreshold(t *testing.T) {
	data := nearlyFullSharedDiskFsStats()
	data["evictionHard"] = map[string]interface{}{"nodefs.available": "10%", "imagefs.available": "15%"}
	r := newNodeRenderable(t, viper.New(), map[string]interface{}{})
	got := renderNodeSubTemplate(t, r, "node_stats_fs", data)
	for _, want := range []string{"10% TRIPPED", "15% TRIPPED"} {
		if !strings.Contains(got, want) {
			t.Errorf("got = %q, want substring %q", got, want)
		}
	}
}

func renderNodeStatsSummaryFs(t *testing.T, data map[string]interface{}) string {
	t.Helper()
	r := newNodeRenderable(t, viper.New(), map[string]interface{}{})
	return renderNodeSubTemplate(t, r, "node_stats_summary_fs", data)
}

func renderNodeKubeletSummary(t *testing.T, health string, kubeletconfig map[string]interface{}) string {
	t.Helper()
	r := newNodeRenderable(t, viper.New(), map[string]interface{}{})
	return renderNodeSubTemplate(t, r, "node_kubelet_summary", map[string]interface{}{
		"health": health, "kubeletconfig": kubeletconfig,
	})
}

// TestNodeKubeletSummaryFoldsHealthAndLogRotationIntoOneLine pins the whole line, since the point
// of it is that reachability and the kubelet's non-default knobs are one line rather than a
// "kubelet health"/"kubelet config" pair of sections.
func TestNodeKubeletSummaryFoldsHealthAndLogRotationIntoOneLine(t *testing.T) {
	got := renderNodeKubeletSummary(t, "ok", aksKubeletConfig())
	want := "\n  kubelet ok, container logs: retains up to 250MB per container (50M x 5 files)"
	if got != want {
		t.Errorf("got = %q, want %q", got, want)
	}
}

// TestNodeKubeletSummaryOmitsReservedAndEviction asserts systemReserved/kubeReserved and
// evictionHard don't leak back onto the kubelet line: they only mean something next to the
// allocatable/free figures they explain, which live on the usage section's lines.
func TestNodeKubeletSummaryOmitsReservedAndEviction(t *testing.T) {
	got := renderNodeKubeletSummary(t, "ok", aksKubeletConfig())
	for _, unwanted := range []string{"reserved", "pid:1000", "eviction", "nodefs"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("expected %q to stay off the kubelet line, got = %q", unwanted, got)
		}
	}
}

// TestNodeKubeletSummaryDefaultPoliciesHidden asserts the manager-policy/pidsLimit/
// shutdownGracePeriod fields stay hidden when they're at their default values, matching the
// codebase's convention of only calling out settings that deviate from the norm.
func TestNodeKubeletSummaryDefaultPoliciesHidden(t *testing.T) {
	got := renderNodeKubeletSummary(t, "ok", aksKubeletConfig())
	for _, unwanted := range []string{"podPidsLimit", "cpuManager", "memoryManager", "topologyManager", "shutdownGracePeriod"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("expected %q to stay hidden at default value, got = %q", unwanted, got)
		}
	}
}

func TestNodeKubeletSummaryShowsNonDefaultPolicies(t *testing.T) {
	kc := aksKubeletConfig()
	kc["podPidsLimit"] = float64(4096)
	kc["cpuManagerPolicy"] = "static"
	kc["shutdownGracePeriod"] = "30s"
	kc["shutdownGracePeriodCriticalPods"] = "10s"
	got := renderNodeKubeletSummary(t, "ok", kc)
	for _, want := range []string{"podPidsLimit:4096", "cpuManager:static", "shutdownGracePeriod:30s/10s(critical)"} {
		if !strings.Contains(got, want) {
			t.Errorf("got = %q, want substring %q", got, want)
		}
	}
}

// TestNodeKubeletSummaryUnreachableKubelet: an apiserver->kubelet proxy failure is the one thing
// this line exists to surface, so it has to survive the fold into a single line.
func TestNodeKubeletSummaryUnreachableKubelet(t *testing.T) {
	got := renderNodeKubeletSummary(t, "unreachable: connection refused", nil)
	if !strings.Contains(got, "unreachable: connection refused") {
		t.Errorf("got = %q, want the healthz failure reported on the kubelet line", got)
	}
}

func TestNodeKubeletSummaryRendersNothingWithoutKubeletApi(t *testing.T) {
	if got := renderNodeKubeletSummary(t, "", nil); got != "" {
		t.Errorf("got = %q, want no kubelet line when the kubelet API wasn't read", got)
	}
}

func TestNodeReservedClause(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sys, kube interface{}
		want      string
	}{
		{"both contributions", "100m", "180m", ", reserved:100m(system)+180m(kube)"},
		{"kube only", nil, "180m", ", reserved:180m(kube)"},
		{"system only", "100m", nil, ", reserved:100m(system)"},
		{"neither", nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newNodeRenderable(t, viper.New(), map[string]interface{}{})
			got := renderNodeSubTemplate(t, r, "node_reserved_clause", map[string]interface{}{
				"sys": tc.sys, "kube": tc.kube,
			})
			if got != tc.want {
				t.Errorf("got = %q, want %q", got, tc.want)
			}
		})
	}
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

// renderObjHealthSummary renders "generic_health_summary" or "resource_health_summary" -- both
// expect dict "obj" (RenderableObject) "callerNamespace" (optional) rather than the bare object
// renderCrossplaneTemplate's callers pass.
func renderObjHealthSummary(t *testing.T, templateName string, obj map[string]interface{}, callerNamespace string) (string, error) {
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
	return r.renderTemplate(templateName, map[string]interface{}{"obj": r, "callerNamespace": callerNamespace})
}

func TestGenericHealthSummaryTemplate(t *testing.T) {
	tests := []struct {
		name string
		obj  map[string]interface{}
		want string
	}{
		{
			name: "Ready condition true falls back to kstatus Current",
			obj: map[string]interface{}{
				"kind":     "Widget",
				"metadata": map[string]interface{}{"name": "my-widget", "generation": int64(2)},
				"status": map[string]interface{}{
					"observedGeneration": int64(2),
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "True"},
					},
				},
			},
			want: "Current: Resource is Ready",
		},
		{
			name: "Ready condition false surfaces its message as InProgress",
			obj: map[string]interface{}{
				"kind":     "Widget",
				"metadata": map[string]interface{}{"name": "my-widget"},
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "False", "reason": "Blocked", "message": "still blocked"},
					},
				},
			},
			want: "InProgress: still blocked",
		},
		{
			name: "bare status.ready boolean is surfaced even without a Ready condition",
			obj: map[string]interface{}{
				"kind":     "Widget",
				"metadata": map[string]interface{}{"name": "my-widget"},
				"status":   map[string]interface{}{"ready": false},
			},
			want: "ready:",
		},
		{
			name: "observedGeneration mismatch at status.observedGeneration is flagged",
			obj: map[string]interface{}{
				"kind":     "Widget",
				"metadata": map[string]interface{}{"name": "my-widget", "generation": int64(3)},
				"status":   map[string]interface{}{"observedGeneration": int64(2)},
			},
			want: "!= gen",
		},
		{
			name: "observedGeneration recorded only on the Ready condition is still flagged (#kstatus doesn't check this)",
			obj: map[string]interface{}{
				"kind":     "Widget",
				"metadata": map[string]interface{}{"name": "my-widget", "generation": int64(5)},
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "True", "observedGeneration": int64(4)},
					},
				},
			},
			want: "!= gen",
		},
		{
			name: "matching observedGeneration on the Ready condition is silent",
			obj: map[string]interface{}{
				"kind":     "Widget",
				"metadata": map[string]interface{}{"name": "my-widget", "generation": int64(4)},
				"status": map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "True", "observedGeneration": int64(4)},
					},
				},
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderObjHealthSummary(t, "generic_health_summary", tt.obj, "")
			if err != nil {
				t.Fatalf("renderTemplate() error = %v", err)
			}
			if tt.want == "" {
				if strings.Contains(got, "!=") {
					t.Errorf("got = %q, want no observed-generation mismatch flag", got)
				}
				return
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("got = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func TestResourceHealthSummaryTemplate_DispatchesToKnownKind(t *testing.T) {
	obj := map[string]interface{}{
		"kind":     "Job",
		"metadata": map[string]interface{}{"name": "my-job", "namespace": "test"},
		"status":   map[string]interface{}{"succeeded": float64(1)},
	}
	got, err := renderObjHealthSummary(t, "resource_health_summary", obj, "test")
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "Succeeded") {
		t.Errorf("got = %q, want job_health_summary's Succeeded flag, not the generic fallback", got)
	}
}

func TestResourceHealthSummaryTemplate_FallsBackToGenericForUnknownKind(t *testing.T) {
	obj := map[string]interface{}{
		"kind":     "Widget",
		"metadata": map[string]interface{}{"name": "my-widget", "namespace": "test"},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
		},
	}
	got, err := renderObjHealthSummary(t, "resource_health_summary", obj, "test")
	if err != nil {
		t.Fatalf("renderTemplate() error = %v", err)
	}
	if !strings.Contains(got, "Current: Resource is Ready") {
		t.Errorf("got = %q, want the generic_health_summary fallback", got)
	}
}
