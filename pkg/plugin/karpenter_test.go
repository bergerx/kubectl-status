package plugin

import (
	"reflect"
	"testing"
)

func TestNodePoolCanPossiblySatisfy(t *testing.T) {
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
		name                 string
		nodePoolRequirements []interface{}
		podRequirement       map[string]interface{}
		want                 bool
	}{
		{
			name:                 "no requirement on key at all -- cannot provision",
			nodePoolRequirements: []interface{}{req("arch", "In", "amd64")},
			podRequirement:       req("zone", "In", "eu-west-1a"),
			want:                 false,
		},
		{
			name:                 "In/In overlapping value sets -- compatible",
			nodePoolRequirements: []interface{}{req("zone", "In", "eu-west-1a", "eu-west-1b")},
			podRequirement:       req("zone", "In", "eu-west-1a"),
			want:                 true,
		},
		{
			name:                 "In/In disjoint value sets -- disqualifies",
			nodePoolRequirements: []interface{}{req("zone", "In", "eu-west-1b", "eu-west-1c")},
			podRequirement:       req("zone", "In", "eu-west-1a"),
			want:                 false,
		},
		{
			name:                 "pod In excluded entirely by pool NotIn -- disqualifies",
			nodePoolRequirements: []interface{}{req("zone", "NotIn", "eu-west-1a")},
			podRequirement:       req("zone", "In", "eu-west-1a"),
			want:                 false,
		},
		{
			name:                 "pod In only partially excluded by pool NotIn -- compatible",
			nodePoolRequirements: []interface{}{req("zone", "NotIn", "eu-west-1a")},
			podRequirement:       req("zone", "In", "eu-west-1a", "eu-west-1b"),
			want:                 true,
		},
		{
			name:                 "pod NotIn vs pool In, pool has an allowed value pod doesn't exclude -- compatible",
			nodePoolRequirements: []interface{}{req("zone", "In", "eu-west-1a", "eu-west-1b")},
			podRequirement:       req("zone", "NotIn", "eu-west-1a"),
			want:                 true,
		},
		{
			name:                 "pod NotIn vs pool In, pool's only allowed value is excluded -- disqualifies",
			nodePoolRequirements: []interface{}{req("zone", "In", "eu-west-1a")},
			podRequirement:       req("zone", "NotIn", "eu-west-1a"),
			want:                 false,
		},
		{
			name:                 "NotIn/NotIn -- always compatible",
			nodePoolRequirements: []interface{}{req("zone", "NotIn", "eu-west-1a")},
			podRequirement:       req("zone", "NotIn", "eu-west-1b"),
			want:                 true,
		},
		{
			name:                 "pool Exists -- degrades to compatible",
			nodePoolRequirements: []interface{}{req("gpu", "Exists")},
			podRequirement:       req("gpu", "In", "true"),
			want:                 true,
		},
		{
			name:                 "pod Exists -- degrades to compatible",
			nodePoolRequirements: []interface{}{req("gpu", "In", "true")},
			podRequirement:       req("gpu", "Exists"),
			want:                 true,
		},
		{
			name:                 "Gt/Lt combinations -- degrade to compatible",
			nodePoolRequirements: []interface{}{req("cores", "Gt", "4")},
			podRequirement:       req("cores", "Lt", "16"),
			want:                 true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodePoolCanPossiblySatisfy(tt.nodePoolRequirements, tt.podRequirement)
			if got != tt.want {
				t.Errorf("nodePoolCanPossiblySatisfy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKarpenterUnsatisfiableKeys(t *testing.T) {
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
	pool := func(name string, requirements ...interface{}) interface{} {
		return map[string]interface{}{"name": name, "requirements": requirements}
	}

	tests := []struct {
		name            string
		podRequirements []interface{}
		nodePools       []interface{}
		want            []string
	}{
		{
			name:            "custom label no NodePool declares -- unsatisfiable",
			podRequirements: []interface{}{req("workload.example.com/tier", "In", "stateful")},
			nodePools: []interface{}{
				pool("default", req("node.kubernetes.io/instance-type", "Exists")),
			},
			want: []string{"workload.example.com/tier"},
		},
		{
			name:            "at least one NodePool declares the key compatibly -- silent",
			podRequirements: []interface{}{req("topology.kubernetes.io/zone", "In", "eu-west-1a")},
			nodePools: []interface{}{
				pool("a", req("topology.kubernetes.io/zone", "In", "eu-west-1b")),
				pool("b", req("topology.kubernetes.io/zone", "In", "eu-west-1a")),
			},
			want: nil,
		},
		{
			name:            "every NodePool has an incompatible zone -- unsatisfiable",
			podRequirements: []interface{}{req("topology.kubernetes.io/zone", "In", "eu-west-1a")},
			nodePools: []interface{}{
				pool("a", req("topology.kubernetes.io/zone", "In", "eu-west-1b")),
				pool("b", req("topology.kubernetes.io/zone", "In", "eu-west-1c")),
			},
			want: []string{"topology.kubernetes.io/zone"},
		},
		{
			name:            "no NodePools at all -- unsatisfiable for every key",
			podRequirements: []interface{}{req("zone", "In", "a"), req("arch", "In", "amd64")},
			nodePools:       nil,
			want:            []string{"arch", "zone"},
		},
		{
			name:            "no pod requirements -- nothing to report",
			podRequirements: nil,
			nodePools:       []interface{}{pool("a")},
			want:            nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := karpenterUnsatisfiableKeys(tt.podRequirements, tt.nodePools)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("karpenterUnsatisfiableKeys() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestKarpenterDisqualifyingKey(t *testing.T) {
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
		name                 string
		nodePoolRequirements []interface{}
		podRequirements      []interface{}
		want                 string
	}{
		{
			name:                 "pool satisfies every pod requirement -- empty",
			nodePoolRequirements: []interface{}{req("zone", "In", "a"), req("arch", "In", "amd64")},
			podRequirements:      []interface{}{req("zone", "In", "a"), req("arch", "In", "amd64")},
			want:                 "",
		},
		{
			name:                 "pool fails the second requirement",
			nodePoolRequirements: []interface{}{req("zone", "In", "a")},
			podRequirements:      []interface{}{req("zone", "In", "a"), req("arch", "In", "amd64")},
			want:                 "arch",
		},
		{
			name:                 "pool has no requirements at all",
			nodePoolRequirements: nil,
			podRequirements:      []interface{}{req("zone", "In", "a")},
			want:                 "zone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := karpenterDisqualifyingKey(tt.nodePoolRequirements, tt.podRequirements)
			if got != tt.want {
				t.Errorf("karpenterDisqualifyingKey() = %q, want %q", got, tt.want)
			}
		})
	}
}
