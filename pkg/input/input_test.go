package input

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	repo, err := NewResourceRepo(f)
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
	repo, err := NewResourceRepo(f)
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
	repo, err := NewResourceRepo(f)
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

// TestOwnersResolutionIsCached verifies that resolving the same ownerReference for two different
// objects (e.g. two Pods owned by the same ReplicaSet) only performs a single API lookup.
func TestOwnersResolutionIsCached(t *testing.T) {
	f := newTestFactory()
	t.Cleanup(func() { f.Cleanup() })
	repo, err := NewResourceRepo(f)
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
