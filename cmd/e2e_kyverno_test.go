package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

// runKyvernoSubtests is one of TestE2EParallel's topical groups (cmd/main_test.go). It qualifies
// on both of that function's criteria: it owns a namespace no other subtest touches
// (e2e-kyverno-report), and the four ClusterPolicies it applies (tests/e2e-artifacts/
// kyverno-policies.yaml) are all scoped by match.any[].resources.namespaces/names to that one
// namespace/name -- so, unlike a typical cluster-scoped fixture, they don't evaluate (or
// therefore race) any object another subtest creates.
//
// PolicyReport.tmpl/ClusterPolicyReport.tmpl render .scope/.results/.summary, all of which only
// Kyverno's admission-controller and reports-controller write as they actually evaluate a real
// ClusterPolicy against a real resource -- a hand-patched report would only confirm our own
// reading of the wgpolicyk8s.io shape, never Kyverno's actual behaviour (result values, the
// scored:"false"->"warn" rewrite, category/severity sourced from the *policy's* annotations
// rather than the rule's), which is the whole point of covering this live. Same reasoning as
// runFluxSubtests.
func runKyvernoSubtests(t *testing.T, hackOpts []func(*plugin.RenderConfig), clientset *kubernetes.Clientset) {
	t.Run("Kyverno PolicyReport/ClusterPolicyReport render real pass/fail/warn results from a real ClusterPolicy evaluation", func(t *testing.T) {
		t.Parallel()
		// Installed inside the subtest rather than at the group's top level for the same reason
		// ensureFlux is: it's a real controller install (four Deployments, cold image pulls) that
		// shouldn't run on every TestE2EParallel invocation regardless of -run pattern. installMu
		// (cmd/e2e_helpers_test.go) makes this safe from inside a t.Parallel() subtest.
		ensureKyverno(t)
		opts := combineOpts(hackOpts)

		const ns = "e2e-kyverno-report"
		_, err := clientset.CoreV1().Namespaces().Create(context.TODO(),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{})
		t.Cleanup(func() { deleteNamespaceAndWait(t, clientset, ns) })
		require.NoError(t, err)

		// Cluster-scoped, so applied without a namespace override -- applyManifest's own
		// t.Cleanup deletes all four ClusterPolicies afterward.
		applyManifest(t, "e2e-artifacts/kyverno-policies.yaml")
		waitForClusterScoped(t, "clusterpolicy/e2e-kyverno-check-image-tag", "condition=Ready")
		waitForClusterScoped(t, "clusterpolicy/e2e-kyverno-require-team-label", "condition=Ready")
		waitForClusterScoped(t, "clusterpolicy/e2e-kyverno-recommend-resource-limits", "condition=Ready")
		waitForClusterScoped(t, "clusterpolicy/e2e-kyverno-require-namespace-team-label", "condition=Ready")

		// The Namespace object above is itself a resource e2e-kyverno-require-namespace-team-label
		// matches by name -- but it was created before the policy exists (and before Kyverno's
		// webhook picks the policy up), so its own admission review predates the rule. Kyverno has
		// no "revalidate on policy creation" hook, only the periodic background scan (30s here,
		// see ensureKyverno), so the ClusterPolicyReport for it is a background-scan product, not
		// an admission-review one.
		applyManifestInNamespace(t, "e2e-artifacts/kyverno-pod.yaml", ns)

		reportName := waitForPolicyReportSummary(t, "policyreport", ns, func(s reportSummary) bool {
			return s.Pass+s.Fail+s.Warn+s.Error+s.Skip >= 3
		})
		clusterReportName := waitForPolicyReportSummary(t, "clusterpolicyreport", "", func(s reportSummary) bool {
			return s.Fail >= 1
		})

		cmdTest{
			args:            []string{"policyreport", reportName, "-n", ns, "--include-events=false", "--include-managed-fields=false"},
			stdoutRegexPath: "e2e-artifacts/kyverno-policyreport.regex",
		}.assert(t, nil, opts...)
		cmdTest{
			args:            []string{"clusterpolicyreport", clusterReportName, "--include-events=false", "--include-managed-fields=false"},
			stdoutRegexPath: "e2e-artifacts/kyverno-clusterpolicyreport.regex",
		}.assert(t, nil, opts...)
	})
}

// waitForClusterScoped is waitForInNamespace for a cluster-scoped resource, which `kubectl wait`
// takes no `-n` for.
func waitForClusterScoped(t *testing.T, resource, forParam string) {
	t.Helper()
	cmd := exec.Command("kubectl", "wait", "--for", forParam, resource, "--timeout=2m")
	output, err := cmd.CombinedOutput()
	t.Logf("wait result for %s: %s", resource, string(output))
	require.NoError(t, err)
}

// reportSummary mirrors PolicyReportSummary/ClusterPolicyReportSummary (wgpolicyk8s.io/v1alpha2)
// closely enough for the counts runKyvernoSubtests waits on -- decoded straight off `kubectl get
// -o json` rather than pulling in Kyverno's own API types as a test dependency for five ints.
type reportSummary struct {
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Warn  int `json:"warn"`
	Error int `json:"error"`
	Skip  int `json:"skip"`
}

type reportItem struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Summary reportSummary `json:"summary"`
}

type reportList struct {
	Items []reportItem `json:"items"`
}

// waitForPolicyReportSummary polls `kubectl get <kind>` (namespaced when namespace != "", else
// cluster-wide) until exactly one report's summary satisfies want, returning its name. Kyverno
// writes summary/results asynchronously after the triggering admission review (the aggregate
// report controller merges the ephemeral per-review report a few seconds later), so a single GET
// right after creating the watched resource routinely observes a report that exists but is still
// summary:{} -- this polls past that window rather than racing it, matching the reasoning
// waitForFluxHPASettled documents for its own settle-then-assert wait.
//
// Exactly one match is required by design: every ClusterPolicy in
// tests/e2e-artifacts/kyverno-policies.yaml is scoped by match.any[].resources.namespaces/names to
// this one subtest's namespace/name, so no other object anywhere on the shared cluster can ever
// produce a second report `want` accepts.
func waitForPolicyReportSummary(t *testing.T, kind, namespace string, want func(reportSummary) bool) string {
	t.Helper()
	var name string
	require.Eventuallyf(t, func() bool {
		args := []string{"get", kind, "-o", "json"}
		if namespace != "" {
			args = append(args, "-n", namespace)
		} else {
			args = append(args, "-A")
		}
		out, err := exec.Command("kubectl", args...).CombinedOutput()
		if err != nil {
			t.Logf("kubectl get %s: %v: %s", kind, err, out)
			return false
		}
		var list reportList
		if err := json.Unmarshal(out, &list); err != nil {
			t.Logf("unmarshal %s list: %v", kind, err)
			return false
		}
		for _, item := range list.Items {
			if want(item.Summary) {
				name = item.Metadata.Name
				return true
			}
		}
		return false
	}, 3*time.Minute, 3*time.Second, "no %s in %q ever matched the wanted summary", kind, namespace)
	return name
}
