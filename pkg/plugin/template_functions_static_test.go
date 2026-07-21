package plugin

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	emptyMap     = map[string]interface{}{}
	searchForMap = map[string]interface{}{
		"searchKey1": "searchVal1",
		"searchKey2": "searchVal2",
	}
	nonMatchingValueMap = map[string]interface{}{
		"searchKey": "searchValDoesntMatch",
	}
	nonMatchingKeyMap = map[string]interface{}{
		"searchKeyDoesntMatch": "searchVal",
	}
	matchingSuperSetMap1 = map[string]interface{}{
		"searchKey1": "searchVal1",
		"searchKey2": "searchVal2",
		"otherKey1":  "doestMatter1",
	}
	matchingSuperSetMap2 = map[string]interface{}{
		"searchKey1": "searchVal1",
		"searchKey2": "searchVal2",
		"otherKey2":  "doestMatter2",
	}
	nestedSearchForMap = map[string]interface{}{
		"outerKey.innerKey.searchKey1": "searchVal1",
		"outerKey.innerKey.searchKey2": "searchVal2",
	}
	matchingNestedMap = map[string]interface{}{
		"outerKey": map[string]interface{}{
			"innerKey": matchingSuperSetMap1,
			"otherKey": "doesntMatter",
		},
	}
	nonMatchingMiddleKeyNestedMap = map[string]interface{}{
		"outerKey": matchingSuperSetMap1,
	}
)

func TestGetMatchingItemInMapList(t *testing.T) {
	type args struct {
		searchFor map[string]interface{}
		mapList   []interface{}
	}
	tests := []struct {
		name     string
		args     args
		wantItem map[string]interface{}
	}{
		{
			name: "one-to-one maps",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{searchForMap},
			},
			wantItem: searchForMap,
		}, {
			name: "key exists but value doesn't match",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{nonMatchingValueMap},
			},
			wantItem: nil,
		}, {
			name: "search key doesnt exist in mapList",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{nonMatchingKeyMap},
			},
			wantItem: nil,
		}, {
			name: "empty mapList",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{emptyMap},
			},
			wantItem: nil,
		}, {
			name: "empty searchFor",
			args: args{
				searchFor: emptyMap,
				mapList:   []interface{}{searchForMap},
			},
			wantItem: nil,
		}, {
			name: "searchFor is subset",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{nonMatchingKeyMap, nonMatchingValueMap, matchingSuperSetMap1},
			},
			wantItem: matchingSuperSetMap1,
		}, {
			name: "multiple matches should return first match",
			args: args{
				searchFor: searchForMap,
				mapList:   []interface{}{nonMatchingKeyMap, nonMatchingValueMap, matchingSuperSetMap2, matchingSuperSetMap1},
			},
			wantItem: matchingSuperSetMap2,
		}, {
			name: "nested map is subset",
			args: args{
				searchFor: nestedSearchForMap,
				mapList:   []interface{}{nonMatchingKeyMap, nonMatchingValueMap, matchingSuperSetMap1, matchingNestedMap},
			},
			wantItem: matchingNestedMap,
		}, {
			name: "nested map missing key",
			args: args{
				searchFor: nestedSearchForMap,
				mapList:   []interface{}{nonMatchingKeyMap},
			},
			wantItem: nil,
		}, {
			name: "nested map missing middle key",
			args: args{
				searchFor: nestedSearchForMap,
				mapList:   []interface{}{nonMatchingMiddleKeyNestedMap},
			},
			wantItem: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotItem := getMatchingItemInMapList(tt.args.searchFor, tt.args.mapList); !reflect.DeepEqual(gotItem, tt.wantItem) {
				t.Errorf("getMatchingItemInMapList() = %v, want %v", gotItem, tt.wantItem)
			}
		})
	}
}

func TestSortMapListByKeysValueIsStableOnTies(t *testing.T) {
	// When multiple items share the same key value, the sort must preserve
	// their original relative order (as returned by the k8s API) instead of
	// reordering them arbitrarily, otherwise output like "Known/recorded
	// manage events" becomes flaky between otherwise identical runs.
	mapList := []interface{}{
		map[string]interface{}{"manager": "kubectl-client-side-apply", "time": "2024-01-01T00:00:00Z"},
		map[string]interface{}{"manager": "kube-controller-manager", "time": "2024-01-01T00:00:00Z"},
		map[string]interface{}{"manager": "another-manager", "time": "2023-12-31T00:00:00Z"},
	}
	for i := 0; i < 10; i++ {
		got := sortMapListByKeysValue("time", mapList)
		want := []interface{}{mapList[2], mapList[0], mapList[1]}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sortMapListByKeysValue() = %v, want %v", got, want)
		}
	}
}

func TestSortMapListByFloatKeysValueDescIsStableOnTies(t *testing.T) {
	// Mirrors TestSortMapListByKeysValueIsStableOnTies: ties (e.g. two pods reporting the same
	// usage) must preserve original relative order rather than reordering arbitrarily, otherwise
	// a Node's "pods by usage" ranking becomes flaky between otherwise identical runs.
	mapList := []interface{}{
		map[string]interface{}{"ref": "ns/a", "memUsage": 5.0},
		map[string]interface{}{"ref": "ns/b", "memUsage": 10.0},
		map[string]interface{}{"ref": "ns/c", "memUsage": 10.0},
	}
	for i := 0; i < 10; i++ {
		got := sortMapListByFloatKeysValueDesc("memUsage", mapList)
		want := []interface{}{mapList[1], mapList[2], mapList[0]}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sortMapListByFloatKeysValueDesc() = %v, want %v", got, want)
		}
	}
}

