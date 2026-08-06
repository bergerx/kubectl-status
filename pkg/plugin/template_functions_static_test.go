package plugin

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
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

func TestEvictionHeadroomPercentThreshold(t *testing.T) {
	// nodefs.available<10%, actual disk currently 73.6% free (34.8GB/47.3GB): plenty of headroom.
	got := evictionHeadroom("10%", 34.8e9, 47.3e9, "B")
	if !got.OK || got.Tripped || got.AtRisk {
		t.Fatalf("evictionHeadroom() = %+v, want OK, not tripped, not at risk", got)
	}
	if got.Current != "74% free" {
		t.Errorf("Current = %q, want %q", got.Current, "74% free")
	}
}

func TestEvictionHeadroomPercentThresholdAtRisk(t *testing.T) {
	// threshold 10%, currently 12% free: above the threshold but within 1.5x of tripping it.
	got := evictionHeadroom("10%", 1.2, 10, "")
	if !got.OK || got.Tripped || !got.AtRisk {
		t.Fatalf("evictionHeadroom() = %+v, want OK, not tripped, at risk", got)
	}
}

func TestEvictionHeadroomPercentThresholdTripped(t *testing.T) {
	// threshold 10%, currently 5% free: already past the threshold.
	got := evictionHeadroom("10%", 0.5, 10, "")
	if !got.OK || !got.Tripped {
		t.Fatalf("evictionHeadroom() = %+v, want OK and tripped", got)
	}
}

func TestEvictionHeadroomAbsoluteThreshold(t *testing.T) {
	// memory.available<100Mi, node currently reports 11.4GB available: no total needed.
	got := evictionHeadroom("100Mi", 11.4e9, 0, "B")
	if !got.OK || got.Tripped || got.AtRisk {
		t.Fatalf("evictionHeadroom() = %+v, want OK, not tripped, not at risk", got)
	}
}

func TestEvictionHeadroomPercentThresholdWithoutTotalIsNotOK(t *testing.T) {
	// A percent threshold can't be normalized without knowing the resource's total capacity.
	got := evictionHeadroom("10%", 34.8e9, 0, "B")
	if got.OK {
		t.Fatalf("evictionHeadroom() = %+v, want OK=false when total is unknown", got)
	}
	if got.Threshold != "10%" {
		t.Errorf("Threshold = %q, want %q (raw threshold preserved for fallback display)", got.Threshold, "10%")
	}
}

func TestEvictionHeadroomUnparseableThresholdIsNotOK(t *testing.T) {
	got := evictionHeadroom("not-a-number", 1, 1, "")
	if got.OK {
		t.Fatalf("evictionHeadroom() = %+v, want OK=false for an unparseable threshold", got)
	}
}

func TestEvictionAnnotationTrippedDoesNotCorruptOnPercentSign(t *testing.T) {
	// Regression test: the message always contains a literal "%" from the threshold/current
	// percentages. evictionAnnotation must build it with Sprint, not Sprintf -- piping the composed
	// string back through a Sprintf-based colorer (as an earlier version of this code did via the
	// template's "bold" func) misparses "% " followed by a verb letter (T, f, s, ...) as a format
	// verb with a missing argument, e.g. "10% TRIPPED" corrupts to "10%!T(MISSING)RIPPED".
	got := evictionAnnotation("10%", 0.5, 10, "")
	want := " (10% TRIPPED: 5% free)"
	if got != want {
		t.Fatalf("evictionAnnotation() = %q, want %q", got, want)
	}
}

func TestEvictionAnnotationAtRisk(t *testing.T) {
	got := evictionAnnotation("10%", 1.2, 10, "")
	want := " (nearing 10%: 12% free)"
	if got != want {
		t.Fatalf("evictionAnnotation() = %q, want %q", got, want)
	}
}

func TestEvictionAnnotationEmptyWhenHealthy(t *testing.T) {
	got := evictionAnnotation("10%", 9.0, 10, "")
	if got != "" {
		t.Fatalf("evictionAnnotation() = %q, want empty string when not at risk", got)
	}
}

