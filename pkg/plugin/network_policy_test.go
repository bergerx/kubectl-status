package plugin

import (
	"reflect"
	"testing"
)

func TestNetworkPolicySelectsPod(t *testing.T) {
	tests := []struct {
		name      string
		spec      map[string]interface{}
		podLabels map[string]string
		want      bool
	}{
		{
			name:      "empty podSelector matches every pod",
			spec:      map[string]interface{}{"podSelector": map[string]interface{}{}},
			podLabels: map[string]string{"app": "foo"},
			want:      true,
		},
		{
			name: "matchLabels subset matches",
			spec: map[string]interface{}{"podSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"app": "foo"},
			}},
			podLabels: map[string]string{"app": "foo", "tier": "backend"},
			want:      true,
		},
		{
			name: "matchLabels mismatch does not match",
			spec: map[string]interface{}{"podSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"app": "bar"},
			}},
			podLabels: map[string]string{"app": "foo"},
			want:      false,
		},
		{
			name: "matchExpressions In matches",
			spec: map[string]interface{}{"podSelector": map[string]interface{}{
				"matchExpressions": []interface{}{
					map[string]interface{}{"key": "tier", "operator": "In", "values": []interface{}{"backend", "frontend"}},
				},
			}},
			podLabels: map[string]string{"tier": "backend"},
			want:      true,
		},
		{
			name: "matchExpressions DoesNotExist fails when key present",
			spec: map[string]interface{}{"podSelector": map[string]interface{}{
				"matchExpressions": []interface{}{
					map[string]interface{}{"key": "tier", "operator": "DoesNotExist"},
				},
			}},
			podLabels: map[string]string{"tier": "backend"},
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := networkPolicySelectsPod(tt.spec, tt.podLabels); got != tt.want {
				t.Errorf("networkPolicySelectsPod() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGatekeeperConstraintMatchesNamespace(t *testing.T) {
	tests := []struct {
		name      string
		matchSpec map[string]interface{}
		nsName    string
		nsLabels  map[string]string
		want      bool
	}{
		{
			name:      "no match fields at all -- applies to every namespace",
			matchSpec: map[string]interface{}{},
			nsName:    "team-a",
			want:      true,
		},
		{
			name:      "scope: Cluster never applies to a namespace's own objects",
			matchSpec: map[string]interface{}{"scope": "Cluster"},
			nsName:    "team-a",
			want:      false,
		},
		{
			name:      "scope: Namespaced is unrestricted, same as unset",
			matchSpec: map[string]interface{}{"scope": "Namespaced"},
			nsName:    "team-a",
			want:      true,
		},
		{
			name:      "namespaces allowlist includes this namespace",
			matchSpec: map[string]interface{}{"namespaces": []interface{}{"team-a", "team-b"}},
			nsName:    "team-a",
			want:      true,
		},
		{
			name:      "namespaces allowlist excludes this namespace",
			matchSpec: map[string]interface{}{"namespaces": []interface{}{"team-b"}},
			nsName:    "team-a",
			want:      false,
		},
		{
			name:      "excludedNamespaces excludes this namespace",
			matchSpec: map[string]interface{}{"excludedNamespaces": []interface{}{"team-a"}},
			nsName:    "team-a",
			want:      false,
		},
		{
			name:      "excludedNamespaces does not name this namespace",
			matchSpec: map[string]interface{}{"excludedNamespaces": []interface{}{"team-b"}},
			nsName:    "team-a",
			want:      true,
		},
		{
			name: "namespaceSelector matches this namespace's labels",
			matchSpec: map[string]interface{}{"namespaceSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"env": "prod"},
			}},
			nsName:   "team-a",
			nsLabels: map[string]string{"env": "prod"},
			want:     true,
		},
		{
			name: "namespaceSelector does not match this namespace's labels",
			matchSpec: map[string]interface{}{"namespaceSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{"env": "prod"},
			}},
			nsName:   "team-a",
			nsLabels: map[string]string{"env": "staging"},
			want:     false,
		},
		{
			name:      "empty namespaceSelector matches every namespace",
			matchSpec: map[string]interface{}{"namespaceSelector": map[string]interface{}{}},
			nsName:    "team-a",
			nsLabels:  map[string]string{"env": "staging"},
			want:      true,
		},
		{
			name: "namespaces allowlist and namespaceSelector both have to pass",
			matchSpec: map[string]interface{}{
				"namespaces": []interface{}{"team-a"},
				"namespaceSelector": map[string]interface{}{
					"matchLabels": map[string]interface{}{"env": "prod"},
				},
			},
			nsName:   "team-a",
			nsLabels: map[string]string{"env": "staging"},
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gatekeeperConstraintMatchesNamespace(tt.matchSpec, tt.nsName, tt.nsLabels); got != tt.want {
				t.Errorf("gatekeeperConstraintMatchesNamespace() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNetworkPolicyPolicyTypes(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]interface{}
		want []string
	}{
		{
			name: "no policyTypes, no egress -- defaults to Ingress only",
			spec: map[string]interface{}{},
			want: []string{"Ingress"},
		},
		{
			name: "no policyTypes, has egress -- defaults to Ingress and Egress",
			spec: map[string]interface{}{"egress": []interface{}{map[string]interface{}{}}},
			want: []string{"Ingress", "Egress"},
		},
		{
			name: "explicit policyTypes is used as-is",
			spec: map[string]interface{}{"policyTypes": []interface{}{"Egress"}},
			want: []string{"Egress"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := networkPolicyPolicyTypes(tt.spec)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("networkPolicyPolicyTypes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCiliumPolicySelectsPod(t *testing.T) {
	tests := []struct {
		name           string
		obj            map[string]interface{}
		podLabels      map[string]string
		wantMatches    bool
		wantDirections []string
	}{
		{
			name: "empty endpointSelector matches every pod, no rule lists means no restriction",
			obj: map[string]interface{}{"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{},
			}},
			podLabels:      map[string]string{"app": "foo"},
			wantMatches:    true,
			wantDirections: nil,
		},
		{
			name: "absent endpointSelector key (not just an empty map) also matches every pod",
			obj: map[string]interface{}{"spec": map[string]interface{}{
				"ingress": []interface{}{map[string]interface{}{}},
			}},
			podLabels:      map[string]string{"app": "foo"},
			wantMatches:    true,
			wantDirections: []string{"ingress"},
		},
		{
			name: "matchLabels mismatch does not match",
			obj: map[string]interface{}{"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "bar"}},
			}},
			podLabels:   map[string]string{"app": "foo"},
			wantMatches: false,
		},
		{
			name: "ingress rule list restricts ingress only",
			obj: map[string]interface{}{"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "foo"}},
				"ingress":          []interface{}{map[string]interface{}{}},
			}},
			podLabels:      map[string]string{"app": "foo"},
			wantMatches:    true,
			wantDirections: []string{"ingress"},
		},
		{
			name: "ingressDeny also restricts ingress",
			obj: map[string]interface{}{"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{},
				"ingressDeny":      []interface{}{map[string]interface{}{}},
			}},
			podLabels:      map[string]string{"app": "foo"},
			wantMatches:    true,
			wantDirections: []string{"ingress"},
		},
		{
			name: "egress and egressDeny restrict egress",
			obj: map[string]interface{}{"spec": map[string]interface{}{
				"endpointSelector": map[string]interface{}{},
				"egress":           []interface{}{map[string]interface{}{}},
			}},
			podLabels:      map[string]string{"app": "foo"},
			wantMatches:    true,
			wantDirections: []string{"egress"},
		},
		{
			name: "specs (multi-rule) is checked in addition to spec",
			obj: map[string]interface{}{
				"specs": []interface{}{
					map[string]interface{}{
						"endpointSelector": map[string]interface{}{"matchLabels": map[string]interface{}{"app": "foo"}},
						"egress":           []interface{}{map[string]interface{}{}},
					},
				},
			},
			podLabels:      map[string]string{"app": "foo"},
			wantMatches:    true,
			wantDirections: []string{"egress"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatches, gotDirections := ciliumPolicySelectsPod(tt.obj, tt.podLabels)
			if gotMatches != tt.wantMatches {
				t.Errorf("ciliumPolicySelectsPod() matches = %v, want %v", gotMatches, tt.wantMatches)
			}
			if !reflect.DeepEqual(gotDirections, tt.wantDirections) {
				t.Errorf("ciliumPolicySelectsPod() directions = %v, want %v", gotDirections, tt.wantDirections)
			}
		})
	}
}

func TestCalicoPolicyTypes(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]interface{}
		want []string
	}{
		{
			name: "no types, no egress -- defaults to Ingress only",
			spec: map[string]interface{}{},
			want: []string{"Ingress"},
		},
		{
			name: "nil spec -- defaults to Ingress only (reading a nil map is safe in Go)",
			spec: nil,
			want: []string{"Ingress"},
		},
		{
			name: "no types, has egress -- defaults to Ingress and Egress",
			spec: map[string]interface{}{"egress": []interface{}{map[string]interface{}{}}},
			want: []string{"Ingress", "Egress"},
		},
		{
			name: "explicit types is used as-is",
			spec: map[string]interface{}{"types": []interface{}{"Egress"}},
			want: []string{"Egress"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calicoPolicyTypes(tt.spec)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("calicoPolicyTypes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCalicoPolicySelectsPod(t *testing.T) {
	tests := []struct {
		name      string
		spec      map[string]interface{}
		podLabels map[string]string
		namespace string
		want      bool
	}{
		{
			name:      "empty selector matches every pod",
			spec:      map[string]interface{}{"selector": ""},
			podLabels: map[string]string{"app": "foo"},
			namespace: "default",
			want:      true,
		},
		{
			name:      "selector matches pod label",
			spec:      map[string]interface{}{"selector": "app == 'foo'"},
			podLabels: map[string]string{"app": "foo"},
			namespace: "default",
			want:      true,
		},
		{
			name:      "selector mismatches pod label",
			spec:      map[string]interface{}{"selector": "app == 'bar'"},
			podLabels: map[string]string{"app": "foo"},
			namespace: "default",
			want:      false,
		},
		{
			name:      "selector matches the synthetic namespace label",
			spec:      map[string]interface{}{"selector": "projectcalico.org/namespace == 'prod'"},
			podLabels: map[string]string{"app": "foo"},
			namespace: "prod",
			want:      true,
		},
		{
			name:      "unparseable selector conservatively does not match",
			spec:      map[string]interface{}{"selector": "((("},
			podLabels: map[string]string{"app": "foo"},
			namespace: "default",
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calicoPolicySelectsPod(tt.spec, tt.podLabels, tt.namespace); got != tt.want {
				t.Errorf("calicoPolicySelectsPod() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCalicoNamespaceSelectorMatches(t *testing.T) {
	tests := []struct {
		name      string
		spec      map[string]interface{}
		namespace string
		nsLabels  map[string]string
		want      bool
	}{
		{
			name:      "empty namespaceSelector matches every namespace",
			spec:      map[string]interface{}{},
			namespace: "prod",
			nsLabels:  map[string]string{},
			want:      true,
		},
		{
			name:      "namespaceSelector matches a namespace label",
			spec:      map[string]interface{}{"namespaceSelector": "env == 'prod'"},
			namespace: "prod-ns",
			nsLabels:  map[string]string{"env": "prod"},
			want:      true,
		},
		{
			name:      "namespaceSelector matches the synthetic name label",
			spec:      map[string]interface{}{"namespaceSelector": "projectcalico.org/name == 'prod-ns'"},
			namespace: "prod-ns",
			nsLabels:  map[string]string{},
			want:      true,
		},
		{
			name:      "namespaceSelector mismatch",
			spec:      map[string]interface{}{"namespaceSelector": "env == 'prod'"},
			namespace: "dev-ns",
			nsLabels:  map[string]string{"env": "dev"},
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calicoNamespaceSelectorMatches(tt.spec, tt.namespace, tt.nsLabels); got != tt.want {
				t.Errorf("calicoNamespaceSelectorMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}