func TestFieldsV1Paths(t *testing.T) {
	tests := []struct {
		name     string
		fieldsV1 map[string]interface{}
		want     []string
	}{
		{
			name:     "single nested field under spec",
			fieldsV1: map[string]interface{}{"f:spec": map[string]interface{}{"f:template": map[string]interface{}{".": struct{}{}}}},
			want:     []string{"spec.template"},
		},
		{
			name:     "leaf field under metadata",
			fieldsV1: map[string]interface{}{"f:metadata": map[string]interface{}{"f:annotations": map[string]interface{}{".": struct{}{}}}},
			want:     []string{"metadata.annotations"},
		},
		{
			name: "multiple siblings under status stop at status",
			fieldsV1: map[string]interface{}{"f:status": map[string]interface{}{
				"f:conditions": map[string]interface{}{},
				"f:phase":      map[string]interface{}{},
			}},
			want: []string{"status"},
		},
		{
			name: "mix of labels, template and conditions",
			fieldsV1: map[string]interface{}{
				"f:metadata": map[string]interface{}{"f:labels": map[string]interface{}{".": struct{}{}}},
				"f:spec":     map[string]interface{}{"f:template": map[string]interface{}{".": struct{}{}}},
				"f:status":   map[string]interface{}{"f:conditions": map[string]interface{}{}},
			},
			want: []string{"metadata.labels", "spec.template", "status.conditions"},
		},
		{
			name:     "single owned label descends into the label key",
			fieldsV1: map[string]interface{}{"f:metadata": map[string]interface{}{"f:labels": map[string]interface{}{"f:app": map[string]interface{}{}}}},
			want:     []string{"metadata.labels.app"},
		},
		{
			name: "single owned annotation descends into the annotation key, quoted since it contains dots",
			fieldsV1: map[string]interface{}{"f:metadata": map[string]interface{}{"f:annotations": map[string]interface{}{
				"f:deployment.kubernetes.io/revision": map[string]interface{}{},
			}}},
			want: []string{`metadata.annotations."deployment.kubernetes.io/revision"`},
		},
		{
			name: "multiple owned labels stop at metadata.labels",
			fieldsV1: map[string]interface{}{"f:metadata": map[string]interface{}{"f:labels": map[string]interface{}{
				"f:app":  map[string]interface{}{},
				"f:tier": map[string]interface{}{},
			}}},
			want: []string{"metadata.labels"},
		},
		{
			name:     "empty fieldsV1",
			fieldsV1: map[string]interface{}{},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fieldsV1Paths(tt.fieldsV1); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fieldsV1Paths() = %v, want %v", got, tt.want)
			}
		})
	}
}

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

func TestAgoSuffix(t *testing.T) {
	v := viper.New()
	cfg := NewRenderConfig(v)
	v.Set("absolute-time", false)
	if got := cfg.agoSuffix(); got != " ago" {
		t.Errorf("agoSuffix() = %q, want %q", got, " ago")
	}
	v.Set("absolute-time", true)
	if got := cfg.agoSuffix(); got != "" {
		t.Errorf("agoSuffix() = %q, want empty string", got)
	}
}

func TestForOrSince(t *testing.T) {
	v := viper.New()
	cfg := NewRenderConfig(v)
	v.Set("absolute-time", false)
	if got := cfg.forOrSince(); got != "for" {
		t.Errorf("forOrSince() = %q, want %q", got, "for")
	}
	v.Set("absolute-time", true)
	if got := cfg.forOrSince(); got != "since" {
		t.Errorf("forOrSince() = %q, want %q", got, "since")
	}
}

func TestColorAgoAbsolute(t *testing.T) {
	v := viper.New()
	cfg := NewRenderConfig(v)
	v.Set("absolute-time", true)
	input := "2006-01-02T15:04:05Z"
	if got := cfg.colorAgo(input); got != input {
		t.Errorf("colorAgo(%q) = %q, want %q", input, got, input)
	}
}

// genCertOptions configures generateTestCert.
type genCertOptions struct {
	subjectCN  string
	dnsNames   []string
	isCA       bool
	selfSigned bool
	// parent/parentKey are used when selfSigned is false, to sign this cert with another.
	parent    *x509.Certificate
	parentKey *rsa.PrivateKey
}

// generateTestCert generates an in-memory RSA key + x509 certificate PEM block, either
// self-signed or signed by the given parent, avoiding any static/expiring PEM fixtures.
func generateTestCert(t *testing.T, opts genCertOptions) (certPEM []byte, cert *x509.Certificate, key *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate rsa key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: opts.subjectCN},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour * 365),
		DNSNames:     opts.dnsNames,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         opts.isCA,
	}
	if opts.isCA {
		template.BasicConstraintsValid = true
	}

	parent := template
	signerKey := key
	if !opts.selfSigned {
		parent = opts.parent
		signerKey = opts.parentKey
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	cert, err = x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("failed to parse generated certificate: %v", err)
	}
	return certPEM, cert, key
}

