package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

type cmdTest struct {
	name        string
	args        []string
	stdoutRegex string // Regex
	stdoutEqual string // Regex
	stderrRegex string // Regex
	stderrEqual string // Regex
	wantErr     string // Contains
}

func (c cmdTest) assert(t *testing.T) {
	t.Helper()
	stdout, stderr, err := executeCMD(t, c.args)
	switch {
	case c.stdoutRegex == "" && c.stdoutEqual == "":
		assert.Empty(t, stdout)
	case c.stdoutRegex != "":
		assert.Regexp(t, c.stdoutRegex, stdout)
	case c.stdoutEqual != "":
		assert.Equal(t, c.stdoutEqual, stdout)
	}
	switch {
	case c.stderrRegex == "" && c.stderrEqual == "":
		assert.Empty(t, stderr)
	case c.stderrRegex != "":
		assert.Regexp(t, c.stderrRegex, stderr)
	case c.stderrEqual != "":
		assert.Equal(t, c.stderrEqual, stderr)
	}
	if c.wantErr != "" {
		assert.ErrorContains(t, err, c.wantErr)
	}
}

func TestRootCmdWithoutACluster(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	defer plugin.SetDurationRound(func(_ interface{}) string { return "1m" })()
	tests := []cmdTest{
		{
			name:        "empty call should print an error and simple usage",
			stderrRegex: `You must provide one or more resources by argument or filename.\nExample resource specifications`,
		},
		{
			name:        "pods against a non-configured client should print an error",
			args:        []string{"pods"},
			stderrRegex: `The connection to the server localhost:8080 was refused`,
		},
		{
			name:        "missing file should fail",
			args:        []string{"-f", "non-existing.yaml"},
			stderrRegex: `error: the path \"non-existing.yaml\" does not exist\n$`,
		},
		{
			name:        "file without local should fail",
			args:        []string{"-f", "../tests/artifacts/deployment-healthy.yaml"},
			stderrRegex: `dial tcp \[::1\]:8080: connect: connection refused\n$`,
		},
		{
			name:        "file with local should succeed",
			args:        []string{"-f", "../tests/artifacts/deployment-healthy.yaml", "--local"},
			stdoutRegex: `^\nDeployment/httpbin-deployment`,
		},
		{
			name: "cr file with local with status should render ready conditions",
			args: []string{"-f", "../tests/artifacts/cr-dbconn-mymysql.yaml", "--local"},
			stdoutRegex: `^
DatabaseConnection/mymysql -n default, created 1m ago
  Current: Resource is Ready
  Ready ConnectionEstablished, Database connection successfully established. for 1m
$`,
		},
		{
			name:        "file with 'kind: List' should list all resources",
			args:        []string{"-f", "../tests/artifacts/multiple-2-pods-list.yaml", "--local"},
			stdoutRegex: `(?ms)Pod/etcd-minikube.*Pod/storage-provisioner`,
			// TODO: logs `couldn't get current server API group list: Get "http://localhost:8080/api?timeout=32s": dial tcp [::1]:8080: connect: connection refused`
		},
		{
			name:        "file with multiple yaml documents should list all resources",
			args:        []string{"-f", "../tests/artifacts/multiple-2-pods-docs.yaml", "--local"},
			stdoutRegex: `(?ms)Pod/etcd-minikube.*Pod/storage-provisioner`,
			// TODO: logs `couldn't get current server API group list: Get "http://localhost:8080/api?timeout=32s": dial tcp [::1]:8080: connect: connection refused`
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t)
		})
	}
}

