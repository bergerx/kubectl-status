package plugin

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	"github.com/bergerx/kubectl-status/pkg/plugin/calicoselector"
)

// networkPolicySelectsPod reports whether a NetworkPolicy's spec.podSelector matches podLabels.
// podSelector is a full metav1.LabelSelector (matchLabels + matchExpressions), and an empty
// selector ({} -- no matchLabels, no matchExpressions) matches every Pod in the policy's
// namespace, per https://kubernetes.io/docs/concepts/services-networking/network-policies/ --
// metav1.LabelSelectorAsSelector already returns labels.Everything() for that case, so this uses
// real selector semantics rather than the isSubset helper (which is for a different direction of
// matching, see KubeGetServicesMatchingPod).
func networkPolicySelectsPod(policySpec map[string]interface{}, podLabels map[string]string) bool {
	selMap, _ := policySpec["podSelector"].(map[string]interface{})
	ls := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selMap, ls); err != nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(podLabels))
}

// gatekeeperConstraintMatchesNamespace reports whether a Gatekeeper Constraint's spec.match would
// admit objects created in the given namespace -- i.e. whether the namespace itself is in scope,
// not whether any particular object in it would be. It only evaluates the namespace-scoping
// fields (scope/namespaces/excludedNamespaces/namespaceSelector); spec.match.kinds/labelSelector/
// name constrain which objects within an admitted namespace the constraint actually governs, and
// the constraint's rego body decides pass/fail -- neither is answerable without a specific
// candidate object, so this stays at "may govern this namespace" rather than "will deny this
// object". scope: Cluster is excluded outright: such a constraint only ever evaluates
// cluster-scoped objects (Nodes, PersistentVolumes, ...), never anything created inside a
// namespace. An absent namespaces/excludedNamespaces/namespaceSelector each impose no restriction
// of their own, matching Gatekeeper's actual match semantics (see
// gatekeeper_constraint_match_and_enforcement.tmpl, which renders the same fields from the
// opposite direction -- a Constraint's own perspective on what it matches).
func gatekeeperConstraintMatchesNamespace(matchSpec map[string]interface{}, namespaceName string, namespaceLabels map[string]string) bool {
	if scope, _ := matchSpec["scope"].(string); scope == "Cluster" {
		return false
	}
	if namespaces, ok := matchSpec["namespaces"].([]interface{}); ok && len(namespaces) > 0 {
		found := false
		for _, n := range namespaces {
			if s, _ := n.(string); s == namespaceName {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if excluded, ok := matchSpec["excludedNamespaces"].([]interface{}); ok {
		for _, n := range excluded {
			if s, _ := n.(string); s == namespaceName {
				return false
			}
		}
	}
	if selMap, ok := matchSpec["namespaceSelector"].(map[string]interface{}); ok {
		ls := &metav1.LabelSelector{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selMap, ls); err != nil {
			return false
		}
		sel, err := metav1.LabelSelectorAsSelector(ls)
		if err != nil {
			return false
		}
		if !sel.Matches(labels.Set(namespaceLabels)) {
			return false
		}
	}
	return true
}

// networkPolicyPolicyTypes normalizes NetworkPolicy spec.policyTypes, applying the documented
// default used when the field is omitted: Ingress always applies, and Egress applies only when
// the policy also defines an egress rule set. See
// https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.32/#networkpolicyspec-v1-networking-k8s-io
func networkPolicyPolicyTypes(spec map[string]interface{}) []string {
	return policyTypesWithDefault(spec, "policyTypes")
}

// calicoPolicyTypes normalizes a Calico NetworkPolicy/GlobalNetworkPolicy's spec.types, applying
// the same defaulting rule documented for upstream NetworkPolicy (Ingress always applies, Egress
// only when the policy also defines egress rules) -- Calico's spec.types field mirrors
// spec.policyTypes here, just under a different name. See
// https://docs.tigera.io/calico/latest/reference/resources/networkpolicy.
func calicoPolicyTypes(spec map[string]interface{}) []string {
	return policyTypesWithDefault(spec, "types")
}

// policyTypesWithDefault is shared by networkPolicyPolicyTypes and calicoPolicyTypes -- both
// upstream NetworkPolicy and Calico's NetworkPolicy/GlobalNetworkPolicy apply the identical
// default (Ingress implied; Egress only if egress rules are present) under a differently-named
// spec field.
func policyTypesWithDefault(spec map[string]interface{}, typesKey string) []string {
	if rawTypes, ok := spec[typesKey].([]interface{}); ok && len(rawTypes) > 0 {
		types := make([]string, 0, len(rawTypes))
		for _, t := range rawTypes {
			if s, ok := t.(string); ok {
				types = append(types, s)
			}
		}
		return types
	}
	types := []string{"Ingress"}
	if _, hasEgress := spec["egress"]; hasEgress {
		types = append(types, "Egress")
	}
	return types
}

// calicoPolicySelectsPod reports whether a Calico NetworkPolicy/GlobalNetworkPolicy's spec.selector
// matches podLabels. Unlike Kubernetes LabelSelectors, Calico's selector is a small boolean
// expression language (see pkg/plugin/calicoselector), evaluated against Calico's own
// workload-endpoint label set -- which is the Pod's labels plus a synthetic
// "projectcalico.org/namespace" label -- not the Pod's bare labels. See
// https://docs.tigera.io/calico-cloud/network-policy/policy-tiers/tiered-policy. An empty
// selector matches everything, same as an absent podSelector for upstream NetworkPolicy.
// Unparseable selectors are conservatively treated as non-matching (logged at V(3)) rather than
// risking a false match.
func calicoPolicySelectsPod(spec map[string]interface{}, podLabels map[string]string, namespace string) bool {
	selectorStr, _ := spec["selector"].(string)
	sel, err := calicoselector.Parse(selectorStr)
	if err != nil {
		klog.V(3).ErrorS(err, "failed to parse Calico selector", "selector", selectorStr)
		return false
	}
	return sel.Evaluate(withCalicoNamespaceLabel(podLabels, namespace))
}

// calicoNamespaceSelectorMatches reports whether a Calico GlobalNetworkPolicy's
// spec.namespaceSelector accepts a namespace, given that namespace's labels. An empty
// namespaceSelector matches every namespace (GlobalNetworkPolicy is cluster-scoped, so unlike the
// namespaced NetworkPolicy case there's no implicit namespace restriction to fall back on). See
// https://docs.tigera.io/calico-cloud/network-policy/policy-tiers/tiered-policy. Calico adds a
// synthetic "projectcalico.org/name" label to namespaces for use in such selectors.
func calicoNamespaceSelectorMatches(spec map[string]interface{}, namespace string, namespaceLabels map[string]string) bool {
	selectorStr, _ := spec["namespaceSelector"].(string)
	if selectorStr == "" {
		return true
	}
	sel, err := calicoselector.Parse(selectorStr)
	if err != nil {
		klog.V(3).ErrorS(err, "failed to parse Calico namespaceSelector", "selector", selectorStr)
		return false
	}
	augmented := make(map[string]string, len(namespaceLabels)+1)
	for k, v := range namespaceLabels {
		augmented[k] = v
	}
	augmented["projectcalico.org/name"] = namespace
	return sel.Evaluate(augmented)
}

func withCalicoNamespaceLabel(podLabels map[string]string, namespace string) map[string]string {
	augmented := make(map[string]string, len(podLabels)+1)
	for k, v := range podLabels {
		augmented[k] = v
	}
	augmented["projectcalico.org/namespace"] = namespace
	return augmented
}

// ciliumRuleSpecs returns the individual Cilium Rule objects making up a CiliumNetworkPolicy or
// CiliumClusterwideNetworkPolicy -- its spec is either a single Rule (spec.endpointSelector,
// spec.ingress, ...) or, for multi-rule policies, a list of Rules under specs. See
// https://docs.cilium.io/en/stable/network/kubernetes/policy/.
func ciliumRuleSpecs(obj map[string]interface{}) (rules []map[string]interface{}) {
	if spec, ok := obj["spec"].(map[string]interface{}); ok {
		rules = append(rules, spec)
	}
	if specs, ok := obj["specs"].([]interface{}); ok {
		for _, s := range specs {
			if m, ok := s.(map[string]interface{}); ok {
				rules = append(rules, m)
			}
		}
	}
	return rules
}

// ciliumEndpointSelectorMatchesPod reports whether a Cilium Rule's endpointSelector matches
// podLabels. endpointSelector uses the same matchLabels/matchExpressions shape as a Kubernetes
// LabelSelector (https://docs.cilium.io/en/latest/security/policy/kubernetes/), and a
// missing/empty selector targets every endpoint, same as networkPolicySelectsPod's handling of an
// empty podSelector. Note: a policy authored against Cilium's own reserved label prefixes (e.g.
// "k8s:app") won't match here since podLabels are the Pod's bare labels -- acceptable for this
// compact signal, see the package doc.
func ciliumEndpointSelectorMatchesPod(rule map[string]interface{}, podLabels map[string]string) bool {
	selMap, _ := rule["endpointSelector"].(map[string]interface{})
	ls := &metav1.LabelSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selMap, ls); err != nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(podLabels))
}

// ciliumPolicySelectsPod reports whether any Rule in a CiliumNetworkPolicy/
// CiliumClusterwideNetworkPolicy object selects podLabels, and the union of restricted
// directions across the matching Rule(s). Unlike upstream NetworkPolicy, Cilium has no implied
// default direction: a Rule only restricts ingress when it carries an ingress or ingressDeny rule
// list, and only restricts egress when it carries egress or egressDeny -- a bare endpointSelector
// with no rule lists selects the endpoint but enforces nothing.
func ciliumPolicySelectsPod(obj map[string]interface{}, podLabels map[string]string) (matches bool, directions []string) {
	ingress, egress := false, false
	for _, rule := range ciliumRuleSpecs(obj) {
		if !ciliumEndpointSelectorMatchesPod(rule, podLabels) {
			continue
		}
		matches = true
		if _, ok := rule["ingress"]; ok {
			ingress = true
		}
		if _, ok := rule["ingressDeny"]; ok {
			ingress = true
		}
		if _, ok := rule["egress"]; ok {
			egress = true
		}
		if _, ok := rule["egressDeny"]; ok {
			egress = true
		}
	}
	if ingress {
		directions = append(directions, "ingress")
	}
	if egress {
		directions = append(directions, "egress")
	}
	return matches, directions
}

// ciliumPolicyDirectionsForTemplate is the template-callable wrapper for ciliumPolicySelectsPod,
// used to render the restricted directions for a CiliumNetworkPolicy/CiliumClusterwideNetworkPolicy
// already known to select the Pod (see KubeGetCiliumNetworkPoliciesMatchingPod).
func ciliumPolicyDirectionsForTemplate(obj map[string]interface{}, podLabels map[string]interface{}) []string {
	_, directions := ciliumPolicySelectsPod(obj, stringifyLabels(podLabels))
	return directions
}

func stringifyLabels(labels map[string]interface{}) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = fmt.Sprintf("%v", v)
	}
	return out
}
