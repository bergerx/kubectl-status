package plugin

import (
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func labelSelector(s map[string]interface{}) string {
	ls := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(s, ls); err != nil {
		return fmt.Sprintf("%v", s)
	}
	return metav1.FormatLabelSelector(ls)
}

// tolerationMatchesTaint reports whether a single toleration covers a single taint, following
// https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/#concepts
func tolerationMatchesTaint(toleration, taint map[string]interface{}) bool {
	if effect, _ := toleration["effect"].(string); effect != "" {
		taintEffect, _ := taint["effect"].(string)
		if effect != taintEffect {
			return false
		}
	}
	key, _ := toleration["key"].(string)
	operator, _ := toleration["operator"].(string)
	if operator == "" {
		operator = "Equal"
	}
	taintKey, _ := taint["key"].(string)
	switch operator {
	case "Exists":
		return key == "" || key == taintKey
	case "Equal":
		if key != taintKey {
			return false
		}
		value, _ := toleration["value"].(string)
		taintValue, _ := taint["value"].(string)
		return value == taintValue
	default:
		return false
	}
}

// taintsNotToleratedByPod returns the subset of nodeTaints that block scheduling or trigger
// eviction (NoSchedule/NoExecute) and aren't covered by any of the pod's tolerations.
// PreferNoSchedule is a soft preference, not a blocker, and is intentionally excluded.
func taintsNotToleratedByPod(nodeTaints, tolerations []interface{}) (result []interface{}) {
	for _, t := range nodeTaints {
		taint, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		effect, _ := taint["effect"].(string)
		if effect != "NoSchedule" && effect != "NoExecute" {
			continue
		}
		tolerated := false
		for _, tol := range tolerations {
			toleration, ok := tol.(map[string]interface{})
			if !ok {
				continue
			}
			if tolerationMatchesTaint(toleration, taint) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			result = append(result, taint)
		}
	}
	return result
}

// cloudProviderNames maps the scheme of a Node's .spec.providerID -- the
// "<provider>://<provider-specific-id>" prefix naming the infrastructure that backs the node -- to
// a display name. providerID is the only field in the Node object meant to identify the provider,
// but it is optional and its format is provider-defined (see
// https://github.com/kubernetes/kubernetes/issues/124348), so an unrecognized scheme is reported
// verbatim rather than dropped: telling the reader the node says "ibmcloud" beats saying nothing.
var cloudProviderNames = map[string]string{
	"alicloud":     "Alibaba Cloud",
	"aws":          "AWS",
	"azure":        "Azure",
	"digitalocean": "DigitalOcean",
	"equinixmetal": "Equinix Metal",
	"gce":          "GCP",
	"hcloud":       "Hetzner",
	"ibm":          "IBM Cloud",
	"kind":         "kind",
	"kwok":         "kwok",
	"metal3":       "Metal3",
	"oci":          "OCI",
	"openstack":    "OpenStack",
	"vsphere":      "vSphere",
}

// cloudProviderLabelPrefixes maps label namespaces only one provider's components ever set to that
// provider. Weaker evidence than providerID -- a node can carry these while the cloud provider
// integration itself is absent or half-configured -- so nodeCloudProvider qualifies a match found
// this way. A slice rather than a map so a node carrying two of these namespaces always reports
// the same provider instead of whichever one map iteration happened to reach first.
var cloudProviderLabelPrefixes = []struct{ prefix, name string }{
	{"eks.amazonaws.com/", "AWS"},
	{"karpenter.k8s.aws/", "AWS"},
	{"cloud.google.com/", "GCP"},
	{"topology.gke.io/", "GCP"},
	{"kubernetes.azure.com/", "Azure"},
	{"doks.digitalocean.com/", "DigitalOcean"},
	{"oci.oraclecloud.com/", "OCI"},
	{"ibm-cloud.kubernetes.io/", "IBM Cloud"},
}

// nodeCloudProvider names the infrastructure backing a Node, preferring .spec.providerID (set by
// the cloud provider itself) over provider-specific label namespaces (circumstantial, so the
// returned name says where it came from). Returns "" when neither offers evidence, which the Node
// template renders as no provider at all: node names, addresses and topology values all look like
// they identify a provider and are all installation-specific, and to a reader mid-incident a
// confidently wrong provider is worse than none.
func nodeCloudProvider(providerID string, labels map[string]interface{}) string {
	if scheme, _, found := strings.Cut(providerID, "://"); found && scheme != "" {
		if name, ok := cloudProviderNames[strings.ToLower(scheme)]; ok {
			return name
		}
		return scheme
	}
	for _, provider := range cloudProviderLabelPrefixes {
		for key := range labels {
			if strings.HasPrefix(key, provider.prefix) {
				return provider.name + " (from labels)"
			}
		}
	}
	return ""
}

// formatNodeSelector renders spec.nodeSelector, a flat key/value map, as "k=v,k2=v2" -- the same
// comma-joined "k=v" convention used elsewhere for rendered label selectors, keys sorted for
// stable output.
func formatNodeSelector(nodeSelector map[string]interface{}) string {
	keys := make([]string, 0, len(nodeSelector))
	for k := range nodeSelector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, _ := nodeSelector[k].(string)
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// formatNodeSelectorRequirement renders a single matchExpressions/matchFields entry from
// spec.affinity.nodeAffinity, mirroring the compact style of
// k8s.io/apimachinery/pkg/labels.Requirement.String() ("key in (a,b)", "!key", "key>5") but
// extended with Gt/Lt, which LabelSelectorRequirement doesn't support -- this is deliberately not
// routed through the labelSelector pipe function, since NodeSelectorRequirement isn't
// metav1.LabelSelector-shaped and silently dropping Gt/Lt would misrepresent the constraint.
func formatNodeSelectorRequirement(expr map[string]interface{}) string {
	key, _ := expr["key"].(string)
	operator, _ := expr["operator"].(string)
	values := toStringSlice(expr["values"])
	joined := strings.Join(values, ",")
	switch operator {
	case "Exists":
		return key
	case "DoesNotExist":
		return "!" + key
	case "In":
		return fmt.Sprintf("%s in (%s)", key, joined)
	case "NotIn":
		return fmt.Sprintf("%s notin (%s)", key, joined)
	case "Gt":
		return fmt.Sprintf("%s>%s", key, joined)
	case "Lt":
		return fmt.Sprintf("%s<%s", key, joined)
	default:
		return fmt.Sprintf("%s %s (%s)", key, operator, joined)
	}
}

// formatNodeSelectorTerm renders one nodeSelectorTerm as the AND of its matchExpressions and
// matchFields requirements.
func formatNodeSelectorTerm(term map[string]interface{}) string {
	var parts []string
	for _, e := range toInterfaceMapSlice(term["matchExpressions"]) {
		parts = append(parts, formatNodeSelectorRequirement(e))
	}
	for _, e := range toInterfaceMapSlice(term["matchFields"]) {
		parts = append(parts, formatNodeSelectorRequirement(e))
	}
	return strings.Join(parts, ",")
}

// formatNodeSelectorTerms renders
// spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms as
// the OR of its terms, each an AND of its requirements. Multiple terms are parenthesized so the
// OR/AND nesting stays unambiguous.
func formatNodeSelectorTerms(terms []interface{}) string {
	rendered := make([]string, 0, len(terms))
	for _, t := range terms {
		term, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if r := formatNodeSelectorTerm(term); r != "" {
			rendered = append(rendered, r)
		}
	}
	if len(rendered) > 1 {
		for i, r := range rendered {
			rendered[i] = fmt.Sprintf("(%s)", r)
		}
	}
	return strings.Join(rendered, " or ")
}

// nodeSelectorRequirementMatches reports whether values (nodeLabels or nodeFields) satisfies a
// single matchExpressions/matchFields requirement, mirroring
// k8s.io/apimachinery/pkg/labels.Requirement.Matches: NotIn and DoesNotExist are satisfied when
// the key is absent; In, Exists, Gt and Lt all require the key to be present.
func nodeSelectorRequirementMatches(expr map[string]interface{}, values map[string]string) bool {
	key, _ := expr["key"].(string)
	operator, _ := expr["operator"].(string)
	val, exists := values[key]
	reqValues := toStringSlice(expr["values"])
	switch operator {
	case "In":
		return exists && stringSliceContains(reqValues, val)
	case "NotIn":
		return !exists || !stringSliceContains(reqValues, val)
	case "Exists":
		return exists
	case "DoesNotExist":
		return !exists
	case "Gt":
		return exists && len(reqValues) == 1 && numericLess(reqValues[0], val)
	case "Lt":
		return exists && len(reqValues) == 1 && numericLess(val, reqValues[0])
	default:
		return false
	}
}

// nodeSelectorTermMatches reports whether nodeLabels/nodeFields satisfy a single nodeSelectorTerm:
// AND across its matchExpressions (checked against nodeLabels) and matchFields (checked against
// nodeFields).
func nodeSelectorTermMatches(term map[string]interface{}, nodeLabels, nodeFields map[string]string) bool {
	for _, e := range toInterfaceMapSlice(term["matchExpressions"]) {
		if !nodeSelectorRequirementMatches(e, nodeLabels) {
			return false
		}
	}
	for _, e := range toInterfaceMapSlice(term["matchFields"]) {
		if !nodeSelectorRequirementMatches(e, nodeFields) {
			return false
		}
	}
	return true
}

// nodeSelectorTermsMatch reports whether nodeLabels/nodeFields satisfy at least one of terms (OR
// across terms), implementing required-during-scheduling node affinity semantics. An empty terms
// list is unconstrained and matches everything -- mirroring
// k8s.io/component-helpers/scheduling/corev1/nodeaffinity's treatment of an absent
// nodeAffinity/nodeSelector as "no constraint" rather than "matches nothing".
func nodeSelectorTermsMatch(terms []interface{}, nodeLabels, nodeFields map[string]string) bool {
	if len(terms) == 0 {
		return true
	}
	for _, t := range terms {
		term, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if nodeSelectorTermMatches(term, nodeLabels, nodeFields) {
			return true
		}
	}
	return false
}

// podHardConstraintRequirements flattens a Pod's hard node-selection constraints (nodeSelector and,
// only when unambiguous, required nodeAffinity) into a flat list of key-level requirements a
// Karpenter NodePool must be able to provision against. nodeSelector entries become implicit
// "In [value]" requirements. Required nodeAffinity's nodeSelectorTerms are OR'd together by
// Kubernetes semantics, so a key isn't really "hard-required" unless every term constrains it the
// same way; rather than guess which terms are safe to treat as universal, only the single-term case
// is flattened, and multiple OR'd terms are skipped entirely. matchFields entries (e.g.
// metadata.name) are always excluded: Karpenter NodePool requirements are label-only and can never
// express a field selector, so treating one as a hard-constraint key here would falsely claim no
// NodePool can ever satisfy it.
func podHardConstraintRequirements(nodeSelector map[string]interface{}, terms []interface{}) []interface{} {
	var result []interface{}
	keys := make([]string, 0, len(nodeSelector))
	for k := range nodeSelector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, _ := nodeSelector[k].(string)
		result = append(result, map[string]interface{}{
			"key":      k,
			"operator": "In",
			"values":   []interface{}{v},
		})
	}
	if len(terms) == 1 {
		if term, ok := terms[0].(map[string]interface{}); ok {
			for _, e := range toInterfaceMapSlice(term["matchExpressions"]) {
				result = append(result, e)
			}
		}
	}
	return result
}
