package plugin

import (
	"strconv"
)

// toInterfaceMapSlice converts an unstructured field (expected to be a []interface{} of
// map[string]interface{} elements, e.g. matchExpressions/matchFields) into []map[string]interface{},
// tolerating (by skipping) any element that isn't shaped like one -- unstructured content from the
// API server is never guaranteed to match the expected shape.
func toInterfaceMapSlice(field interface{}) []map[string]interface{} {
	items, _ := field.([]interface{})
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]interface{}); ok {
			result = append(result, m)
		}
	}
	return result
}

// toStringSlice converts an unstructured []interface{} of strings (e.g. a NodeSelectorRequirement
// "values" field) into a []string, skipping any non-string element.
func toStringSlice(values interface{}) []string {
	items, _ := values.([]interface{})
	result := make([]string, 0, len(items))
	for _, v := range items {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// stringSliceContains reports whether s is present in values.
func stringSliceContains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

// numericLess reports whether a < b, treating both as base-10 integers. A non-integer operand
// (malformed input) never satisfies the comparison, matching Requirement.Matches' behavior of
// treating unparsable Gt/Lt values as non-matching rather than erroring.
func numericLess(a, b string) bool {
	aInt, aErr := strconv.ParseInt(a, 10, 64)
	bInt, bErr := strconv.ParseInt(b, 10, 64)
	return aErr == nil && bErr == nil && aInt < bInt
}

// stringSlicesIntersect reports whether a and b share at least one element.
func stringSlicesIntersect(a, b []string) bool {
	for _, v := range a {
		if stringSliceContains(b, v) {
			return true
		}
	}
	return false
}

// stringSliceSubset reports whether every element of a is present in b.
func stringSliceSubset(a, b []string) bool {
	for _, v := range a {
		if !stringSliceContains(b, v) {
			return false
		}
	}
	return true
}
