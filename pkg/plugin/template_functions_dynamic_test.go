package plugin

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/bergerx/kubectl-status/pkg/input"
)

func TestIsSubset(t *testing.T) {
	tests := []struct {
		name string
		a    map[string]string
		b    map[string]string
		want bool
	}{
		{
			name: "empty a is never a subset",
			a:    map[string]string{},
			b:    map[string]string{"app": "foo"},
			want: false,
		},
		{
			name: "nil a is never a subset",
			a:    nil,
			b:    map[string]string{"app": "foo"},
			want: false,
		},
		{
			name: "a equals b",
			a:    map[string]string{"app": "foo"},
			b:    map[string]string{"app": "foo"},
			want: true,
		},
		{
			name: "a is a proper subset of b",
			a:    map[string]string{"app": "foo"},
			b:    map[string]string{"app": "foo", "tier": "backend"},
			want: true,
		},
		{
			name: "key in a missing from b",
			a:    map[string]string{"app": "foo", "tier": "backend"},
			b:    map[string]string{"app": "foo"},
			want: false,
		},
		{
			name: "key present in both but value mismatches",
			a:    map[string]string{"app": "foo"},
			b:    map[string]string{"app": "bar"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSubset(tt.a, tt.b); got != tt.want {
				t.Errorf("isSubset(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDoesServiceMatchLabels(t *testing.T) {
	tests := []struct {
		name   string
		svc    corev1.Service
		labels map[string]string
		want   bool
	}{
		{
			name: "selector is a subset of the pod labels",
			svc: corev1.Service{Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "foo"},
			}},
			labels: map[string]string{"app": "foo", "tier": "backend"},
			want:   true,
		},
		{
			name: "selector does not match",
			svc: corev1.Service{Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "bar"},
			}},
			labels: map[string]string{"app": "foo"},
			want:   false,
		},
		{
			name: "empty selector never matches, even with no labels to compare against",
			svc: corev1.Service{Spec: corev1.ServiceSpec{
				Selector: map[string]string{},
			}},
			labels: map[string]string{},
			want:   false,
		},
		{
			name: "ExternalName services are excluded regardless of selector",
			svc: corev1.Service{Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeExternalName,
				Selector: map[string]string{"app": "foo"},
			}},
			labels: map[string]string{"app": "foo"},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doesServiceMatchLabels(tt.svc, tt.labels); got != tt.want {
				t.Errorf("doesServiceMatchLabels() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDoesIngressUseService(t *testing.T) {
	ingressWithBackend := func(svcName string) netv1.Ingress {
		return netv1.Ingress{Spec: netv1.IngressSpec{
			Rules: []netv1.IngressRule{
				{
					IngressRuleValue: netv1.IngressRuleValue{
						HTTP: &netv1.HTTPIngressRuleValue{
							Paths: []netv1.HTTPIngressPath{
								{
									Backend: netv1.IngressBackend{
										Service: &netv1.IngressServiceBackend{Name: svcName},
									},
								},
							},
						},
					},
				},
			},
		}}
	}

	tests := []struct {
		name    string
		ing     netv1.Ingress
		svcName string
		want    bool
	}{
		{
			name:    "rule's backend references the service",
			ing:     ingressWithBackend("web"),
			svcName: "web",
			want:    true,
		},
		{
			name:    "rule's backend references a different service",
			ing:     ingressWithBackend("web"),
			svcName: "other",
			want:    false,
		},
		{
			name:    "no rules at all",
			ing:     netv1.Ingress{},
			svcName: "web",
			want:    false,
		},
		{
			name: "rule with no HTTP value is skipped",
			ing: netv1.Ingress{Spec: netv1.IngressSpec{
				Rules: []netv1.IngressRule{{}},
			}},
			svcName: "web",
			want:    false,
		},
		{
			name: "second path in the same rule matches",
			ing: netv1.Ingress{Spec: netv1.IngressSpec{
				Rules: []netv1.IngressRule{
					{
						IngressRuleValue: netv1.IngressRuleValue{
							HTTP: &netv1.HTTPIngressRuleValue{
								Paths: []netv1.HTTPIngressPath{
									{Backend: netv1.IngressBackend{Service: &netv1.IngressServiceBackend{Name: "other"}}},
									{Backend: netv1.IngressBackend{Service: &netv1.IngressServiceBackend{Name: "web"}}},
								},
							},
						},
					},
				},
			}},
			svcName: "web",
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doesIngressUseService(tt.ing, tt.svcName); got != tt.want {
				t.Errorf("doesIngressUseService() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDoesRouteUseService(t *testing.T) {
	tests := []struct {
		name           string
		obj            input.Object
		routeNamespace string
		svcName        string
		want           bool
	}{
		{
			name:           "no spec.rules at all",
			obj:            input.Object{},
			routeNamespace: "default",
			svcName:        "web",
			want:           false,
		},
		{
			name: "backendRef matches by name only, no kind/group/namespace set",
			obj: input.Object{"spec": map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "web"},
					}},
				},
			}},
			routeNamespace: "default",
			svcName:        "web",
			want:           true,
		},
		{
			name: "backendRef name does not match",
			obj: input.Object{"spec": map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "other"},
					}},
				},
			}},
			routeNamespace: "default",
			svcName:        "web",
			want:           false,
		},
		{
			name: "explicit kind Service still matches",
			obj: input.Object{"spec": map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "web", "kind": "Service"},
					}},
				},
			}},
			routeNamespace: "default",
			svcName:        "web",
			want:           true,
		},
		{
			name: "non-Service kind is excluded",
			obj: input.Object{"spec": map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "web", "kind": "ServiceImport"},
					}},
				},
			}},
			routeNamespace: "default",
			svcName:        "web",
			want:           false,
		},
		{
			name: "non-empty group is excluded, core Service has no group",
			obj: input.Object{"spec": map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "web", "group": "example.com"},
					}},
				},
			}},
			routeNamespace: "default",
			svcName:        "web",
			want:           false,
		},
		{
			name: "explicit namespace matching the route's own namespace matches",
			obj: input.Object{"spec": map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "web", "namespace": "default"},
					}},
				},
			}},
			routeNamespace: "default",
			svcName:        "web",
			want:           true,
		},
		{
			name: "explicit namespace referencing a different namespace is excluded",
			obj: input.Object{"spec": map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "web", "namespace": "other-ns"},
					}},
				},
			}},
			routeNamespace: "default",
			svcName:        "web",
			want:           false,
		},
		{
			name: "second rule's backendRef matches",
			obj: input.Object{"spec": map[string]interface{}{
				"rules": []interface{}{
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "other"},
					}},
					map[string]interface{}{"backendRefs": []interface{}{
						map[string]interface{}{"name": "web"},
					}},
				},
			}},
			routeNamespace: "default",
			svcName:        "web",
			want:           true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doesRouteUseService(tt.obj, tt.routeNamespace, tt.svcName); got != tt.want {
				t.Errorf("doesRouteUseService() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestStatefulSetPodOrdinal and TestStatefulSetPodUnreadySince used to live in
// template_functions_static_test.go, even though both functions they exercise are defined in
// template_functions_dynamic.go -- moved here to colocate each test with its source file, per #806.

func TestStatefulSetPodOrdinal(t *testing.T) {
	tests := []struct {
		name       string
		namePrefix string
		podName    string
		wantN      int
		wantOK     bool
	}{
		{name: "valid ordinal", namePrefix: "web-", podName: "web-2", wantN: 2, wantOK: true},
		{name: "ordinal zero", namePrefix: "web-", podName: "web-0", wantN: 0, wantOK: true},
		{name: "non-numeric suffix", namePrefix: "web-", podName: "web-abc", wantN: 0, wantOK: false},
		{name: "name not prefixed by the StatefulSet name", namePrefix: "web-", podName: "other-2", wantN: 0, wantOK: false},
		{name: "prefix with no suffix", namePrefix: "web-", podName: "web-", wantN: 0, wantOK: false},
		{name: "STS name that's itself a prefix of another STS's pods", namePrefix: "web-", podName: "web-canary-2", wantN: 0, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotN, gotOK := statefulSetPodOrdinal(tt.namePrefix, tt.podName)
			if gotN != tt.wantN || gotOK != tt.wantOK {
				t.Errorf("statefulSetPodOrdinal(%q, %q) = (%d, %v), want (%d, %v)", tt.namePrefix, tt.podName, gotN, gotOK, tt.wantN, tt.wantOK)
			}
		})
	}
}

func TestStatefulSetPodUnreadySince(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	podWithCreationTime := RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"creationTimestamp": created.Format(time.RFC3339)},
	}}}

	tests := []struct {
		name           string
		pod            RenderableObject
		readyCondition map[string]interface{}
		want           time.Time
	}{
		{
			name:           "uses Ready condition's lastTransitionTime when present",
			pod:            podWithCreationTime,
			readyCondition: map[string]interface{}{"lastTransitionTime": "2026-01-02T00:00:00Z"},
			want:           time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name:           "falls back to Pod creation time when no Ready condition",
			pod:            podWithCreationTime,
			readyCondition: map[string]interface{}{},
			want:           created,
		},
		{
			name:           "falls back to Pod creation time on unparseable lastTransitionTime",
			pod:            podWithCreationTime,
			readyCondition: map[string]interface{}{"lastTransitionTime": "not-a-time"},
			want:           created,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statefulSetPodUnreadySince(tt.pod, tt.readyCondition)
			if !got.Equal(tt.want) {
				t.Errorf("statefulSetPodUnreadySince() = %v, want %v", got, tt.want)
			}
		})
	}
}