func TestRenderGroupedTableAlignsOnVisibleWidthNotByteLength(t *testing.T) {
	// Data column widths must be computed from each cell's visible (ANSI-escape-stripped) width,
	// not its byte length: fatih/color's escape codes add bytes a real terminal doesn't render,
	// so byte-based padding would misalign colored output while looking fine in escape-free e2e
	// artifacts (color.NoColor is true when stdout isn't a TTY). Here "short" is colored (more
	// bytes, same visible width as "xyz") and sets column 0's width jointly with "xyz".
	short := "\x1b[31mab\x1b[0m" // visible width 2, byte length 11
	rows := []interface{}{
		[]interface{}{short, "1", "22"},
		[]interface{}{"xyz", "5", "6"},
	}
	got := renderGroupedTable("pod", []interface{}{"grp"}, []interface{}{3}, rows)
	want := "pod  grp\n" + short + "   1  22\nxyz  5  6"
	if got != want {
		t.Fatalf("renderGroupedTable() = %q, want %q", got, want)
	}
}

func TestRenderGroupedTableGroupLabelDoesNotStretchDataColumns(t *testing.T) {
	// A group's spanning label (e.g. Node.tmpl's "cpu use/req/lim (%allocatable)") is typically
	// far wider than the data underneath it -- it must not stretch that data's column widths to
	// fit, or a group's own use/req/lim values end up separated by all the extra padding instead
	// of sitting together.
	rows := []interface{}{
		[]interface{}{"row", "1", "2", "3"},
	}
	got := renderGroupedTable("pod", []interface{}{"cpu use/req/lim (%allocatable)"}, []interface{}{3}, rows)
	want := "pod  cpu use/req/lim (%allocatable)\nrow  1  2  3"
	if got != want {
		t.Fatalf("renderGroupedTable() = %q, want %q", got, want)
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

	// A wildcard certificate as cert-manager issues one, and an apex-only one, to exercise
	// wildcard expected hostnames (Gateway listener/route hostnames and Ingress hosts) on both
	// sides of the comparison.
	wildcardPEM, _, wildcardKey := generateTestCert(t, genCertOptions{
		subjectCN:  "*.example.com",
		dnsNames:   []string{"*.example.com"},
		selfSigned: true,
	})
	apexPEM, _, apexKey := generateTestCert(t, genCertOptions{
		subjectCN:  "example.com",
		dnsNames:   []string{"example.com"},
		selfSigned: true,
	})
	// Pre-RFC-6125 certificate naming the server only in its subject CN, with no SAN extension.
	cnOnlyPEM, _, cnOnlyKey := generateTestCert(t, genCertOptions{
		subjectCN:  "*.cn-only.example.com",
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
			// A "*.example.com" listener only ever routes names the certificate covers, so
			// flagging this is a false alarm -- x509.VerifyHostname can't tell, since it treats
			// its argument as a concrete server name.
			name:     "wildcard expected hostname is served by a concrete SAN below it",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "*.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard expected hostname is served by an identical wildcard SAN",
			secret:   tlsSecret("kubernetes.io/tls", wildcardPEM, keyPEMBytes(wildcardKey)),
			hostname: "*.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard expected hostname is served by a deeper wildcard SAN",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "*.wild.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard expected hostname mismatch on a sibling subdomain",
			secret:   tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey)),
			hostname: "*.other.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "wildcard expected hostname mismatch on an unrelated domain",
			secret:   tlsSecret("kubernetes.io/tls", wildcardPEM, keyPEMBytes(wildcardKey)),
			hostname: "*.example.org",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			// "*.example.com" never routes the apex, so an apex-only certificate serves nothing
			// the listener accepts.
			name:     "wildcard expected hostname is not served by the bare suffix",
			secret:   tlsSecret("kubernetes.io/tls", apexPEM, keyPEMBytes(apexKey)),
			hostname: "*.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			// A certificate wildcard stands for exactly one label (RFC 6125), so "*.example.com"
			// covers no name under "foo.example.com".
			name:     "shallower wildcard SAN doesn't serve a deeper wildcard expected hostname",
			secret:   tlsSecret("kubernetes.io/tls", wildcardPEM, keyPEMBytes(wildcardKey)),
			hostname: "*.foo.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "CN-only certificate matches via its CommonName wildcard",
			secret:   tlsSecret("kubernetes.io/tls", cnOnlyPEM, keyPEMBytes(cnOnlyKey)),
			hostname: "foo.cn-only.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "CN-only certificate matches a wildcard expected hostname",
			secret:   tlsSecret("kubernetes.io/tls", cnOnlyPEM, keyPEMBytes(cnOnlyKey)),
			hostname: "*.cn-only.example.com",
			want: map[string]interface{}{
				"MatchesHostname": true,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "CN-only certificate wildcard spans a single label only",
			secret:   tlsSecret("kubernetes.io/tls", cnOnlyPEM, keyPEMBytes(cnOnlyKey)),
			hostname: "foo.bar.cn-only.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			name:     "CN-only certificate mismatch",
			secret:   tlsSecret("kubernetes.io/tls", cnOnlyPEM, keyPEMBytes(cnOnlyKey)),
			hostname: "foo.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
			},
			checkKeysOnly: []string{"MatchesHostname"},
		},
		{
			// The SAN extension is authoritative when present: a certificate with SANs is not
			// rescued by a CommonName that happens to match.
			name:     "CommonName is ignored when the certificate has SANs",
			secret:   tlsSecret("kubernetes.io/tls", cnNotFirstPEM, keyPEMBytes(cnNotFirstKey)),
			hostname: "other.example.com",
			want: map[string]interface{}{
				"MatchesHostname": false,
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

// Routes carry a list of hostnames, so the templates hand parseTLSSecretCertificate every
// hostname a listener serves for the route rather than only the single-hostname case they used to
// restrict themselves to.
func TestParseTLSSecretCertificateHostnameLists(t *testing.T) {
	cfg := NewRenderConfig(viper.New())
	leafPEM, _, leafKey := generateTestCert(t, genCertOptions{
		subjectCN:  "leaf.example.com",
		dnsNames:   []string{"leaf.example.com", "*.wild.example.com"},
		selfSigned: true,
	})
	secret := tlsSecret("kubernetes.io/tls", leafPEM, keyPEMBytes(leafKey))

	tests := []struct {
		name      string
		hostnames interface{}
		want      bool
	}{
		{"empty list expects nothing", []string{}, true},
		{"nil expects nothing", nil, true},
		{"list of empty strings expects nothing", []string{"", ""}, true},
		{"single-entry list matches", []string{"leaf.example.com"}, true},
		{"one served hostname among several is enough", []string{"nope.example.com", "leaf.example.com"}, true},
		{"no served hostname in the list", []string{"nope.example.com", "other.example.com"}, false},
		{"wildcard entry in the list", []string{"*.wild.example.com"}, true},
		// Templates range over unstructured data, so lists reach here as []interface{}.
		{"untyped list from unstructured data", []interface{}{"leaf.example.com"}, true},
		{"plain string is still accepted", "leaf.example.com", true},
		// A hostname with a space in it can't match anything, but must not be split into two
		// hostnames either (which is what cast.ToStringSlice would do to a bare string).
		{"string is not split on whitespace", "nope.example.com leaf.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.parseTLSSecretCertificate(secret, tt.hostnames)["MatchesHostname"]
			if got != tt.want {
				t.Errorf("parseTLSSecretCertificate(secret, %#v)[MatchesHostname] = %v, want %v", tt.hostnames, got, tt.want)
			}
		})
	}
}

func TestQualifyKind(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		group string
		want  string
	}{
		{"empty group returns kind unchanged", "Gateway", "", "Gateway"},
		{"non-empty group qualifies the kind", "Gateway", "gateway.networking.k8s.io", "Gateway.gateway.networking.k8s.io"},
		{"a different group produces a different qualified kind", "Gateway", "networking.istio.io", "Gateway.networking.istio.io"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qualifyKind(tt.kind, tt.group); got != tt.want {
				t.Errorf("qualifyKind(%q, %q) = %q, want %q", tt.kind, tt.group, got, tt.want)
			}
		})
	}
}

