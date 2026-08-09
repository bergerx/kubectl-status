package plugin

import (
	"sort"
	"strconv"

	"github.com/spf13/cast"
)

// sortByRevisionAnnotation returns a sorted copy of objs (RenderableObject ReplicaSets, passed as
// []interface{} since that's what the "list"/"append" template builtins produce) ordered by their
// "deployment.kubernetes.io/revision" annotation, ascending. Unlike creationTimestamp (which only
// has second resolution, so ReplicaSets created within the same rollout can tie), the revision
// annotation is a reliable total order for a Deployment's ReplicaSets.
func sortByRevisionAnnotation(objs []interface{}) []interface{} {
	result := append([]interface{}{}, objs...)
	sort.SliceStable(result, func(i, j int) bool {
		return revisionAnnotationInt(result[i]) < revisionAnnotationInt(result[j])
	})
	return result
}

func revisionAnnotationInt(obj interface{}) int {
	r, ok := obj.(RenderableObject)
	if !ok {
		return 0
	}
	v, _ := r.Annotations()["deployment.kubernetes.io/revision"].(string)
	n, _ := strconv.Atoi(v)
	return n
}

// sortByRevisionField returns a sorted copy of objs (ControllerRevisions) ordered by their
// numeric top-level "revision" field, ascending, for the same tie-breaking reason as
// sortByRevisionAnnotation.
func sortByRevisionField(objs []interface{}) []interface{} {
	result := append([]interface{}{}, objs...)
	sort.SliceStable(result, func(i, j int) bool {
		return revisionFieldInt(result[i]) < revisionFieldInt(result[j])
	})
	return result
}

func revisionFieldInt(obj interface{}) int {
	r, ok := obj.(RenderableObject)
	if !ok {
		return 0
	}
	n, _ := cast.ToIntE(r.Object["revision"])
	return n
}