// generateTestCSR generates an in-memory PKCS#10 certificate signing request PEM block.
func generateTestCSR(t *testing.T, subjectCN string, dnsNames []string, ipAddresses []net.IP) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate rsa key: %v", err)
	}
	template := &x509.CertificateRequest{
		Subject:     pkix.Name{CommonName: subjectCN},
		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
	}
	derBytes, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("failed to create certificate request: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: derBytes})
}

// keyPEM PEM-encodes an RSA private key in PKCS1 form (content is irrelevant to
// parseTLSSecretCertificate, which never inspects tls.key, only checks for its presence).
func keyPEMBytes(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// dataSecret builds a RenderableObject Secret of the given type with the given data map,
// base64-encoding each raw value the way `data:` is represented on the wire.
func dataSecret(secretType string, data map[string]string) RenderableObject {
	encoded := map[string]interface{}{}
	for k, v := range data {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	obj := map[string]interface{}{
		"type": secretType,
		"data": encoded,
	}
	return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
}

// tlsSecret builds a RenderableObject wrapping a kubernetes.io/tls Secret with the given
// (already base64-encoded-ready, i.e. raw) tls.crt/tls.key byte contents. Passing nil for
// either omits that key from data entirely (simulating a missing key).
func tlsSecret(secretType string, crt, key []byte) RenderableObject {
	data := map[string]interface{}{}
	if crt != nil {
		data["tls.crt"] = base64.StdEncoding.EncodeToString(crt)
	}
	if key != nil {
		data["tls.key"] = base64.StdEncoding.EncodeToString(key)
	}
	obj := map[string]interface{}{
		"type": secretType,
		"data": data,
	}
	return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
}

func TestParseTLSSecretCertificate(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	selfSignedPEM, selfSignedCert, selfSignedKey := generateTestCert(t, genCertOptions{
		subjectCN:  "self-signed.example.com",
		dnsNames:   []string{"self-signed.example.com"},
		selfSigned: true,
	})
	_ = selfSignedCert

	caPEM, caCert, caKey := generateTestCert(t, genCertOptions{
		subjectCN:  "Test CA",
		isCA:       true,
		selfSigned: true,
	})

	leafPEM, _, leafKey := generateTestCert(t, genCertOptions{
		subjectCN: "leaf.example.com",
		dnsNames:  []string{"leaf.example.com", "*.wild.example.com"},
		parent:    caCert,
		parentKey: caKey,
	})
	_ = leafKey

	chainPEM := append(append([]byte{}, leafPEM...), caPEM...)

	// CommonName deliberately does not come first in DNSNames, to catch any assumption that
	// slicing off DNSNames[0] is equivalent to filtering out the CommonName.
	cnNotFirstPEM, _, cnNotFirstKey := generateTestCert(t, genCertOptions{
		subjectCN:  "cn.example.com",
		dnsNames:   []string{"extra.example.com", "cn.example.com"},
		selfSigned: true,
	})

	tests := []struct {
		name          string
		secret        RenderableObject
		hostname      string
		want          map[string]interface{}
		checkKeysOnly []string // if set, only assert these keys (for concise cert-content checks)
	}{
		{
			name:     "secret not found",
			secret:   RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}},
			hostname: "",
			want: map[string]interface{}{
				"Exists":          false,
				"WrongType":       false,
				"ActualType":      "",
				"MissingKeys":     []string{},
				"ParseError":      "",
				"SelfSigned":      false,
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"Exists", "WrongType", "ActualType", "MissingKeys", "ParseError", "SelfSigned", "MatchesHostname"},
		},
		{
			name:     "wrong type",
			secret:   tlsSecret("Opaque", selfSignedPEM, keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":     true,
				"WrongType":  true,
				"ActualType": "Opaque",
			},
			checkKeysOnly: []string{"Exists", "WrongType", "ActualType"},
		},
		{
			name:     "missing tls.crt",
			secret:   tlsSecret("kubernetes.io/tls", nil, keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":      true,
				"WrongType":   false,
				"MissingKeys": []string{"tls.crt"},
			},
			checkKeysOnly: []string{"Exists", "WrongType", "MissingKeys"},
		},
		{
			name:     "missing tls.key",
			secret:   tlsSecret("kubernetes.io/tls", selfSignedPEM, nil),
			hostname: "",
			want: map[string]interface{}{
				"Exists":      true,
				"MissingKeys": []string{"tls.key"},
			},
			checkKeysOnly: []string{"Exists", "MissingKeys"},
		},
		{
			name:     "missing both keys",
			secret:   tlsSecret("kubernetes.io/tls", nil, nil),
			hostname: "",
			want: map[string]interface{}{
				"Exists":      true,
				"MissingKeys": []string{"tls.crt", "tls.key"},
			},
			checkKeysOnly: []string{"Exists", "MissingKeys"},
		},
		{
			name:     "malformed base64",
			secret:   tlsSecret("kubernetes.io/tls", []byte("not-valid-base64!!!"), keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists": true,
			},
			checkKeysOnly: []string{"Exists"},
		},
		{
			name:     "malformed pem",
			secret:   tlsSecret("kubernetes.io/tls", []byte("aGVsbG8gd29ybGQ="), keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists": true,
			},
			checkKeysOnly: []string{"Exists"},
		},
		{
			name:     "self-signed cert",
			secret:   tlsSecret("kubernetes.io/tls", selfSignedPEM, keyPEMBytes(selfSignedKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":          true,
				"WrongType":       false,
				"ParseError":      "",
				"SelfSigned":      true,
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"Exists", "WrongType", "ParseError", "SelfSigned", "MatchesHostname"},
		},
		{
			name:     "CA-signed leaf is not self-signed",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":     true,
				"ParseError": "",
				"SelfSigned": false,
			},
			checkKeysOnly: []string{"Exists", "ParseError", "SelfSigned"},
		},
		{
			name:     "chain: leaf+root concatenated reports leaf's self-signed status, not root's",
			secret:   tlsSecret("kubernetes.io/tls", chainPEM, keyPEMBytes(leafKey)),
			hostname: "",
			want: map[string]interface{}{
				"Exists":     true,
				"ParseError": "",
				"SelfSigned": false,
			},
			checkKeysOnly: []string{"Exists", "ParseError", "SelfSigned"},
		},
		{
			name:     "hostname match exact",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "leaf.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "hostname mismatch",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "other.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard SAN match",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "foo.wild.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "AltDNSNames excludes the CommonName regardless of its position in DNSNames",
			secret:   tlsSecret("kubernetes.io/tls", cnNotFirstPEM, keyPEMBytes(cnNotFirstKey)),
			hostname: "",
			want: map[string]interface{}{
				"DNSNames":    []string{"extra.example.com", "cn.example.com"},
				"AltDNSNames": []string{"extra.example.com"},
			},
			checkKeysOnly: []string{"DNSNames", "AltDNSNames"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.parseTLSSecretCertificate(tt.secret, tt.hostname)
			for _, key := range tt.checkKeysOnly {
				wantVal, ok := tt.want[key]
				if !ok {
					t.Fatalf("test case %q: missing expected value for key %q", tt.name, key)
				}
				gotVal := got[key]
				if !reflect.DeepEqual(gotVal, wantVal) {
					t.Errorf("parseTLSSecretCertificate()[%q] = %#v, want %#v", key, gotVal, wantVal)
				}
			}
			if tt.name == "malformed base64" || tt.name == "malformed pem" {
				if got["ParseError"] == "" {
					t.Errorf("expected non-empty ParseError for %q", tt.name)
				}
			}
		})
	}

	// Sanity: every key in the result map must always be present (never <no value> in templates).
	all := cfg.parseTLSSecretCertificate(RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}}, "")
	expectedKeys := []string{
		"Exists", "WrongType", "ActualType", "MissingKeys", "ParseError",
		"Subject", "Issuer", "SerialNumber", "NotBefore", "NotAfter",
		"DNSNames", "AltDNSNames", "IPAddresses", "KeyAlgorithm", "SelfSigned", "Expired", "MatchesHostname",
	}
	for _, key := range expectedKeys {
		if _, ok := all[key]; !ok {
			t.Errorf("result map missing key %q", key)
		}
	}
}