func TestHostnameIntersections(t *testing.T) {
	tests := []struct {
		name             string
		listenerHostname string
		routeHostnames   interface{}
		want             []string
	}{
		{
			name:             "route pins no hostname, so nothing narrows the listener",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{},
			want:             nil,
		},
		{
			name:             "listener serves every hostname, so the route's stand unnarrowed",
			listenerHostname: "",
			routeHostnames:   []string{"a.example.com", "*.b.example.com"},
			want:             []string{"a.example.com", "*.b.example.com"},
		},
		{
			name:             "identical concrete hostnames",
			listenerHostname: "a.example.com",
			routeHostnames:   []string{"a.example.com"},
			want:             []string{"a.example.com"},
		},
		{
			name:             "different concrete hostnames are disjoint",
			listenerHostname: "a.example.com",
			routeHostnames:   []string{"b.example.com"},
			want:             nil,
		},
		{
			// The listener attaches the route, and the pair only ever serves the route's
			// concrete hostname -- so that, not the wildcard, is what the cert must cover.
			name:             "wildcard listener narrows to the route's concrete hostname",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{"a.example.com", "deep.a.example.com", "other.org"},
			want:             []string{"a.example.com", "deep.a.example.com"},
		},
		{
			name:             "concrete listener narrows a wildcard route to itself",
			listenerHostname: "a.example.com",
			routeHostnames:   []string{"*.example.com"},
			want:             []string{"a.example.com"},
		},
		{
			name:             "a wildcard never covers its own suffix",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{"example.com"},
			want:             nil,
		},
		{
			name:             "two wildcards keep the deeper one",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{"*.apps.example.com"},
			want:             []string{"*.apps.example.com"},
		},
		{
			name:             "two wildcards keep the deeper one, listener side",
			listenerHostname: "*.apps.example.com",
			routeHostnames:   []string{"*.example.com"},
			want:             []string{"*.apps.example.com"},
		},
		{
			name:             "identical wildcards",
			listenerHostname: "*.example.com",
			routeHostnames:   []string{"*.example.com"},
			want:             []string{"*.example.com"},
		},
		{
			name:             "sibling wildcards are disjoint",
			listenerHostname: "*.a.example.com",
			routeHostnames:   []string{"*.b.example.com"},
			want:             nil,
		},
		{
			name:             "matching is case-insensitive",
			listenerHostname: "*.EXAMPLE.com",
			routeHostnames:   []string{"A.example.COM"},
			want:             []string{"A.example.COM"},
		},
		{
			name:             "untyped list from unstructured data",
			listenerHostname: "*.example.com",
			routeHostnames:   []interface{}{"a.example.com"},
			want:             []string{"a.example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hostnameIntersections(tt.listenerHostname, tt.routeHostnames)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("hostnameIntersections(%q, %#v) = %#v, want %#v", tt.listenerHostname, tt.routeHostnames, got, tt.want)
			}
		})
	}
}