func TestE2EAgainstVanillaMinikube(t *testing.T) {
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping e2e test")
	}
	if os.Getenv("ASSUME_MINIKUBE_IS_CONFIGURED") != "true" {
		defer startMinikube(t, "kubectl-status-e2e")()
	}
	defer plugin.SetDurationRound(func(_ interface{}) string { return "1m" })()
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
			name:        "pods on kube-system ns should return storage-provisioner",
			args:        []string{"pods", "-n", "kube-system"},
			stdoutRegex: `^\nPod/[a-z0-9-]+ -n kube-system`,
		},
		{
			name:        "node query should return at least a node",
			args:        []string{"node"},
			stdoutRegex: `^\nNode/`,
		},
		{
			name:        "cr file without a crd should fail",
			args:        []string{"-f", "../tests/artifacts/cr-dbconn-mymysql.yaml"},
			stderrRegex: `no matches for kind "DatabaseConnection" in version "example.com/v1alpha1"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t)
		})
	}
}

func TestAllArtifacts(t *testing.T) {
	defer plugin.SetDurationRound(func(_ interface{}) string { return "1m" })()
	t.Setenv("KUBECONFIG", "/dev/null")
	viper.Set("test", true)
	artifacts, err := filepath.Glob("../tests/artifacts/*.yaml")
	assert.NoError(t, err)
	for _, artifact := range artifacts {
		t.Run(strings.Replace(artifact, "../", "", -1), func(t *testing.T) {
			outFile := strings.Replace(artifact, ".yaml", ".out", -1)
			out, err := os.ReadFile(outFile)
			assert.NoError(t, err)
			test := cmdTest{
				args:        []string{"-f", artifact, "--local", "--shallow", "--v", "255"},
				stdoutEqual: string(out),
			}
			test.assert(t) // to update the out files check /tests/artifacts/README.md
		})
	}
}

func executeCMD(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	cmd := RootCmd()
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	stderr := &bytes.Buffer{}
	cmd.SetErr(stderr)
	cmd.SetArgs(args)
	t.Logf("running command with: %s", strings.Join(args, " "))
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func startMinikube(t *testing.T, clusterName string) (deleteMinikube func()) {
	t.Helper()
	t.Log("Creating temp folder for minikube.kubeconfig...")
	dir, err := os.MkdirTemp("", clusterName)
	assert.NoError(t, err)
	kubeconfig := path.Join(dir, "minikube.kubeconfig")
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Log("Starting Minikube cluster...")
	startMinikube := exec.Command("minikube", "start", "-p", clusterName)
	assert.NoError(t, startMinikube.Run())
	return func() {
		cmd := exec.Command("minikube", "delete", "-p", clusterName)
		t.Log("Deleting Minikube cluster...")
		if err := cmd.Run(); err != nil {
			t.Log("Error deleting Minikube cluster:", err)
		}
		t.Log("Deleting temp folder of minikube.kubeconfig...")
		if err := os.RemoveAll(dir); err != nil {
			t.Log("Error deleting temp folder of minikube.kubeconfig:", err)
		}
	}
}

func TestE2EDynamicManifests(t *testing.T) {
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping e2e test")
	}
	if os.Getenv("ASSUME_MINIKUBE_IS_CONFIGURED") != "true" {
		defer startMinikube(t, "kubectl-status-e2e")()
	}
	defer plugin.SetDurationRound(func(_ interface{}) string { return "1m" })()
	viper.Set("test", true)
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		homeDir := os.Getenv("HOME")
		kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		t.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("owners should be included with deep", func(t *testing.T) {
		owner := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner",
				Namespace: "default",
			},
		}
		owner, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), owner, metav1.CreateOptions{})
		defer clientset.CoreV1().Secrets("default").Delete(context.TODO(), owner.Name, metav1.DeleteOptions{})
		assert.NoError(t, err)
		// Create the owned secret with owner reference
		owned := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "Secret",
						Name:       "owner",
						UID:        owner.UID,
					},
				},
			},
		}
		_, err = clientset.CoreV1().Secrets("default").Create(context.TODO(), owned, metav1.CreateOptions{})
		defer clientset.CoreV1().Secrets("default").Delete(context.TODO(), owned.Name, metav1.DeleteOptions{})
		assert.NoError(t, err)

		test := cmdTest{
			args: []string{"secret/owned", "--deep", "--v", "255"},
			stdoutRegex: `(?ms)
Secret\/owned -n default, created 1m ago by Secret/owner
  Current: Resource is always ready
  Known\/recorded manage events:
    1m ago Updated by [^ ]+ \(metadata, type\)
  Owners:
    Secret\/owner -n default, created 1m ago
      Current: Resource is always ready
      Known\/recorded manage events:
        1m ago Updated by [^ ]+ \(type\)
`,
		}
		test.assert(t) // to update the out files check /tests/artifacts/README.md
	})
}