func TestCertificatesInSecret(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	caPEM, caCert, caKey := generateTestCert(t, genCertOptions{
		subjectCN:  "Test CA",
		isCA:       true,
		selfSigned: true,
	})
	leafPEM, _, _ := generateTestCert(t, genCertOptions{
		subjectCN: "leaf.example.com",
		dnsNames:  []string{"leaf.example.com"},
		parent:    caCert,
		parentKey: caKey,
	})

	t.Run("opaque secret with ca.crt and tls.crt/tls.key", func(t *testing.T) {
		obj := map[string]interface{}{
			"type": "Opaque",
			"data": map[string]interface{}{
				"ca.crt":  base64.StdEncoding.EncodeToString(caPEM),
				"tls.crt": base64.StdEncoding.EncodeToString(leafPEM),
				"tls.key": base64.StdEncoding.EncodeToString([]byte("irrelevant")),
			},
		}
		got := cfg.certificatesInSecret(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 2 {
			t.Fatalf("expected 2 certificates, got %d: %#v", len(got), got)
		}
		// sorted alphabetically: ca.crt before tls.crt
		if got[0]["Name"] != "ca.crt" || got[0]["SelfSigned"] != true {
			t.Errorf("got[0] = %#v, want ca.crt/self-signed", got[0])
		}
		if got[1]["Name"] != "tls.crt" || got[1]["SelfSigned"] != false {
			t.Errorf("got[1] = %#v, want tls.crt/not self-signed", got[1])
		}
		if got[1]["Issuer"] != caCert.Subject.String() {
			t.Errorf("got[1][Issuer] = %v, want %v", got[1]["Issuer"], caCert.Subject.String())
		}
	})

	t.Run("secret with no .crt fields", func(t *testing.T) {
		obj := map[string]interface{}{
			"type": "Opaque",
			"data": map[string]interface{}{
				"username": base64.StdEncoding.EncodeToString([]byte("admin")),
			},
		}
		got := cfg.certificatesInSecret(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 0 {
			t.Errorf("expected no certificates, got %#v", got)
		}
	})

	t.Run("malformed cert data reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"type": "Opaque",
			"data": map[string]interface{}{
				"ca.crt": "not-valid-base64!!!",
			},
		}
		got := cfg.certificatesInSecret(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 || got[0]["ParseError"] == "" {
			t.Errorf("expected 1 entry with non-empty ParseError, got %#v", got)
		}
	})

	if got := cfg.certificatesInSecret(RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}}); len(got) != 0 {
		t.Errorf("expected nil Object to yield no certificates, got %#v", got)
	}
}

