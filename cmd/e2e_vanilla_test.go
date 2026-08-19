package main

import (
	"testing"

	"k8s.io/klog/v2"
)

func TestE2EAgainstVanillaCluster(t *testing.T) {
	e2eClusterTest(t)
	hackOpts := testHackOpts(t)
	klog.InitFlags(nil)
	t.Log("starting tests...")
	applyManifest(t, "e2e-artifacts/pods-in-namespace.yaml")
	tests := []cmdTest{
		{
			name:        "empty call should print an error and usage",
			stderrRegex: `You must provide one or more resources by argument or filename.\nExample resource specifications`,
		},
		{
			name:        "pods on non-existent ns should fail",
			args:        []string{"pods", "-n", "non-existent"},
			stderrRegex: `error: no resources found\n$`,
		},
		{
			// Renders against a namespace this test owns rather than kube-system: the pods there
			// belong to the cluster, so their images, replica-hash names, restart counts and live
			// usage all move with the Kubernetes version and with whatever else the
			// shared cluster has been doing, none of which a whole-output fixture can pin.
			name:            "pods in a namespace should render every pod in it",
			args:            []string{"pods", "-n", "e2e-pods-in-namespace", "--include-events=false", "--include-managed-fields=false"},
			stdoutRegexPath: "e2e-artifacts/pods-in-namespace.regex",
		},
		{
			// The kubelet-api-summary and detailed-usage sections are per-pod views of whatever
			// else happens to be running on the shared cluster (a "pods needing attention" list
			// keyed off restart counts, a pod-by-usage table ordered by live memory), so they
			// can't be pinned in a whole-output fixture -- turn them off rather than settle for
			// matching only the parts of the Node render that are stable.
			name: "node query should return at least a node",
			args: []string{"node", "--include-events=false", "--include-managed-fields=false",
				"--include-node-kubelet-api-summary=false", "--include-node-detailed-usage=false"},
			stdoutRegexPath: "e2e-artifacts/node-query.regex",
		},
		{
			name:        "cr file without a crd should fail",
			args:        []string{"-f", "../tests/artifacts/cr-dbconn-mymysql.yaml"},
			stderrRegex: `no matches for kind "DatabaseConnection" in version "example.com/v1alpha1"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t, nil, combineOpts(hackOpts, viperTestHackOpts())...)
		})
	}
}
