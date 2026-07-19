package main

import (
	"testing"

	"k8s.io/klog/v2"
)

func TestE2EAgainstVanillaMinikube(t *testing.T) {
	e2eMinikubeTest(t)
	hackOpts := testHackOpts(t)
	klog.InitFlags(nil)
	t.Log("starting tests...")
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
			name:            "pods on kube-system ns should return storage-provisioner",
			args:            []string{"pods", "-n", "kube-system", "--include-events=false", "--include-managed-fields=false"},
			stdoutRegexPath: "e2e-artifacts/pods-kube-system.regex",
		},
		{
			name:            "node query should return at least a node",
			args:            []string{"node", "--include-events=false", "--include-managed-fields=false"},
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