func TestCertificatesInConfigMap(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	caPEM, caCert, caKey := generateTestCert(t, genCertOptions{
		subjectCN:  "Test CA",
		isCA:       true,
		selfSigned: true,
	})
	leafPEM, _, _ := generateTestCert(t, genCertOptions{
		subjectCN: "leaf.example.com",
		dnsNames:  []string{"leaf.example.com"},
		parent:    caCert,
		parentKey: caKey,
	})

	t.Run("data holds plain-text PEM, not base64", func(t *testing.T) {
		obj := map[string]interface{}{
			"data": map[string]interface{}{
				"ca.crt": string(caPEM),
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 {
			t.Fatalf("expected 1 certificate, got %d: %#v", len(got), got)
		}
		if got[0]["Name"] != "ca.crt" || got[0]["SelfSigned"] != true || got[0]["ParseError"] != "" {
			t.Errorf("got[0] = %#v, want ca.crt/self-signed with no error", got[0])
		}
	})

	t.Run("binaryData holds base64-encoded PEM", func(t *testing.T) {
		obj := map[string]interface{}{
			"binaryData": map[string]interface{}{
				"tls.crt": base64.StdEncoding.EncodeToString(leafPEM),
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 {
			t.Fatalf("expected 1 certificate, got %d: %#v", len(got), got)
		}
		if got[0]["Name"] != "tls.crt" || got[0]["Issuer"] != caCert.Subject.String() {
			t.Errorf("got[0] = %#v, want tls.crt issued by %v", got[0], caCert.Subject.String())
		}
	})

	t.Run("combines data and binaryData, sorted by key", func(t *testing.T) {
		obj := map[string]interface{}{
			"data": map[string]interface{}{
				"ca.crt": string(caPEM),
			},
			"binaryData": map[string]interface{}{
				"tls.crt": base64.StdEncoding.EncodeToString(leafPEM),
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 2 {
			t.Fatalf("expected 2 certificates, got %d: %#v", len(got), got)
		}
		if got[0]["Name"] != "ca.crt" || got[1]["Name"] != "tls.crt" {
			t.Errorf("expected ca.crt before tls.crt, got %#v", got)
		}
	})

	t.Run("configmap with no .crt fields", func(t *testing.T) {
		obj := map[string]interface{}{
			"data": map[string]interface{}{
				"application.properties": "foo=bar",
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 0 {
			t.Errorf("expected no certificates, got %#v", got)
		}
	})

	t.Run("malformed PEM in data reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"data": map[string]interface{}{
				"ca.crt": "not a certificate",
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 || got[0]["ParseError"] == "" {
			t.Errorf("expected 1 entry with non-empty ParseError, got %#v", got)
		}
	})

	t.Run("malformed base64 in binaryData reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"binaryData": map[string]interface{}{
				"ca.crt": "not-valid-base64!!!",
			},
		}
		got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if len(got) != 1 || got[0]["ParseError"] == "" {
			t.Errorf("expected 1 entry with non-empty ParseError, got %#v", got)
		}
	})

	if got := cfg.certificatesInConfigMap(RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}}); len(got) != 0 {
		t.Errorf("expected nil Object to yield no certificates, got %#v", got)
	}
}

func TestCertificateInCSR(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	_, caCert, caKey := generateTestCert(t, genCertOptions{
		subjectCN:  "Test CA",
		isCA:       true,
		selfSigned: true,
	})
	leafPEM, _, _ := generateTestCert(t, genCertOptions{
		subjectCN: "leaf.example.com",
		dnsNames:  []string{"leaf.example.com"},
		parent:    caCert,
		parentKey: caKey,
	})

	t.Run("not yet issued", func(t *testing.T) {
		obj := map[string]interface{}{"status": map[string]interface{}{}}
		got := cfg.certificateInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got != nil {
			t.Errorf("expected nil for unissued CSR, got %#v", got)
		}
	})

	t.Run("issued certificate parses", func(t *testing.T) {
		obj := map[string]interface{}{
			"status": map[string]interface{}{
				"certificate": base64.StdEncoding.EncodeToString(leafPEM),
			},
		}
		got := cfg.certificateInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got == nil {
			t.Fatal("expected non-nil result for issued CSR")
		}
		if got["ParseError"] != "" {
			t.Errorf("unexpected ParseError: %v", got["ParseError"])
		}
		if got["Issuer"] != caCert.Subject.String() {
			t.Errorf("Issuer = %v, want %v", got["Issuer"], caCert.Subject.String())
		}
	})

	t.Run("malformed base64 reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"status": map[string]interface{}{
				"certificate": "not-valid-base64!!!",
			},
		}
		got := cfg.certificateInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got == nil || got["ParseError"] == "" {
			t.Errorf("expected non-nil result with ParseError, got %#v", got)
		}
	})
}

