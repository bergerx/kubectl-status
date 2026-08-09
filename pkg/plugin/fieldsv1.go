package plugin

import (
	"sort"
	"strconv"
	"strings"
)

// fieldsV1Paths reduces a managedFields entry's FieldsV1 tree (as decoded from
// JSON, with nested keys prefixed "f:") to the deepest common path per
// top-level branch: it descends into a branch while every node along the way
// has exactly one "f:"-prefixed child, and reports that node's path once it
// hits a fork (or a leaf), so touching only spec.template yields "spec.template"
// but touching both status.conditions and status.phase yields just "status".
func fieldsV1Paths(fieldsV1 map[string]interface{}) []string {
	var paths []string
	for key, value := range fieldsV1 {
		if !strings.HasPrefix(key, "f:") {
			continue
		}
		segments := fieldsV1DeepestPath([]string{strings.TrimPrefix(key, "f:")}, value)
		paths = append(paths, joinFieldsV1Segments(segments))
	}
	sort.Strings(paths)
	return paths
}

// fieldsV1DeepestPath descends into a branch of the FieldsV1 tree while every
// node along the way has exactly one "f:"-prefixed child, returning the
// segments collected so far once it hits a fork or a leaf. This means
// touching only spec.template yields ["spec","template"], but touching both
// status.conditions and status.phase yields just ["status"]. A single owned
// label/annotation (e.g. metadata.labels.app) descends the same way, since
// its key is just another "f:"-prefixed child.
func fieldsV1DeepestPath(segments []string, value interface{}) []string {
	node, ok := value.(map[string]interface{})
	if !ok {
		return segments
	}
	var childKey string
	for key := range node {
		if !strings.HasPrefix(key, "f:") {
			continue
		}
		if childKey != "" {
			// more than one field child at this level: this is as deep as we can go
			return segments
		}
		childKey = key
	}
	if childKey == "" {
		return segments
	}
	return fieldsV1DeepestPath(append(segments, strings.TrimPrefix(childKey, "f:")), node[childKey])
}

// joinFieldsV1Segments joins path segments with ".", quoting any segment that
// itself contains a "." (e.g. the annotation key "deployment.kubernetes.io/
// revision") so it isn't misread as further nesting.
func joinFieldsV1Segments(segments []string) string {
	quoted := make([]string, len(segments))
	for i, segment := range segments {
		if strings.Contains(segment, ".") {
			quoted[i] = strconv.Quote(segment)
		} else {
			quoted[i] = segment
		}
	}
	return strings.Join(quoted, ".")
}
