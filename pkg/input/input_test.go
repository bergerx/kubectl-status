package input

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/viper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest/fake"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"
)

func newTestFactory() *cmdtesting.TestFactory {
	f := cmdtesting.NewTestFactory().WithNamespace("test")
	f.Client = &fake.RESTClient{}
	f.UnstructuredClient = f.Client
	return f
}

func TestOwnersOrphanDetection(t *testing.T) {
	f := newTestFactory()
	t.Cleanup(func() { f.Cleanup() })
	repo, err := NewResourceRepo(f, viper.New())
	if err != nil {
		t.Fatal(err)
	}

	existingRS := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]interface{}{
			"name":      "existing-rs",
			"namespace": "test",
		},
	}}
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	dynClient, err := f.DynamicClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dynClient.Resource(gvr).Namespace("test").Create(context.TODO(), existingRS, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	obj := Object{
		"metadata": map[string]interface{}{
			"namespace": "test",
			"ownerReferences": []interface{}{
				map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "ReplicaSet",
					"name":       "existing-rs",
				},
				map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "ReplicaSet",
					"name":       "missing-rs",
				},
			},
		},
	}

	owners, orphans, err := repo.Owners(obj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(owners) != 1 || owners[0].Unstructured().GetName() != "existing-rs" {
		t.Fatalf("expected 1 resolved owner (existing-rs), got %+v", owners)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d: %+v", len(orphans), orphans)
	}
	if orphans[0].Name != "missing-rs" {
		t.Errorf("expected orphan name missing-rs, got %s", orphans[0].Name)
	}
}

func TestOwnersNoOwnerReferences(t *testing.T) {
	f := newTestFactory()
	t.Cleanup(func() { f.Cleanup() })
	repo, err := NewResourceRepo(f, viper.New())
	if err != nil {
		t.Fatal(err)
	}

	owners, orphans, err := repo.Owners(Object{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(owners) != 0 || len(orphans) != 0 {
		t.Errorf("expected no owners and no orphans, got owners=%+v orphans=%+v", owners, orphans)
	}
}

// TestOwnersClusterScopedOwner guards against the dynamic client's Get being scope-blind: a
// namespaced object referencing an existing cluster-scoped owner (e.g. a Node) must not be
// misreported as orphaned just because the lookup path defaults to namespaced.
func TestOwnersClusterScopedOwner(t *testing.T) {
	f := newTestFactory()
	t.Cleanup(func() { f.Cleanup() })
	repo, err := NewResourceRepo(f, viper.New())
	if err != nil {
		t.Fatal(err)
	}

	existingNode := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Node",
		"metadata": map[string]interface{}{
			"name": "existing-node",
		},
	}}
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}
	dynClient, err := f.DynamicClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dynClient.Resource(gvr).Create(context.TODO(), existingNode, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	obj := Object{
		"metadata": map[string]interface{}{
			"namespace": "test",
			"ownerReferences": []interface{}{
				map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Node",
					"name":       "existing-node",
				},
			},
		},
	}

	owners, orphans, err := repo.Owners(obj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("existing cluster-scoped owner wrongly reported as orphan: %+v", orphans)
	}
	if len(owners) != 1 {
		t.Fatalf("expected the cluster-scoped owner to resolve, got %d owners", len(owners))
	}
}

// newAPIServiceFactory builds a test factory whose dynamic client can Get/Create
// apiregistration.k8s.io APIService objects -- a group client-go's own scheme doesn't know about,
// so the fake dynamic client needs an explicit List-kind mapping for it.
func newAPIServiceFactory(t *testing.T, apiService *unstructured.Unstructured) *cmdtesting.TestFactory {
	t.Helper()
	f := newTestFactory()
	objs := []runtime.Object{}
	if apiService != nil {
		objs = append(objs, apiService)
	}
	f.FakeDynamicClient = fakedynamic.NewSimpleDynamicClientWithCustomListKinds(
		scheme.Scheme,
		map[schema.GroupVersionResource]string{metricsAPIServiceGVR: "APIServiceList"},
		objs...,
	)
	return f
}

func apiServiceObj(available bool, message string) *unstructured.Unstructured {
	status := "False"
	if available {
		status = "True"
	}
	condition := map[string]interface{}{"type": "Available", "status": status}
	if message != "" {
		condition["message"] = message
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiregistration.k8s.io/v1",
		"kind":       "APIService",
		"metadata": map[string]interface{}{
			"name": "v1beta1.metrics.k8s.io",
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{condition},
		},
	}}
}