func TestIstioHost(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		namespace string
		want      IstioHostRef
	}{
		{
			name:      "short name resolves against the naming object's namespace",
			host:      "reviews",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.bookinfo", Name: "reviews", Namespace: "bookinfo", InCluster: true},
		},
		{
			name:      "namespaced name carries its own namespace",
			host:      "reviews.other",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			name:      "svc form",
			host:      "reviews.other.svc",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			name:      "fully qualified cluster-local form",
			host:      "reviews.other.svc.cluster.local",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			// The whole point: these four spellings have to pair with each other, which is what
			// a VirtualService destination and its DestinationRule routinely disagree on.
			name:      "trailing dot and casing don't change the key",
			host:      "Reviews.Other.SVC.Cluster.Local.",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			name:      "external host keeps its own spelling and resolves to no Service",
			host:      "api.example.com",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "api.example.com"},
		},
		{
			name:      "a custom cluster domain is still recognised by its svc label",
			host:      "reviews.other.svc.mesh.internal",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "reviews.other", Name: "reviews", Namespace: "other", InCluster: true},
		},
		{
			name:      "wildcard never resolves to a Service",
			host:      "*.example.com",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "*.example.com"},
		},
		{
			name:      "bare wildcard",
			host:      "*",
			namespace: "bookinfo",
			want:      IstioHostRef{Key: "*"},
		},
		{
			name:      "empty host",
			host:      "",
			namespace: "bookinfo",
			want:      IstioHostRef{},
		},
		{
			// Cluster-scoped renders (and -f files with no namespace) have nothing to resolve a
			// short name against, so it stays unresolved rather than pairing with whatever
			// short name happens to match.
			name:      "short name without a namespace to resolve against",
			host:      "reviews",
			namespace: "",
			want:      IstioHostRef{Key: "reviews"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := istioHost(tt.host, tt.namespace)
			if got != tt.want {
				t.Errorf("istioHost(%q, %q) = %#v, want %#v", tt.host, tt.namespace, got, tt.want)
			}
		})
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

