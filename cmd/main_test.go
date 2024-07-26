package main

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/klog/v2"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

type cmdTest struct {
	name    string
	args    []string
	stdout  string // Regex
	stderr  string // Regex
	wantErr string // Contains
}

func (c cmdTest) assert(t *testing.T) {
	t.Helper()
	stdout, stderr, err := executeCMD(c.args)
	if c.stdout == "" {
		assert.Empty(t, stdout)
	} else {
		assert.Regexp(t, c.stdout, stdout)
	}
	if c.stderr == "" {
		assert.Empty(t, stderr)
	} else {
		assert.Regexp(t, c.stderr, stderr)
	}
	if c.wantErr != "" {
		assert.ErrorContains(t, err, c.wantErr)
	}
}

func TestRootCmdWithoutACluster(t *testing.T) {
	_ = os.Setenv("KUBECONFIG", "/dev/null")
	defer plugin.SetDurationRound(func(_ interface{}) string { return "1m" })()
	tests := []cmdTest{
		{
			name:   "empty call should print an error and simple usage",
			stderr: `You must provide one or more resources by argument or filename.\nExample resource specifications`,
		},
		{
			name:   "pods against a non-configured client should print an error",
			args:   []string{"pods"},
			stderr: `The connection to the server localhost:8080 was refused`,
		},
		{
			name:   "missing file should fail",
			args:   []string{"-f", "non-existing.yaml"},
			stderr: `error: the path \"non-existing.yaml\" does not exist\n$`,
		},
		{
			name:   "file without local should fail",
			args:   []string{"-f", "../tests/artifacts/deployment-healthy.yaml"},
			stderr: `dial tcp \[::1\]:8080: connect: connection refused\n$`,
		},
		{
			name:   "file with local should succeed",
			args:   []string{"-f", "../tests/artifacts/deployment-healthy.yaml", "--local"},
			stdout: `^\nDeployment/httpbin-deployment`,
		},
		{
			name: "cr file with local with status should render ready conditions",
			args: []string{"-f", "../tests/artifacts/cr-dbconn-mymysql.yaml", "--local"},
			stdout: `^
DatabaseConnection/mymysql -n default, created 1m ago
  Current: Resource is Ready
  Ready ConnectionEstablished, Database connection successfully established. for 1m
$`,
		},
		{
			name:   "file with kind: List should list both resources",
			args:   []string{"-f", "../tests/artifacts/multiple-2-pods-list.yaml", "--local"},
			stdout: `(?ms)Pod/etcd-minikube.*Pod/storage-provisioner`,
			// TODO: logs `couldn't get current server API group list: Get "http://localhost:8080/api?timeout=32s": dial tcp [::1]:8080: connect: connection refused`
		},
		{
			name:   "file with multiple yaml documents should list all resources",
			args:   []string{"-f", "../tests/artifacts/multiple-2-pods-docs.yaml", "--local"},
			stdout: `(?ms)Pod/etcd-minikube.*Pod/storage-provisioner`,
			// TODO: logs `couldn't get current server API group list: Get "http://localhost:8080/api?timeout=32s": dial tcp [::1]:8080: connect: connection refused`
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t)
		})
	}
}

func TestE2EWithVanillaMinikube(t *testing.T) {
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping e2e test")
	}
	defer startMinikube(t, "kubectl-status-e2e")()
	defer plugin.SetDurationRound(func(_ interface{}) string { return "1m" })()
	klog.InitFlags(nil)
	t.Log("starting tests...")
	tests := []cmdTest{
		{
			name:   "empty call should print an error and usage",
			stderr: `You must provide one or more resources by argument or filename.\nExample resource specifications`,
		},
		{
			name:   "pods on non-existent ns should fail",
			args:   []string{"pods", "-n", "non-existent"},
			stderr: `error: no resources found\n$`,
		},
		{
			name:   "pods on kube-system ns should return storage-provisioner",
			args:   []string{"pods", "-n", "kube-system"},
			stdout: `^\nPod/[a-z0-9-]+ -n kube-system`,
		},
		{
			name:   "node query should return return at least a node",
			args:   []string{"node"},
			stdout: `^\nNode/`,
		},
		{
			name:   "cr file without a crd should fail",
			args:   []string{"-f", "../tests/artifacts/cr-dbconn-mymysql.yaml"},
			stderr: `no matches for kind "DatabaseConnection" in version "example.com/v1alpha1"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t)
		})
	}
}

func executeCMD(args []string) (string, string, error) {
	cmd := RootCmd()
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func startMinikube(t *testing.T, clusterName string) (deleteMinikube func()) {
	t.Log("Creating temp folder...")
	dir, err := os.MkdirTemp("", clusterName)
	assert.NoError(t, err)
	kubeconfig := path.Join(dir, "minikube.kubeconfig")
	_ = os.Setenv("KUBECONFIG", kubeconfig)
	t.Log("Starting Minikube cluster...")
	startMinikube := exec.Command("minikube", "start", "-p", clusterName)
	assert.NoError(t, startMinikube.Run())
	return func() {
		cmd := exec.Command("minikube", "delete", "-p", clusterName)
		t.Log("Deleting Minikube cluster...")
		if err := cmd.Run(); err != nil {
			t.Log("Error deleting Minikube cluster:", err)
		}
		t.Log("Deleting temp folder...")
		if err := os.RemoveAll(dir); err != nil {
			t.Log("Error deleting temp folder:", err)
		}
	}
}