func TestCertificateRequestInCSR(t *testing.T) {
	t.Run("parses subject, SANs and key algorithm", func(t *testing.T) {
		csrPEM := generateTestCSR(t, "my-pod.default.pod.cluster.local",
			[]string{"my-pod.default.pod.cluster.local", "my-svc.default.svc.cluster.local"},
			[]net.IP{net.ParseIP("10.0.0.1")})
		obj := map[string]interface{}{
			"spec": map[string]interface{}{
				"request": base64.StdEncoding.EncodeToString(csrPEM),
			},
		}
		got := certificateRequestInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got["ParseError"] != "" {
			t.Fatalf("unexpected ParseError: %v", got["ParseError"])
		}
		if got["Subject"] != "CN=my-pod.default.pod.cluster.local" {
			t.Errorf("Subject = %v", got["Subject"])
		}
		altDNSNames, _ := got["AltDNSNames"].([]string)
		if len(altDNSNames) != 1 || altDNSNames[0] != "my-svc.default.svc.cluster.local" {
			t.Errorf("AltDNSNames = %#v, want [my-svc.default.svc.cluster.local]", altDNSNames)
		}
		ipAddresses, _ := got["IPAddresses"].([]string)
		if len(ipAddresses) != 1 || ipAddresses[0] != "10.0.0.1" {
			t.Errorf("IPAddresses = %#v, want [10.0.0.1]", ipAddresses)
		}
	})

	t.Run("missing spec.request reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{"spec": map[string]interface{}{}}
		got := certificateRequestInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got["ParseError"] == "" {
			t.Errorf("expected ParseError for missing request, got %#v", got)
		}
	})

	t.Run("malformed base64 reports ParseError", func(t *testing.T) {
		obj := map[string]interface{}{
			"spec": map[string]interface{}{
				"request": "not-valid-base64!!!",
			},
		}
		got := certificateRequestInCSR(RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}})
		if got["ParseError"] == "" {
			t.Errorf("expected ParseError for malformed base64, got %#v", got)
		}
	})
}

func TestIsStatusConditionHealthyUserProvidedTypes(t *testing.T) {
	resetUserAbnormalTrueConditionTypes := func() {
		userAbnormalTrueConditionTypesOnce = sync.Once{}
		userAbnormalTrueConditionTypes = userAbnormalTrueConditionTypeMatcher{}
	}
	t.Cleanup(resetUserAbnormalTrueConditionTypes)

	t.Run("no user file present", func(t *testing.T) {
		resetUserAbnormalTrueConditionTypes()
		t.Setenv("HOME", t.TempDir())
		condition := map[string]interface{}{"type": "CustomAbnormalTrue", "status": "True"}
		if !isStatusConditionHealthy(condition) {
			t.Errorf("expected condition type unknown to kubectl-status to follow the default 'True is healthy' polarity")
		}
	})

	t.Run("user provided condition type overrides default polarity", func(t *testing.T) {
		resetUserAbnormalTrueConditionTypes()
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".kubectl-status"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "# comment\n\nCustomAbnormalTrue\nAnotherOne\n"
		path := filepath.Join(home, ".kubectl-status", "abnormal-true-condition-types")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)

		trueCondition := map[string]interface{}{"type": "CustomAbnormalTrue", "status": "True"}
		if isStatusConditionHealthy(trueCondition) {
			t.Errorf("expected user provided abnormal-true condition type with status True to be unhealthy")
		}
		falseCondition := map[string]interface{}{"type": "CustomAbnormalTrue", "status": "False"}
		if !isStatusConditionHealthy(falseCondition) {
			t.Errorf("expected user provided abnormal-true condition type with status False to be healthy")
		}
	})

	t.Run("user provided suffix and prefix patterns", func(t *testing.T) {
		resetUserAbnormalTrueConditionTypes()
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".kubectl-status"), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "*Problematic\nUnhealthy*\n"
		path := filepath.Join(home, ".kubectl-status", "abnormal-true-condition-types")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)

		suffixMatch := map[string]interface{}{"type": "DiskProblematic", "status": "True"}
		if isStatusConditionHealthy(suffixMatch) {
			t.Errorf("expected type matching the '*Problematic' suffix pattern with status True to be unhealthy")
		}
		prefixMatch := map[string]interface{}{"type": "UnhealthyDisk", "status": "True"}
		if isStatusConditionHealthy(prefixMatch) {
			t.Errorf("expected type matching the 'Unhealthy*' prefix pattern with status True to be unhealthy")
		}
		noMatch := map[string]interface{}{"type": "SomethingElse", "status": "True"}
		if !isStatusConditionHealthy(noMatch) {
			t.Errorf("expected type not matching any user pattern to keep the default 'True is healthy' polarity")
		}
	})
}