func TestStatefulSetRollbackTrapBlocker(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	longUnready := now.Add(-time.Hour).Format(time.RFC3339)
	justUnready := now.Add(-time.Second).Format(time.RFC3339)
	defaultParams := statefulSetRollbackTrapParams{
		namePrefix:     "web-",
		replicas:       3,
		updateRevision: "web-good",
		threshold:      10 * time.Minute,
		now:            now,
	}
	// pod builds a StatefulSet-owned Pod as the trap detection sees it: named with an ordinal,
	// labelled with the revision it was created on, and with a Ready condition.
	pod := func(name, revision, ready, lastTransitionTime string) RenderableObject {
		return RenderableObject{Unstructured: unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"name":              name,
				"labels":            map[string]interface{}{"controller-revision-hash": revision},
				"creationTimestamp": longUnready,
			},
			"status": map[string]interface{}{"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": ready, "lastTransitionTime": lastTransitionTime},
			}},
		}}}
	}
	terminating := func(pod RenderableObject) RenderableObject {
		pod.Object["metadata"].(map[string]interface{})["deletionTimestamp"] = longUnready
		return pod
	}

	tests := []struct {
		name      string
		pods      []RenderableObject
		params    statefulSetRollbackTrapParams
		wantPod   string
		wantFound bool
	}{
		{
			name:      "Pod stuck unready on an outdated revision is the trap",
			pods:      []RenderableObject{pod("web-0", "web-bad", "False", longUnready)},
			wantPod:   "web-0",
			wantFound: true,
		},
		{
			name: "reports the lowest-ordinal unready Pod, the one the sync is stopped at",
			pods: []RenderableObject{
				pod("web-2", "web-bad", "False", longUnready),
				pod("web-1", "web-bad", "False", longUnready),
			},
			wantPod:   "web-1",
			wantFound: true,
		},
		{
			name: "no trap while a lower-ordinal Pod is already on the target revision",
			pods: []RenderableObject{
				// The rollout is stopped at web-0, and deleting web-2 wouldn't get it going.
				pod("web-0", "web-good", "False", longUnready),
				pod("web-2", "web-bad", "False", longUnready),
			},
		},
		{name: "no trap when every Pod is Ready", pods: []RenderableObject{pod("web-0", "web-bad", "True", longUnready)}},
		{name: "no trap when the Pod already reached the target revision", pods: []RenderableObject{pod("web-0", "web-good", "False", longUnready)}},
		{name: "no trap without a revision label", pods: []RenderableObject{pod("web-0", "", "False", longUnready)}},
		{name: "no trap before the unready threshold, still ordinary startup latency", pods: []RenderableObject{pod("web-0", "web-bad", "False", justUnready)}},
		{name: "no trap for an already terminating Pod, the controller is awaiting its deletion", pods: []RenderableObject{terminating(pod("web-0", "web-bad", "False", longUnready))}},
		{
			name:   "no trap below the partition, such a Pod is recreated on the current revision",
			pods:   []RenderableObject{pod("web-0", "web-bad", "False", longUnready)},
			params: statefulSetRollbackTrapParams{namePrefix: "web-", replicas: 3, partition: 1, updateRevision: "web-good", threshold: 10 * time.Minute, now: now},
		},
		{
			name:   "no trap for a condemned Pod outside the replica range",
			pods:   []RenderableObject{pod("web-3", "web-bad", "False", longUnready)},
			params: defaultParams,
		},
		{name: "no trap for a Pod of another workload sharing the selector", pods: []RenderableObject{pod("web-canary-0", "web-bad", "False", longUnready)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := tt.params
			if params.namePrefix == "" {
				params = defaultParams
			}
			gotPod, gotRevision, gotFound := statefulSetRollbackTrapBlocker(tt.pods, params)
			if gotFound != tt.wantFound {
				t.Fatalf("statefulSetRollbackTrapBlocker() found = %v, want %v", gotFound, tt.wantFound)
			}
			if !tt.wantFound {
				return
			}
			if gotPod.Name() != tt.wantPod {
				t.Errorf("statefulSetRollbackTrapBlocker() pod = %q, want %q", gotPod.Name(), tt.wantPod)
			}
			if gotRevision != "web-bad" {
				t.Errorf("statefulSetRollbackTrapBlocker() revision = %q, want %q", gotRevision, "web-bad")
			}
		})
	}
}

