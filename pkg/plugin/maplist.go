package plugin

import (
	"sort"
	"strings"

	"k8s.io/klog/v2"
)

// getMatchingItemInMapList checks if the provided searchFor map is a subset of an item in the given mapList.
// Returns the first matching item.
//
// mapList parameter should actually be a "[]map[string]interface{}" but due to unstructured json serialisation
// we need to use "[]interface{}" and cast it inside.
//
// searchFor parameter should actually be a "map[string]string" but due to unstructured json serialisation
// we need to use "map[string]interface{}" and cast the value to string inside.
func getMatchingItemInMapList(searchFor map[string]interface{}, mapList []interface{}) (item map[string]interface{}) {
	for _, untypedMapListItem := range mapList {
		typedMapListItem := untypedMapListItem.(map[string]interface{})
		if hasMapListAMatchingItem(searchFor, typedMapListItem) {
			klog.V(5).InfoS("getMatchingItemInMapList found a matching item", "typedMapListItem", typedMapListItem)
			return typedMapListItem
		}
	}
	klog.V(5).InfoS("getMatchingItemInMapList couldn't find any matching item", "searchFor", searchFor, "typedMapListItem", mapList)
	return
}

func hasMapListAMatchingItem(searchFor map[string]interface{}, typedMapListItem map[string]interface{}) bool {
	klog.V(5).InfoS("hasMapListAMatchingItem will search", "searchFor", searchFor, "typedMapListItem", typedMapListItem)
	if len(searchFor) == 0 {
		return false
	}
	for searchKey, searchValue := range searchFor {
		if searchValue == nil {
			continue
		}
		if strings.Contains(searchKey, ".") {
			splitSearchKey := strings.SplitN(searchKey, ".", 2)
			outerKey := splitSearchKey[0]
			innerMapListItem, exists := typedMapListItem[outerKey]
			if !exists {
				return false
			}
			innerTypedMapListItem, ok := innerMapListItem.(map[string]interface{})
			if !ok {
				return false
			}
			innerKey := splitSearchKey[1]
			innerSearchFor := map[string]interface{}{innerKey: searchValue}
			if !hasMapListAMatchingItem(innerSearchFor, innerTypedMapListItem) {
				return false
			}
			continue
		}
		mapListItem, exists := typedMapListItem[searchKey]
		if !exists || mapListItem == nil {
			return false
		}
		mapListItemValue, ok := mapListItem.(string)
		if !ok {
			return false
		}
		searchForValue, ok := searchValue.(string)
		if !ok {
			return false
		}
		if mapListItemValue != searchForValue {
			return false
		}
	}
	return true
}

// sortMapListByKeysValue returns a sorted copy of mapList based on the provided key's value.
//
// mapList parameter should actually be a "[]map[string]interface{}" but due to unstructured json serialisation
// we need to use "[]interface{}" and cast it inside.
func sortMapListByKeysValue(key string, mapList []interface{}) (result []interface{}) {
	result = append(result, mapList...)
	sort.SliceStable(result, func(i, j int) bool {
		var typedMapListItemI, typedMapListItemJ string
		if mapI, ok := result[i].(map[string]interface{}); ok {
			typedMapListItemI, _ = mapI[key].(string)
		}
		if mapJ, ok := result[j].(map[string]interface{}); ok {
			typedMapListItemJ, _ = mapJ[key].(string)
		}
		return typedMapListItemI < typedMapListItemJ
	})
	return
}

// sortMapListByFloatKeysValueDesc returns a sorted copy of mapList in descending order of the
// given key's float64 value, e.g. ranking a node's pods by measured resource usage without a
// second apiserver round trip: callers accumulate the usage as a float64 while they already have
// the metrics at hand, and only need the ordering applied once, at render time.
func sortMapListByFloatKeysValueDesc(key string, mapList []interface{}) (result []interface{}) {
	result = append(result, mapList...)
	sort.SliceStable(result, func(i, j int) bool {
		var typedMapListItemI, typedMapListItemJ float64
		if mapI, ok := result[i].(map[string]interface{}); ok {
			typedMapListItemI, _ = mapI[key].(float64)
		}
		if mapJ, ok := result[j].(map[string]interface{}); ok {
			typedMapListItemJ, _ = mapJ[key].(float64)
		}
		return typedMapListItemI > typedMapListItemJ
	})
	return
}