func TestParseBasicAuthSecret(t *testing.T) {
	tests := []struct {
		name              string
		secret            RenderableObject
		wantUsername      bool
		wantUsernameEmpty bool
		wantPassword      bool
		wantPasswordEmpty bool
	}{
		{
			name:         "secret not found",
			secret:       RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}},
			wantUsername: false,
			wantPassword: false,
		},
		{
			name:         "both present",
			secret:       dataSecret("kubernetes.io/basic-auth", map[string]string{"username": "alice", "password": "hunter2"}),
			wantUsername: true,
			wantPassword: true,
		},
		{
			name:         "username only",
			secret:       dataSecret("kubernetes.io/basic-auth", map[string]string{"username": "alice"}),
			wantUsername: true,
			wantPassword: false,
		},
		{
			name:         "password only",
			secret:       dataSecret("kubernetes.io/basic-auth", map[string]string{"password": "hunter2"}),
			wantUsername: false,
			wantPassword: true,
		},
		{
			name:         "neither present",
			secret:       dataSecret("kubernetes.io/basic-auth", map[string]string{}),
			wantUsername: false,
			wantPassword: false,
		},
		{
			name:              "password key present but empty",
			secret:            dataSecret("kubernetes.io/basic-auth", map[string]string{"username": "alice", "password": ""}),
			wantUsername:      true,
			wantPassword:      true,
			wantPasswordEmpty: true,
		},
		{
			name:              "username key present but empty",
			secret:            dataSecret("kubernetes.io/basic-auth", map[string]string{"username": "", "password": "hunter2"}),
			wantUsername:      true,
			wantUsernameEmpty: true,
			wantPassword:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBasicAuthSecret(tt.secret)
			if got["HasUsername"] != tt.wantUsername {
				t.Errorf("HasUsername = %v, want %v", got["HasUsername"], tt.wantUsername)
			}
			if got["UsernameEmpty"] != tt.wantUsernameEmpty {
				t.Errorf("UsernameEmpty = %v, want %v", got["UsernameEmpty"], tt.wantUsernameEmpty)
			}
			if got["HasPassword"] != tt.wantPassword {
				t.Errorf("HasPassword = %v, want %v", got["HasPassword"], tt.wantPassword)
			}
			if got["PasswordEmpty"] != tt.wantPasswordEmpty {
				t.Errorf("PasswordEmpty = %v, want %v", got["PasswordEmpty"], tt.wantPasswordEmpty)
			}
		})
	}
}

func TestParseSSHAuthSecret(t *testing.T) {
	_, _, rsaKey := generateTestCert(t, genCertOptions{subjectCN: "irrelevant", selfSigned: true})
	validKeyPEM := keyPEMBytes(rsaKey)

	tests := []struct {
		name           string
		secret         RenderableObject
		wantExists     bool
		wantParseError bool
		wantKeyType    string
	}{
		{
			name:       "no ssh-privatekey entry",
			secret:     dataSecret("kubernetes.io/ssh-auth", map[string]string{}),
			wantExists: false,
		},
		{
			name:           "malformed base64",
			secret:         RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{"type": "kubernetes.io/ssh-auth", "data": map[string]interface{}{"ssh-privatekey": "not-valid-base64!!!"}}}},
			wantExists:     true,
			wantParseError: true,
		},
		{
			name:           "malformed pem",
			secret:         dataSecret("kubernetes.io/ssh-auth", map[string]string{"ssh-privatekey": "not a pem key"}),
			wantExists:     true,
			wantParseError: true,
		},
		{
			name:        "valid rsa private key",
			secret:      RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{"type": "kubernetes.io/ssh-auth", "data": map[string]interface{}{"ssh-privatekey": base64.StdEncoding.EncodeToString(validKeyPEM)}}}},
			wantExists:  true,
			wantKeyType: "ssh-rsa",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSSHAuthSecret(tt.secret)
			if got["Exists"] != tt.wantExists {
				t.Errorf("Exists = %v, want %v", got["Exists"], tt.wantExists)
			}
			if tt.wantParseError && got["ParseError"] == "" {
				t.Errorf("expected non-empty ParseError")
			}
			if !tt.wantParseError && got["ParseError"] != "" {
				t.Errorf("expected empty ParseError, got %q", got["ParseError"])
			}
			if tt.wantKeyType != "" && got["KeyType"] != tt.wantKeyType {
				t.Errorf("KeyType = %v, want %v", got["KeyType"], tt.wantKeyType)
			}
			if tt.wantKeyType != "" {
				fp, _ := got["Fingerprint"].(string)
				if !strings.HasPrefix(fp, "SHA256:") {
					t.Errorf("Fingerprint = %q, want SHA256: prefix", fp)
				}
			}
		})
	}
}

