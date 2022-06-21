package plugin

import (
	"reflect"
	"testing"
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
	t.Parallel()
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
