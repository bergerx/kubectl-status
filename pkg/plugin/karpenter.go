package plugin

import (
	"sort"
)

// nodePoolCanPossiblySatisfy reports whether a NodePool's flat, AND'd
// spec.template.spec.requirements could possibly allow provisioning a node that satisfies a single
// Pod hard requirement (one nodeSelector entry or one required-nodeAffinity matchExpression).
// Returns false only when that can be determined with certainty: no requirement on the key at all,
// or a definite In/NotIn value-set conflict. Every other combination (Exists/DoesNotExist/Gt/Lt on
// either side, or an operator that doesn't reduce to simple set intersection) returns true -- a
// false "cannot satisfy" is the one output this package must never produce, so anything it can't
// evaluate must look identical to "not sure, assume it could work".
func nodePoolCanPossiblySatisfy(nodePoolRequirements []interface{}, podRequirement map[string]interface{}) bool {
	key, _ := podRequirement["key"].(string)
	podOperator, _ := podRequirement["operator"].(string)
	podValues := toStringSlice(podRequirement["values"])

	var poolReq map[string]interface{}
	found := false
	for _, r := range toInterfaceMapSlice(nodePoolRequirements) {
		if rk, _ := r["key"].(string); rk == key {
			poolReq = r
			found = true
			break
		}
	}
	if !found {
		return false
	}
	poolOperator, _ := poolReq["operator"].(string)
	poolValues := toStringSlice(poolReq["values"])

	switch {
	case podOperator == "In" && poolOperator == "In":
		return stringSlicesIntersect(podValues, poolValues)
	case podOperator == "In" && poolOperator == "NotIn":
		return !stringSliceSubset(podValues, poolValues)
	case podOperator == "NotIn" && poolOperator == "In":
		return !stringSliceSubset(poolValues, podValues)
	case podOperator == "NotIn" && poolOperator == "NotIn":
		return true
	default:
		return true
	}
}

// karpenterUnsatisfiableKeys returns the subset of podRequirements' keys for which every NodePool in
// nodePools -- each a dict of "name" and "requirements" ([]interface{} of NodeSelectorRequirement
// maps, i.e. NodePool.Spec.template.spec.requirements) -- fails nodePoolCanPossiblySatisfy. A
// non-empty result means no NodePool can satisfy the whole Pod (each returned key alone rules every
// pool out); an empty result is not a claim that any single pool satisfies every key simultaneously,
// only that no key on its own rules every pool out. Keys are returned sorted for stable output.
func karpenterUnsatisfiableKeys(podRequirements []interface{}, nodePools []interface{}) []string {
	var result []string
	for _, pr := range toInterfaceMapSlice(podRequirements) {
		key, _ := pr["key"].(string)
		anyPoolQualifies := false
		for _, np := range toInterfaceMapSlice(nodePools) {
			requirements, _ := np["requirements"].([]interface{})
			if nodePoolCanPossiblySatisfy(requirements, pr) {
				anyPoolQualifies = true
				break
			}
		}
		if !anyPoolQualifies {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

// karpenterDisqualifyingKey returns the first of podRequirements' keys that a single NodePool's
// requirements fail to satisfy (per nodePoolCanPossiblySatisfy), or "" if none do. Used only for
// --deep per-NodePool detail alongside karpenterUnsatisfiableKeys; a single string return (rather
// than (string, bool)) since Go templates can't destructure a multi-value return.
func karpenterDisqualifyingKey(nodePoolRequirements []interface{}, podRequirements []interface{}) string {
	for _, pr := range toInterfaceMapSlice(podRequirements) {
		if !nodePoolCanPossiblySatisfy(nodePoolRequirements, pr) {
			key, _ := pr["key"].(string)
			return key
		}
	}
	return ""
}