func TestParseServiceAccountTokenSecret(t *testing.T) {
	secretWith := func(annotations map[string]interface{}, data map[string]string) RenderableObject {
		encoded := map[string]interface{}{}
		for k, v := range data {
			encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		obj := map[string]interface{}{
			"type": "kubernetes.io/service-account-token",
			"data": encoded,
			"metadata": map[string]interface{}{
				"annotations": annotations,
			},
		}
		return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
	}

	t.Run("no service-account.name annotation", func(t *testing.T) {
		got := parseServiceAccountTokenSecret(secretWith(nil, nil))
		if got["HasServiceAccountName"] != false {
			t.Errorf("HasServiceAccountName = %v, want false", got["HasServiceAccountName"])
		}
		if got["HasToken"] != false {
			t.Errorf("HasToken = %v, want false", got["HasToken"])
		}
	})

	t.Run("annotation present but token not yet populated", func(t *testing.T) {
		got := parseServiceAccountTokenSecret(secretWith(map[string]interface{}{"kubernetes.io/service-account.name": "default"}, nil))
		if got["HasServiceAccountName"] != true {
			t.Errorf("HasServiceAccountName = %v, want true", got["HasServiceAccountName"])
		}
		if got["ServiceAccountName"] != "default" {
			t.Errorf("ServiceAccountName = %v, want default", got["ServiceAccountName"])
		}
		if got["HasToken"] != false {
			t.Errorf("HasToken = %v, want false", got["HasToken"])
		}
	})

	t.Run("populated by controller", func(t *testing.T) {
		got := parseServiceAccountTokenSecret(secretWith(map[string]interface{}{"kubernetes.io/service-account.name": "default"}, map[string]string{"token": "eyJ..."}))
		if got["HasServiceAccountName"] != true {
			t.Errorf("HasServiceAccountName = %v, want true", got["HasServiceAccountName"])
		}
		if got["HasToken"] != true {
			t.Errorf("HasToken = %v, want true", got["HasToken"])
		}
	})
}

func bootstrapTokenSecret(namespace, name string, data map[string]string) RenderableObject {
	encoded := map[string]interface{}{}
	for k, v := range data {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	obj := map[string]interface{}{
		"type": "bootstrap.kubernetes.io/token",
		"data": encoded,
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
	}
	return RenderableObject{Unstructured: unstructured.Unstructured{Object: obj}}
}

func TestParseBootstrapTokenSecret(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	ApplyTestHack(cfg)

	validData := map[string]string{
		"token-id":                       "abc123",
		"token-secret":                   "0123456789abcdef",
		"usage-bootstrap-authentication": "true",
		"usage-bootstrap-signing":        "true",
		"expiration":                     "2027-01-01T00:00:00Z",
	}

	t.Run("fully valid", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", validData))
		for key, want := range map[string]interface{}{
			"NamespaceOK":         true,
			"NameOK":              true,
			"TokenIDValid":        true,
			"TokenIDMatchesName":  true,
			"TokenSecretPresent":  true,
			"TokenSecretValid":    true,
			"HasExpiration":       true,
			"Expired":             false,
			"UsageAuthentication": true,
			"UsageSigning":        true,
		} {
			if got[key] != want {
				t.Errorf("%s = %v, want %v", key, got[key], want)
			}
		}
	})

	t.Run("wrong namespace", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("default", "bootstrap-token-abc123", validData))
		if got["NamespaceOK"] != false {
			t.Errorf("NamespaceOK = %v, want false", got["NamespaceOK"])
		}
	})

	t.Run("name doesn't match pattern", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "my-bootstrap-token", validData))
		if got["NameOK"] != false {
			t.Errorf("NameOK = %v, want false", got["NameOK"])
		}
	})

	t.Run("token-id doesn't match name suffix", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-zzzzzz", validData))
		if got["TokenIDValid"] != true {
			t.Errorf("TokenIDValid = %v, want true", got["TokenIDValid"])
		}
		if got["TokenIDMatchesName"] != false {
			t.Errorf("TokenIDMatchesName = %v, want false", got["TokenIDMatchesName"])
		}
	})

	t.Run("missing token-id and token-secret", func(t *testing.T) {
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", map[string]string{}))
		want := []string{"token-id", "token-secret"}
		if !reflect.DeepEqual(got["MissingKeys"], want) {
			t.Errorf("MissingKeys = %v, want %v", got["MissingKeys"], want)
		}
	})

	t.Run("malformed token-id", func(t *testing.T) {
		data := map[string]string{"token-id": "BAD-ID", "token-secret": validData["token-secret"]}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["TokenIDValid"] != false {
			t.Errorf("TokenIDValid = %v, want false", got["TokenIDValid"])
		}
	})

	t.Run("malformed token-secret", func(t *testing.T) {
		data := map[string]string{"token-id": validData["token-id"], "token-secret": "too-short"}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["TokenSecretValid"] != false {
			t.Errorf("TokenSecretValid = %v, want false", got["TokenSecretValid"])
		}
	})

	t.Run("expired", func(t *testing.T) {
		data := map[string]string{"token-id": validData["token-id"], "token-secret": validData["token-secret"], "expiration": "2020-01-01T00:00:00Z"}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["HasExpiration"] != true {
			t.Errorf("HasExpiration = %v, want true", got["HasExpiration"])
		}
		if got["Expired"] != true {
			t.Errorf("Expired = %v, want true", got["Expired"])
		}
	})

	t.Run("no expiration set", func(t *testing.T) {
		data := map[string]string{"token-id": validData["token-id"], "token-secret": validData["token-secret"]}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["HasExpiration"] != false {
			t.Errorf("HasExpiration = %v, want false", got["HasExpiration"])
		}
	})

	t.Run("usage flags absent default to disabled", func(t *testing.T) {
		data := map[string]string{"token-id": validData["token-id"], "token-secret": validData["token-secret"]}
		got := cfg.parseBootstrapTokenSecret(bootstrapTokenSecret("kube-system", "bootstrap-token-abc123", data))
		if got["UsageAuthentication"] != false {
			t.Errorf("UsageAuthentication = %v, want false", got["UsageAuthentication"])
		}
		if got["UsageSigning"] != false {
			t.Errorf("UsageSigning = %v, want false", got["UsageSigning"])
		}
	})
}

func TestSecretDataKeys(t *testing.T) {
	tests := []struct {
		name   string
		secret RenderableObject
		want   []string
	}{
		{
			name:   "secret not found",
			secret: RenderableObject{Unstructured: unstructured.Unstructured{Object: nil}},
			want:   nil,
		},
		{
			name:   "data keys sorted",
			secret: dataSecret("Opaque", map[string]string{"z": "1", "a": "2"}),
			want:   []string{"a", "z"},
		},
		{
			name: "data and stringData deduplicated",
			secret: RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{
				"type": "Opaque",
				"data": map[string]interface{}{"a": base64.StdEncoding.EncodeToString([]byte("1"))},
				"stringData": map[string]interface{}{
					"a": "1",
					"b": "2",
				},
			}}},
			want: []string{"a", "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := secretDataKeys(tt.secret)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("secretDataKeys() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
