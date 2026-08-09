package plugin

import (
	"reflect"
	"testing"
)

func TestTaintsNotToleratedByPod(t *testing.T) {
	noSchedule := map[string]interface{}{"key": "dedicated", "value": "gpu", "effect": "NoSchedule"}
	noExecute := map[string]interface{}{"key": "node.kubernetes.io/not-ready", "effect": "NoExecute"}
	preferNoSchedule := map[string]interface{}{"key": "spot", "effect": "PreferNoSchedule"}

	tests := []struct {
		name        string
		nodeTaints  []interface{}
		tolerations []interface{}
		want        []interface{}
	}{
		{
			name:        "no taints",
			nodeTaints:  nil,
			tolerations: nil,
			want:        nil,
		},
		{
			name:        "PreferNoSchedule is never a blocker",
			nodeTaints:  []interface{}{preferNoSchedule},
			tolerations: nil,
			want:        nil,
		},
		{
			name:        "untolerated NoSchedule blocks",
			nodeTaints:  []interface{}{noSchedule},
			tolerations: nil,
			want:        []interface{}{noSchedule},
		},
		{
			name:       "Equal toleration with matching key/value tolerates",
			nodeTaints: []interface{}{noSchedule},
			tolerations: []interface{}{
				map[string]interface{}{"key": "dedicated", "operator": "Equal", "value": "gpu", "effect": "NoSchedule"},
			},
			want: nil,
		},
		{
			name:       "Equal toleration with mismatched value does not tolerate",
			nodeTaints: []interface{}{noSchedule},
			tolerations: []interface{}{
				map[string]interface{}{"key": "dedicated", "operator": "Equal", "value": "cpu", "effect": "NoSchedule"},
			},
			want: []interface{}{noSchedule},
		},
		{
			name:       "Exists toleration with matching key tolerates regardless of value",
			nodeTaints: []interface{}{noSchedule},
			tolerations: []interface{}{
				map[string]interface{}{"key": "dedicated", "operator": "Exists", "effect": "NoSchedule"},
			},
			want: nil,
		},
		{
			name:       "Exists toleration with empty key tolerates everything of that effect",
			nodeTaints: []interface{}{noExecute},
			tolerations: []interface{}{
				map[string]interface{}{"operator": "Exists", "effect": "NoExecute"},
			},
			want: nil,
		},
		{
			name:       "toleration with wrong effect does not tolerate",
			nodeTaints: []interface{}{noSchedule},
			tolerations: []interface{}{
				map[string]interface{}{"key": "dedicated", "operator": "Equal", "value": "gpu", "effect": "NoExecute"},
			},
			want: []interface{}{noSchedule},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taintsNotToleratedByPod(tt.nodeTaints, tt.tolerations)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("taintsNotToleratedByPod() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatNodeSelector(t *testing.T) {
	tests := []struct {
		name         string
		nodeSelector map[string]interface{}
		want         string
	}{
		{
			name:         "empty",
			nodeSelector: map[string]interface{}{},
			want:         "",
		},
		{
			name:         "single key",
			nodeSelector: map[string]interface{}{"disktype": "ssd"},
			want:         "disktype=ssd",
		},
		{
			name:         "multiple keys are sorted",
			nodeSelector: map[string]interface{}{"zone": "us-east-1a", "disktype": "ssd"},
			want:         "disktype=ssd,zone=us-east-1a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatNodeSelector(tt.nodeSelector); got != tt.want {
				t.Errorf("formatNodeSelector() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatNodeSelectorTerms(t *testing.T) {
	tests := []struct {
		name  string
		terms []interface{}
		want  string
	}{
		{
			name:  "no terms",
			terms: nil,
			want:  "",
		},
		{
			name: "single term, single expression",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "disktype", "operator": "In", "values": []interface{}{"ssd"}},
				}},
			},
			want: "disktype in (ssd)",
		},
		{
			name: "single term, multiple AND'd expressions and fields",
			terms: []interface{}{
				map[string]interface{}{
					"matchExpressions": []interface{}{
						map[string]interface{}{"key": "zone", "operator": "In", "values": []interface{}{"a", "b"}},
						map[string]interface{}{"key": "gpu", "operator": "DoesNotExist"},
					},
					"matchFields": []interface{}{
						map[string]interface{}{"key": "metadata.name", "operator": "NotIn", "values": []interface{}{"node-1"}},
					},
				},
			},
			want: "zone in (a,b),!gpu,metadata.name notin (node-1)",
		},
		{
			name: "multiple OR'd terms are parenthesized",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "zone", "operator": "In", "values": []interface{}{"a"}},
				}},
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "zone", "operator": "In", "values": []interface{}{"b"}},
				}},
			},
			want: "(zone in (a)) or (zone in (b))",
		},
		{
			name: "Gt and Lt operators",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "cores", "operator": "Gt", "values": []interface{}{"4"}},
					map[string]interface{}{"key": "cores", "operator": "Lt", "values": []interface{}{"32"}},
				}},
			},
			want: "cores>4,cores<32",
		},
		{
			name: "Exists operator",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "gpu", "operator": "Exists"},
				}},
			},
			want: "gpu",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatNodeSelectorTerms(tt.terms); got != tt.want {
				t.Errorf("formatNodeSelectorTerms() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNodeSelectorTermsMatch(t *testing.T) {
	inTerm := func(key string, values ...string) map[string]interface{} {
		vs := make([]interface{}, len(values))
		for i, v := range values {
			vs[i] = v
		}
		return map[string]interface{}{"key": key, "operator": "In", "values": vs}
	}

	tests := []struct {
		name       string
		terms      []interface{}
		nodeLabels map[string]string
		nodeFields map[string]string
		want       bool
	}{
		{
			name:       "empty selector matches everything",
			terms:      nil,
			nodeLabels: map[string]string{},
			want:       true,
		},
		{
			name: "single term matches",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{inTerm("zone", "a")}},
			},
			nodeLabels: map[string]string{"zone": "a"},
			want:       true,
		},
		{
			name: "single term does not match",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{inTerm("zone", "a")}},
			},
			nodeLabels: map[string]string{"zone": "b"},
			want:       false,
		},
		{
			name: "multiple OR'd terms, second matches",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{inTerm("zone", "a")}},
				map[string]interface{}{"matchExpressions": []interface{}{inTerm("zone", "b")}},
			},
			nodeLabels: map[string]string{"zone": "b"},
			want:       true,
		},
		{
			name: "multiple AND'd expressions within a term, one fails",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					inTerm("zone", "a"),
					map[string]interface{}{"key": "gpu", "operator": "Exists"},
				}},
			},
			nodeLabels: map[string]string{"zone": "a"},
			want:       false,
		},
		{
			name: "matchFields checked against nodeFields, not nodeLabels",
			terms: []interface{}{
				map[string]interface{}{"matchFields": []interface{}{inTerm("metadata.name", "node-1")}},
			},
			nodeFields: map[string]string{"metadata.name": "node-1"},
			want:       true,
		},
		{
			name: "In: key absent does not match",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{inTerm("zone", "a")}},
			},
			nodeLabels: map[string]string{},
			want:       false,
		},
		{
			name: "NotIn: key absent matches",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "zone", "operator": "NotIn", "values": []interface{}{"a"}},
				}},
			},
			nodeLabels: map[string]string{},
			want:       true,
		},
		{
			name: "NotIn: key present with excluded value matches",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "zone", "operator": "NotIn", "values": []interface{}{"a"}},
				}},
			},
			nodeLabels: map[string]string{"zone": "b"},
			want:       true,
		},
		{
			name: "NotIn: key present with excluded value list member does not match",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "zone", "operator": "NotIn", "values": []interface{}{"a"}},
				}},
			},
			nodeLabels: map[string]string{"zone": "a"},
			want:       false,
		},
		{
			name: "Exists: key present matches",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "gpu", "operator": "Exists"},
				}},
			},
			nodeLabels: map[string]string{"gpu": ""},
			want:       true,
		},
		{
			name: "DoesNotExist: key absent matches",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "gpu", "operator": "DoesNotExist"},
				}},
			},
			nodeLabels: map[string]string{},
			want:       true,
		},
		{
			name: "Gt: value greater matches",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "cores", "operator": "Gt", "values": []interface{}{"4"}},
				}},
			},
			nodeLabels: map[string]string{"cores": "8"},
			want:       true,
		},
		{
			name: "Gt: value not greater does not match",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "cores", "operator": "Gt", "values": []interface{}{"4"}},
				}},
			},
			nodeLabels: map[string]string{"cores": "2"},
			want:       false,
		},
		{
			name: "Lt: value less matches",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					map[string]interface{}{"key": "cores", "operator": "Lt", "values": []interface{}{"4"}},
				}},
			},
			nodeLabels: map[string]string{"cores": "2"},
			want:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeSelectorTermsMatch(tt.terms, tt.nodeLabels, tt.nodeFields)
			if got != tt.want {
				t.Errorf("nodeSelectorTermsMatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPodHardConstraintRequirements(t *testing.T) {
	req := func(key, operator string, values ...string) map[string]interface{} {
		m := map[string]interface{}{"key": key, "operator": operator}
		if values != nil {
			vs := make([]interface{}, len(values))
			for i, v := range values {
				vs[i] = v
			}
			m["values"] = vs
		}
		return m
	}

	tests := []struct {
		name         string
		nodeSelector map[string]interface{}
		terms        []interface{}
		want         []interface{}
	}{
		{
			name: "nodeSelector only becomes In requirements, sorted by key",
			nodeSelector: map[string]interface{}{
				"zone": "eu-west-1a",
				"arch": "amd64",
			},
			want: []interface{}{
				req("arch", "In", "amd64"),
				req("zone", "In", "eu-west-1a"),
			},
		},
		{
			name: "single term's matchExpressions are flattened in",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{
					req("workload.example.com/tier", "In", "stateful"),
				}},
			},
			want: []interface{}{
				req("workload.example.com/tier", "In", "stateful"),
			},
		},
		{
			name: "multiple OR'd terms are skipped entirely",
			terms: []interface{}{
				map[string]interface{}{"matchExpressions": []interface{}{req("zone", "In", "a")}},
				map[string]interface{}{"matchExpressions": []interface{}{req("zone", "In", "b")}},
			},
			want: nil,
		},
		{
			name: "matchFields are always excluded",
			terms: []interface{}{
				map[string]interface{}{"matchFields": []interface{}{req("metadata.name", "In", "node-1")}},
			},
			want: nil,
		},
		{
			name:         "no constraints at all",
			nodeSelector: nil,
			terms:        nil,
			want:         nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := podHardConstraintRequirements(tt.nodeSelector, tt.terms)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("podHardConstraintRequirements() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNodeCloudProvider(t *testing.T) {
	labels := func(keys ...string) map[string]interface{} {
		m := map[string]interface{}{}
		for _, k := range keys {
			m[k] = ""
		}
		return m
	}

	tests := []struct {
		name       string
		providerID string
		labels     map[string]interface{}
		want       string
	}{
		{
			name:       "providerID scheme wins over everything else",
			providerID: "aws:///us-east-1a/i-0123456789abcdef0",
			labels:     labels("kubernetes.azure.com/cluster"),
			want:       "AWS",
		},
		{
			name:       "azure's empty-authority form still parses",
			providerID: "azure:///subscriptions/abc/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/1",
			want:       "Azure",
		},
		{
			name:       "gce is reported under the name users know it by",
			providerID: "gce://my-project/europe-west1-b/instance-1",
			want:       "GCP",
		},
		{
			name:       "an unrecognized scheme is still reported verbatim",
			providerID: "someprovider://opaque-id",
			want:       "someprovider",
		},
		{
			name:       "a providerID that isn't a URL at all falls through to the labels",
			providerID: "bare-metal-box-17",
			labels:     labels("eks.amazonaws.com/nodegroup"),
			want:       "AWS (from labels)",
		},
		{
			name:   "labels alone are reported as the weaker evidence they are",
			labels: labels("cloud.google.com/gke-nodepool"),
			want:   "GCP (from labels)",
		},
		{
			name:   "topology and instance-type labels are provider-agnostic -- not evidence",
			labels: labels("topology.kubernetes.io/zone", "node.kubernetes.io/instance-type"),
			want:   "",
		},
		{
			name: "no providerID and no labels -- no guess",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeCloudProvider(tt.providerID, tt.labels)
			if got != tt.want {
				t.Errorf("nodeCloudProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNodeCloudProviderIsDeterministicAcrossNamespaces guards the ordering of
// cloudProviderLabelPrefixes: a node can legitimately carry two providers' label namespaces (an
// EKS node managed by Karpenter carries both eks.amazonaws.com/ and karpenter.k8s.aws/, and a
// migrated cluster can keep a departed provider's labels around), and map iteration order would
// make which one gets reported differ between runs.
func TestNodeCloudProviderIsDeterministicAcrossNamespaces(t *testing.T) {
	multi := map[string]interface{}{
		"kubernetes.azure.com/cluster":  "",
		"eks.amazonaws.com/nodegroup":   "",
		"cloud.google.com/gke-nodepool": "",
	}
	first := nodeCloudProvider("", multi)
	for i := 0; i < 50; i++ {
		if got := nodeCloudProvider("", multi); got != first {
			t.Fatalf("nodeCloudProvider() = %q on run %d, want a stable %q", got, i, first)
		}
	}
}
