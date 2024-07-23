package main

import (
	"bytes"
	"os"
	"os/exec"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/klog/v2"
)

func TestRootCmd(t *testing.T) {
	_ = os.Setenv("KUBECONFIG", "/dev/null")
	tests := []struct {
		name    string
		args    []string
		stdout  string
		stderr  string
		wantErr string
	}{
		{
			name:   "empty call should print an error and simple usage",
			stdout: `^$`,
			stderr: "You must provide one or more resources by argument or filename.\nExample resource specifications",
		},
		{
			name:   "pods against a non-configured client should print an error",
			args:   []string{"pods"},
			stdout: `^$`,
			stderr: "The connection to the server localhost:8080 was refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.args == nil {
				tt.args = []string{}
			}
			stdout, stderr, err := execute(tt.args)
			if tt.stdout != "" {
				assert.Regexp(t, tt.stdout, stdout)
			}
			if tt.stderr != "" {
				assert.Regexp(t, tt.stderr, stderr)
			}
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestE2EWithVanillaMinikube(t *testing.T) {
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping e2e test")
	}
	defer startMinikube(t, "kubectl-status-e2e")()
	klog.InitFlags(nil)
	t.Log("starting tests...")
	tests := []struct {
		name    string
		args    []string
		stdout  string
		stderr  string
		wantErr string
	}{
		{
			name:   "empty call should print an error and usage",
			stdout: `^$`,
			stderr: `You must provide one or more resources by argument or filename.\nExample resource specifications`,
		},
		{
			name:   "pods on non-existent ns should fail",
			args:   []string{"pods", "-n", "non-existent"},
			stdout: `^$`,
			stderr: `error: no resources found\n$`,
		},
		{
			name:   "pods on kube-system ns should return storage-provisioner",
			args:   []string{"pods", "-n", "kube-system"},
			stdout: `^\nPod/[a-z0-9-]+ -n kube-system`,
			stderr: `^$`,
		},
		{
			name:   "node query should return return at least a node",
			args:   []string{"node"},
			stdout: `^\nNode/`,
			stderr: `^$`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.args == nil {
				tt.args = []string{}
			}
			stdout, stderr, err := execute(tt.args)
			if tt.stdout != "" {
				assert.Regexp(t, tt.stdout, stdout)
			}
			if tt.stderr != "" {
				assert.Regexp(t, tt.stderr, stderr)
			}
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func execute(args []string) (string, string, error) {
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
