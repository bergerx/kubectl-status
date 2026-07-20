// Package crossplanedrift computes a leaf-field comparison between a Crossplane managed
// resource's desired configuration (spec.forProvider) and the provider-observed external state
// (status.atProvider).
//
// status.atProvider is commonly a superset of spec.forProvider: it carries provider-observed
// values including API-defaulted fields, and Crossplane may late-initialize desired fields back
// into spec.forProvider. Observe-only imports also populate status.atProvider before a resource
// is fully managed. Because of this, the comparison is deliberately one-directional and
// conservative: it only walks the leaf paths configured in spec.forProvider and looks up the
// matching path in status.atProvider, rather than performing a symmetric recursive equality
// check that would flag every provider-defaulted field as spurious drift.
package crossplanedrift

import (
	"fmt"
	"sort"
	"strings"
)

// Class classifies a single configured leaf path.
type Class int

const (
	// InSync means the configured and observed values are equal after normalization.
	InSync Class = iota
	// Drift means the configured and observed values differ after normalization.
	Drift
	// NotObserved means the configured path has no corresponding entry in status.atProvider.
	NotObserved
)

// Entry is a single compared leaf path from spec.forProvider.
type Entry struct {
	Path     string
	Desired  interface{}
	Observed interface{}
	Class    Class
	Redacted bool
}

// Result is the outcome of comparing spec.forProvider against status.atProvider.
type Result struct {
	// Eligible is true when both forProvider and atProvider are non-empty maps, i.e. this
	// object looks like a Crossplane managed resource with at least one observation.
	Eligible bool
	// TotalConfigured is the number of leaf paths found under spec.forProvider.
	TotalConfigured  int
	InSyncCount      int
	NotObservedCount int
	// DriftEntries holds only the entries classified as Drift, sorted by path, redacted, and
	// bounded to maxDriftEntries (with Truncated/MoreCount reporting what was cut).
	DriftEntries []Entry
	Truncated    bool
	MoreCount    int
	// ObservedOnlyCount is the number of leaf paths present under status.atProvider that have
	// no corresponding leaf path under spec.forProvider (provider-defaulted/computed fields).
	ObservedOnlyCount int
}

// maxDriftEntries bounds rendered drift entries so an object with a pathologically large
// forProvider payload can't dump an unbounded amount of output.
const maxDriftEntries = 20

// sensitivePathComponents marks path components that must have their values redacted before
// display. Matching is case-insensitive substring containment against each dot-separated path
// component, e.g. "dbPassword" or "spec.forProvider.auth.password" both match "password".
var sensitivePathComponents = []string{"password", "secret", "token", "privatekey", "accesskey", "credential"}

// Diff compares spec.forProvider against status.atProvider and returns the classified leaf
// paths. forProvider/atProvider are expected to be the raw unstructured map values (as decoded
// by k8s.io/apimachinery/pkg/apis/meta/v1/unstructured), or nil if absent.
func Diff(forProvider, atProvider map[string]interface{}) Result {
	if len(forProvider) == 0 || len(atProvider) == 0 {
		return Result{Eligible: false}
	}

	desiredLeaves := walkLeaves("", forProvider)
	observedLeaves := walkLeaves("", atProvider)
	observedByPath := make(map[string]interface{}, len(observedLeaves))
	for _, l := range observedLeaves {
		observedByPath[l.path] = l.value
	}
	desiredPaths := make(map[string]struct{}, len(desiredLeaves))
	for _, l := range desiredLeaves {
		desiredPaths[l.path] = struct{}{}
	}

	result := Result{Eligible: true, TotalConfigured: len(desiredLeaves)}

	var driftEntries []Entry
	for _, l := range desiredLeaves {
		observed, found := observedByPath[l.path]
		switch {
		case !found:
			result.NotObservedCount++
		case valuesEqual(l.value, observed):
			result.InSyncCount++
		default:
			driftEntries = append(driftEntries, redactEntry(Entry{
				Path:     l.path,
				Desired:  l.value,
				Observed: observed,
				Class:    Drift,
			}))
		}
	}
	sort.Slice(driftEntries, func(i, j int) bool { return driftEntries[i].Path < driftEntries[j].Path })

	if len(driftEntries) > maxDriftEntries {
		result.MoreCount = len(driftEntries) - maxDriftEntries
		result.Truncated = true
		driftEntries = driftEntries[:maxDriftEntries]
	}
	result.DriftEntries = driftEntries

	for path := range observedByPath {
		if _, ok := desiredPaths[path]; !ok {
			result.ObservedOnlyCount++
		}
	}

	return result
}

func redactEntry(e Entry) Entry {
	if !pathIsSensitive(e.Path) {
		return e
	}
	e.Desired = "REDACTED"
	e.Observed = "REDACTED"
	e.Redacted = true
	return e
}

