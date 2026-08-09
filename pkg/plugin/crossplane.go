package plugin

import (
	"github.com/bergerx/kubectl-status/pkg/plugin/crossplanedrift"
)

// crossplaneManagedResourceDrift compares a Crossplane managed resource's desired
// spec.forProvider configuration against its observed status.atProvider, see
// pkg/plugin/crossplanedrift for the comparison semantics. forProvider/atProvider are expected
// to be untyped map values as decoded off the unstructured object (nil/non-map is treated as
// absent).
func crossplaneManagedResourceDrift(forProvider, atProvider interface{}) crossplanedrift.Result {
	fp, _ := forProvider.(map[string]interface{})
	ap, _ := atProvider.(map[string]interface{})
	return crossplanedrift.Diff(fp, ap)
}

// crossplaneDriftLabel picks the operator-facing label/annotation for a non-empty drift result;
// see crossplanedrift.Label. syncedStatus is the managed resource's Synced condition ".status"
// value ("True"/"False"/"" if the condition is absent); managementPolicies is the untyped
// spec.managementPolicies list value (nil if absent, meaning full-control/default). Returns a
// dict with "Label" and "Annotation" keys for easy template consumption.
func crossplaneDriftLabel(syncedStatus string, managementPolicies interface{}) map[string]string {
	policies, _ := managementPolicies.([]interface{})
	label, annotation := crossplanedrift.Label(syncedStatus, policies)
	return map[string]string{"Label": label, "Annotation": annotation}
}