// TestMetricsUnavailableReason verifies that ResourceRepo.MetricsUnavailableReason distinguishes
// three cluster states -- metrics-server's APIService missing entirely (not installed), present
// but unhealthy (Available=False, surfacing the condition's own diagnostic message), and healthy
// (Available=True) -- and that the result is cached rather than re-checked on every call.
func TestMetricsUnavailableReason(t *testing.T) {
	t.Run("not installed when the APIService doesn't exist", func(t *testing.T) {
		f := newAPIServiceFactory(t, nil)
		t.Cleanup(func() { f.Cleanup() })
		repo, err := NewResourceRepo(f, viper.New())
		if err != nil {
			t.Fatal(err)
		}
		reason := repo.MetricsUnavailableReason()
		if !strings.Contains(reason, "not installed") {
			t.Errorf("expected reason to mention metrics-server isn't installed, got %q", reason)
		}
	})

	t.Run("unavailable, surfacing the condition's message, when the APIService exists but isn't Available", func(t *testing.T) {
		f := newAPIServiceFactory(t, apiServiceObj(false, "no endpoints available for service \"metrics-server\""))
		t.Cleanup(func() { f.Cleanup() })
		repo, err := NewResourceRepo(f, viper.New())
		if err != nil {
			t.Fatal(err)
		}
		reason := repo.MetricsUnavailableReason()
		if !strings.Contains(reason, "no endpoints available for service \"metrics-server\"") {
			t.Errorf("expected reason to surface the Available condition's message, got %q", reason)
		}
	})

	t.Run("empty when the APIService is Available", func(t *testing.T) {
		f := newAPIServiceFactory(t, apiServiceObj(true, ""))
		t.Cleanup(func() { f.Cleanup() })
		repo, err := NewResourceRepo(f, viper.New())
		if err != nil {
			t.Fatal(err)
		}
		if reason := repo.MetricsUnavailableReason(); reason != "" {
			t.Errorf("expected empty reason when the APIService's Available condition is True, got %q", reason)
		}
	})

	t.Run("result is cached", func(t *testing.T) {
		f := newAPIServiceFactory(t, apiServiceObj(true, ""))
		t.Cleanup(func() { f.Cleanup() })
		repo, err := NewResourceRepo(f, viper.New())
		if err != nil {
			t.Fatal(err)
		}
		if reason := repo.MetricsUnavailableReason(); reason != "" {
			t.Fatalf("expected empty reason, got %q", reason)
		}
		// Deleting the APIService out from under the repo must not change the cached result.
		if err := f.FakeDynamicClient.Resource(metricsAPIServiceGVR).Delete(context.TODO(), "v1beta1.metrics.k8s.io", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if reason := repo.MetricsUnavailableReason(); reason != "" {
			t.Errorf("expected cached empty reason to stick even after the underlying APIService changed, got %q", reason)
		}
	})
}

// TestOwnersResolutionIsCached verifies that resolving the same ownerReference for two different
// objects (e.g. two Pods owned by the same ReplicaSet) only performs a single API lookup.
func TestOwnersResolutionIsCached(t *testing.T) {
	f := newTestFactory()
	t.Cleanup(func() { f.Cleanup() })
	repo, err := NewResourceRepo(f, viper.New())
	if err != nil {
		t.Fatal(err)
	}

	existingRS := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]interface{}{
			"name":      "existing-rs",
			"namespace": "test",
		},
	}}
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	dynClient, err := f.DynamicClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dynClient.Resource(gvr).Namespace("test").Create(context.TODO(), existingRS, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.FakeDynamicClient.ClearActions()

	ownerRef := map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"name":       "existing-rs",
	}
	pod1 := Object{"metadata": map[string]interface{}{"namespace": "test", "ownerReferences": []interface{}{ownerRef}}}
	pod2 := Object{"metadata": map[string]interface{}{"namespace": "test", "ownerReferences": []interface{}{ownerRef}}}

	if _, _, err := repo.Owners(pod1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Owners(pod2); err != nil {
		t.Fatal(err)
	}

	getActions := 0
	for _, action := range f.FakeDynamicClient.Actions() {
		if action.GetVerb() == "get" {
			getActions++
		}
	}
	if getActions != 1 {
		t.Errorf("expected owner resolution to be cached across objects (1 get action), got %d", getActions)
	}
}