func pathIsSensitive(path string) bool {
	for _, component := range strings.Split(path, ".") {
		lower := strings.ToLower(component)
		for _, marker := range sensitivePathComponents {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

type leaf struct {
	path  string
	value interface{}
}

// walkLeaves recurses through non-empty nested maps, treating any non-map value (including
// lists, scalars, and empty maps) as a leaf. Lists are intentionally not recursed into: list
// equality is handled as a single comparison by valuesEqual, since providers commonly use
// different orderings/schemas for lists of objects that generic key-based matching can't
// reliably reconcile.
func walkLeaves(prefix string, v interface{}) []leaf {
	m, ok := v.(map[string]interface{})
	if !ok || len(m) == 0 {
		return []leaf{{path: prefix, value: v}}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var leaves []leaf
	for _, k := range keys {
		childPath := k
		if prefix != "" {
			childPath = prefix + "." + k
		}
		leaves = append(leaves, walkLeaves(childPath, m[k])...)
	}
	return leaves
}

// valuesEqual implements the normalized comparison: numeric JSON representations are equal
// regardless of int/float encoding, maps are compared unordered, and lists follow the
// conservative rules documented on the package: scalar lists without duplicates compare as
// sets, everything else (lists containing duplicates, or containing maps/lists) compares by
// position. Type differences that are semantically meaningful, e.g. the string "1" versus the
// number 1, are preserved as inequality.
func valuesEqual(a, b interface{}) bool {
	af, aIsNum := asFloat64(a)
	bf, bIsNum := asFloat64(b)
	if aIsNum || bIsNum {
		return aIsNum && bIsNum && af == bf
	}

	am, aIsMap := a.(map[string]interface{})
	bm, bIsMap := b.(map[string]interface{})
	if aIsMap || bIsMap {
		if !aIsMap || !bIsMap || len(am) != len(bm) {
			return false
		}
		for k, av := range am {
			bv, ok := bm[k]
			if !ok || !valuesEqual(av, bv) {
				return false
			}
		}
		return true
	}

	as, aIsSlice := a.([]interface{})
	bs, bIsSlice := b.([]interface{})
	if aIsSlice || bIsSlice {
		if !aIsSlice || !bIsSlice {
			return false
		}
		return listsEqual(as, bs)
	}

	return a == b
}

func listsEqual(a, b []interface{}) bool {
	if isScalarListNoDuplicates(a) && isScalarListNoDuplicates(b) {
		return scalarSetsEqual(a, b)
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !valuesEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func isScalarListNoDuplicates(list []interface{}) bool {
	seen := make(map[string]struct{}, len(list))
	for _, v := range list {
		switch v.(type) {
		case map[string]interface{}, []interface{}:
			return false
		}
		key := scalarSetKey(v)
		if _, ok := seen[key]; ok {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

// scalarSetKey builds a dedup/membership key that treats numeric values the same way valuesEqual
// does (1 and 1.0 key the same regardless of int/float encoding), while keeping non-numeric types
// distinguished by their Go type so e.g. the string "1" and the number 1 get different keys.
func scalarSetKey(v interface{}) string {
	if f, ok := asFloat64(v); ok {
		return fmt.Sprintf("number:%v", f)
	}
	return fmt.Sprintf("%T:%v", v, v)
}

func scalarSetsEqual(a, b []interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	bSet := make(map[string]struct{}, len(b))
	for _, v := range b {
		bSet[scalarSetKey(v)] = struct{}{}
	}
	for _, v := range a {
		if _, ok := bSet[scalarSetKey(v)]; !ok {
			return false
		}
	}
	return true
}

// Label picks the operator-facing wording for a non-empty drift result: an Observe-only managed
// resource reads as an expected "Observed difference" (Crossplane isn't reconciling it), a
// Synced=False resource reads as the stronger "Drift" signal (a reconcile failure, not just a
// provider-side observation lag), and anything else (typically Synced=True) reads as the
// unsurprising "Observed difference" case where the provider may still reconcile it. annotation
// is an optional parenthetical with more detail, empty when the plain label needs no elaboration.
func Label(syncedStatus string, managementPolicies []interface{}) (label string, annotation string) {
	if isObserveOnly(managementPolicies) {
		return "Observed difference", "Observe-only; Crossplane will not correct them"
	}
	if syncedStatus == "False" {
		return "Drift", "reconcile failure; inspect Synced condition"
	}
	return "Observed difference", ""
}

func isObserveOnly(managementPolicies []interface{}) bool {
	if len(managementPolicies) != 1 {
		return false
	}
	policy, _ := managementPolicies[0].(string)
	return policy == "Observe"
}

func asFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