// unstructuredFromJSON decodes a manifest into the map form templates see. Written as JSON rather
// than as a Go map literal because quotaRolloutHeadroom converts its inputs to their API types,
// and that conversion is typed: replicas has to arrive as an int64, exactly as it does from the
// apiserver or from a -f manifest, which a hand-written map literal makes easy to get wrong.
func unstructuredFromJSON(t *testing.T, manifest string) map[string]interface{} {
	t.Helper()
	obj := map[string]interface{}{}
	if err := json.Unmarshal([]byte(manifest), &obj); err != nil {
		t.Fatalf("unmarshalling test manifest: %v", err)
	}
	return obj
}

// resourceQuotaJSON is a namespace quota tracking requests.memory, with hard and used given as
// quantity strings the way the quota controller writes them.
func resourceQuotaJSON(hard, used string) string {
	return fmt.Sprintf(`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
		"status":{"hard":{"requests.memory":%q},"used":{"requests.memory":%q}}}`, hard, used)
}

// deploymentJSON is a Deployment of replicas Pods, each requesting 256Mi, with status.replicas
// Pods currently existing and the default (25%) surge allowance.
func deploymentJSON(replicas, existing int, extraSpec string) string {
	return fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"test"},
		"spec":{"replicas":%d,%s"template":{"spec":{"containers":[{"name":"main","resources":{"requests":{"memory":"256Mi"}}}]}}},
		"status":{"replicas":%d}}`, replicas, extraSpec, existing)
}

func TestQuotaRolloutHeadroom(t *testing.T) {
	tests := []struct {
		name          string
		workload      string
		quotas        []string
		wantExtraPods int
		wantShortfall *quotaShortfall
	}{
		{
			// 4 replicas + 25% surge = 5 allowed, 4 exist, so one 256Mi Pod is pending and the
			// quota has 128Mi left for it.
			name:          "rolling update surge doesn't fit the remaining headroom",
			workload:      deploymentJSON(4, 4, ""),
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "256Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:          "no shortfall reported when the surge fits",
			workload:      deploymentJSON(4, 4, ""),
			quotas:        []string{resourceQuotaJSON("2Gi", "1Gi")},
			wantExtraPods: 1,
		},
		{
			// A blocked scale-up needs a Pod per missing replica, not just the surge one.
			name:          "need scales with the number of Pods still missing",
			workload:      deploymentJSON(4, 1, ""),
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 4,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "1Gi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:          "explicit maxSurge percentage is resolved against the replica count",
			workload:      deploymentJSON(4, 4, `"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxSurge":"50%"}},`),
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 2,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "512Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:          "Recreate strategy gets no surge allowance",
			workload:      deploymentJSON(4, 4, `"strategy":{"type":"Recreate"},`),
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 0,
		},
		{
			// A StatefulSet deletes a Pod before creating its replacement, so a rollout at full
			// replica count needs no extra headroom.
			name: "StatefulSet at full replica count needs no headroom",
			workload: `{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"name":"db","namespace":"test"},
				"spec":{"replicas":3,"template":{"spec":{"containers":[{"name":"main","resources":{"requests":{"memory":"256Mi"}}}]}}},
				"status":{"replicas":3}}`,
			quotas:        []string{resourceQuotaJSON("2Gi", "2Gi")},
			wantExtraPods: 0,
		},
		{
			name: "StatefulSet scale-up blocked by quota",
			workload: `{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"name":"db","namespace":"test"},
				"spec":{"replicas":3,"template":{"spec":{"containers":[{"name":"main","resources":{"requests":{"memory":"256Mi"}}}]}}},
				"status":{"replicas":2}}`,
			quotas:        []string{resourceQuotaJSON("2Gi", "2Gi")},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "256Mi", Free: "0", Used: "2Gi", Hard: "2Gi"},
		},
		{
			// Sidecars (restarting initContainers) run alongside the main containers so they add
			// up, which upstream's PodRequests handles -- 256Mi + 64Mi here.
			name: "sidecar requests count towards the Pod's total",
			workload: `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"test"},
				"spec":{"replicas":1,"template":{"spec":{
					"containers":[{"name":"main","resources":{"requests":{"memory":"256Mi"}}}],
					"initContainers":[{"name":"proxy","restartPolicy":"Always","resources":{"requests":{"memory":"64Mi"}}},
						{"name":"setup","resources":{"requests":{"memory":"128Mi"}}}]}}},
				"status":{"replicas":1}}`,
			quotas:        []string{resourceQuotaJSON("2Gi", "1920Mi")},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "requests.memory", Need: "320Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:     "a pods count quota is measured in Pods, not bytes",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"status":{"hard":{"pods":"4"},"used":{"pods":"4"}}}`},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "pods", Need: "1", Free: "0", Used: "4", Hard: "4"},
		},
		{
			// Whether these Pods fall in the scope isn't decidable here, so the quota is skipped
			// rather than reported against.
			name:     "scoped quota is skipped",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"spec":{"scopes":["BestEffort"]},
				"status":{"hard":{"requests.memory":"2Gi"},"used":{"requests.memory":"2Gi"}}}`},
			wantExtraPods: 1,
		},
		{
			name:     "object count quota the Pods don't consume is skipped",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"objects","namespace":"test"},
				"status":{"hard":{"count/deployments.apps":"1"},"used":{"count/deployments.apps":"1"}}}`},
			wantExtraPods: 1,
		},
		{
			// status.used not yet reported for a tracked resource means unknown free capacity,
			// which must not be read as the full hard limit being available either way.
			name:     "resource missing from status.used is skipped",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"status":{"hard":{"requests.memory":"2Gi"},"used":{}}}`},
			wantExtraPods: 1,
		},
		{
			// The bare "memory"/"cpu" quota names are aliases of the requests form.
			name:     "bare memory quota name is treated as requests.memory",
			workload: deploymentJSON(4, 4, ""),
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"status":{"hard":{"memory":"2Gi"},"used":{"memory":"1920Mi"}}}`},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "memory", Need: "256Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name: "limits quota is compared against the Pod's limits, not its requests",
			workload: `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"test"},
				"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"main","resources":{
					"requests":{"memory":"256Mi"},"limits":{"memory":"512Mi"}}}]}}},
				"status":{"replicas":1}}`,
			quotas: []string{`{"apiVersion":"v1","kind":"ResourceQuota","metadata":{"name":"compute","namespace":"test"},
				"status":{"hard":{"limits.memory":"2Gi"},"used":{"limits.memory":"1920Mi"}}}`},
			wantExtraPods: 1,
			wantShortfall: &quotaShortfall{Resource: "limits.memory", Need: "512Mi", Free: "128Mi", Used: "1920Mi", Hard: "2Gi"},
		},
		{
			name:          "a kind with no rollout of its own reports nothing",
			workload:      `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web","namespace":"test"}}`,
			quotas:        []string{resourceQuotaJSON("2Gi", "2Gi")},
			wantExtraPods: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var quotas []interface{}
			for _, quota := range tt.quotas {
				quotas = append(quotas, unstructuredFromJSON(t, quota))
			}
			got := quotaRolloutHeadroom(quotas, unstructuredFromJSON(t, tt.workload))
			if got.ExtraPods != tt.wantExtraPods {
				t.Errorf("quotaRolloutHeadroom() ExtraPods = %d, want %d", got.ExtraPods, tt.wantExtraPods)
			}
			if tt.wantShortfall == nil {
				if len(got.Quotas) != 0 {
					t.Fatalf("quotaRolloutHeadroom() Quotas = %+v, want none", got.Quotas)
				}
				return
			}
			if len(got.Quotas) != 1 || len(got.Quotas[0].Shortfalls) != 1 {
				t.Fatalf("quotaRolloutHeadroom() Quotas = %+v, want exactly one shortfall", got.Quotas)
			}
			if got.Quotas[0].Shortfalls[0] != *tt.wantShortfall {
				t.Errorf("quotaRolloutHeadroom() shortfall = %+v, want %+v", got.Quotas[0].Shortfalls[0], *tt.wantShortfall)
			}
		})
	}
}
