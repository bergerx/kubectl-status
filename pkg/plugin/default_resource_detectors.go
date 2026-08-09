package plugin

import "strings"

// defaultResourceDetector inspects an object that has no dedicated <Kind>.tmpl -- i.e. one about
// to be rendered by the "DefaultResource" fallback template in common.tmpl -- and decides whether
// it belongs to a particular ecosystem (Crossplane, Gatekeeper, ...) that wants extra, otherwise
// generic-CRD-unaware rendering. When it recognizes the shape it returns the template name that
// should render it and ok=true; otherwise it returns ok=false and DefaultResource moves on to the
// next detector.
//
// This exists so that "if we don't recognize this CRD, but it looks like ecosystem X, render it
// specially" can keep growing without editing DefaultResource itself: each ecosystem's detection
// logic lives here as an independently unit-testable Go function (see
// default_resource_detectors_test.go) instead of as inline template control flow mixed into the
// one shared fallback every unrecognized object depends on. Adding a new ecosystem is "append a
// function to defaultResourceDetectors", not "edit DefaultResource's template body".
type defaultResourceDetector func(obj RenderableObject) (templateName string, ok bool)

// defaultResourceDetectors is the registry DefaultResourceTemplates iterates, in order. All
// matching detectors contribute -- this is not first-match-wins dispatch -- because the shapes
// aren't mutually exclusive: nothing stops a CRD from legitimately matching more than one
// ecosystem's signal (and today, Crossplane's own composite/managed-resource shapes already
// aren't mutually exclusive with each other, which is why detectCrossplaneResource below is a
// single coarse check feeding one shared rendering template rather than one detector per shape).
var defaultResourceDetectors = []defaultResourceDetector{
	detectCrossplaneResource,
	detectGatekeeperConstraint,
}

// DefaultResourceTemplates returns, in registration order, the name of every template that
// DefaultResource (common.tmpl) should render for r in addition to the generic per-object
// skeleton, as decided by defaultResourceDetectors. Called from DefaultResource as
// `{{- range .DefaultResourceTemplates }}{{ template . $ }}{{- end }}`.
func (r RenderableObject) DefaultResourceTemplates() []string {
	var names []string
	for _, detect := range defaultResourceDetectors {
		if name, ok := detect(r); ok {
			names = append(names, name)
		}
	}
	return names
}

// detectCrossplaneResource recognizes the handful of spec shapes that only a Crossplane object
// (composite resource, claim, or managed resource) populates -- see the doc comments on
// crossplane_composition_ref/crossplane_composed_resources/crossplane_managed_resource_drift in
// common.tmpl for why each individual field is a safe, unambiguous signal on its own. The three
// signals aren't mutually exclusive (an XR can have a compositionRef and composed resourceRefs at
// the same time; a managed resource typically has neither but has forProvider instead), so this
// is deliberately a single "does any Crossplane signal fire" check feeding one rendering template
// -- crossplane_default_resource -- whose own sub-templates each re-check their own narrower
// condition and no-op when it doesn't apply, exactly as they did when called unconditionally.
func detectCrossplaneResource(obj RenderableObject) (string, bool) {
	spec := obj.Spec()
	if spec == nil {
		return "", false
	}
	crossplane, _ := spec["crossplane"].(map[string]interface{})

	compositionRef := spec["compositionRef"]
	revisionRef := spec["compositionRevisionRef"]
	updatePolicy := spec["compositionUpdatePolicy"]
	if crossplane != nil {
		if v, ok := crossplane["compositionRef"]; ok {
			compositionRef = v
		}
		if v, ok := crossplane["compositionRevisionRef"]; ok {
			revisionRef = v
		}
		if v, ok := crossplane["compositionUpdatePolicy"]; ok {
			updatePolicy = v
		}
	}
	if hasNamedRef(compositionRef) || hasNamedRef(revisionRef) || isTruthy(updatePolicy) {
		return "crossplane_default_resource", true
	}

	resourceRefs := spec["resourceRefs"]
	if crossplane != nil {
		if v, ok := crossplane["resourceRefs"]; ok {
			resourceRefs = v
		}
	}
	if hasValidResourceRef(resourceRefs) {
		return "crossplane_default_resource", true
	}

	if forProvider, ok := spec["forProvider"].(map[string]interface{}); ok && len(forProvider) > 0 {
		return "crossplane_default_resource", true
	}

	return "", false
}

// hasNamedRef mirrors `and (kindIs "map" $ref) (kindIs "string" $ref.name)`: a map with a "name"
// key whose value is a string (an empty string still counts -- this only checks shape, not that
// the reference is non-empty, matching the template condition it replaces).
func hasNamedRef(ref interface{}) bool {
	m, ok := ref.(map[string]interface{})
	if !ok {
		return false
	}
	_, ok = m["name"].(string)
	return ok
}

// hasValidResourceRef mirrors the crossplane_composed_resources loop's per-entry validity check:
// a slice containing at least one map entry whose "kind" and "name" are both strings.
func hasValidResourceRef(refs interface{}) bool {
	slice, ok := refs.([]interface{})
	if !ok {
		return false
	}
	for _, item := range slice {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if _, ok := m["kind"].(string); !ok {
			continue
		}
		if _, ok := m["name"].(string); ok {
			return true
		}
	}
	return false
}

// isTruthy approximates text/template's truthiness for the handful of scalar shapes
// compositionUpdatePolicy can realistically hold (a non-empty string in practice; nil when
// absent). Good enough for this one field without importing text/template's unexported isTrue.
func isTruthy(v interface{}) bool {
	if v == nil {
		return false
	}
	if s, ok := v.(string); ok {
		return s != ""
	}
	return true
}

// detectGatekeeperConstraint recognizes a dynamically-generated Gatekeeper Constraint object.
// Gatekeeper generates one CRD Kind per installed ConstraintTemplate (K8sAllowedRepos, any custom
// one, ...), so findTemplateName can only ever route the handful with a dedicated <Kind>.tmpl
// (K8sRequiredLabels today); every other Constraint Kind reaches DefaultResource instead. Every
// Constraint CRD Gatekeeper generates shares one fixed apiVersion group regardless of Kind
// (confirmed against a live-cluster-generated CRD), so this is an exact eligibility check, not a
// spec.match/spec.enforcementAction shape heuristic that could coincidentally match an unrelated
// CRD reusing those field names.
func detectGatekeeperConstraint(obj RenderableObject) (string, bool) {
	if strings.HasPrefix(obj.APIVersion(), "constraints.gatekeeper.sh/") {
		return "gatekeeper_constraint_fallback", true
	}
	return "", false
}
