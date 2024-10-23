package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

type cmdTest struct {
	name            string
	args            []string
	stdoutRegex     string // Regex
	stdoutRegexPath string // Regex match against file contents under test folder
	stdoutEqual     string // Exact
	stdoutEqualPath string // Exact match with file contents under test folder
	stderrRegex     string // Regex
	stderrEqual     string // Exact
	wantErr         string // Contains
}

func nodeNameModifier(stdout string) string {
	return string(regexp.MustCompile(`Node/[a-z0-9-]+`).ReplaceAll([]byte(stdout), []byte(`Node/minikube`)))
}

func (c cmdTest) assert(t *testing.T, stdoutModifier func(string) string) {
	t.Helper()
	t.Logf("running cmdTest assert: %s", c)
	stdout, stderr, err := executeCMD(t, c.args)
	if stdoutModifier != nil {
		stdout = nodeNameModifier(stdout)
	}
	switch {
	case c.stdoutRegex == "" && c.stdoutEqual == "" && c.stdoutRegexPath == "" && c.stdoutEqualPath == "":
		assert.Empty(t, stdout)
	case c.stdoutRegex != "":
		assert.Regexp(t, c.stdoutRegex, stdout)
	case c.stdoutEqual != "":
		assert.Equal(t, c.stdoutEqual, stdout)
	case c.stdoutEqualPath != "":
		outFile := path.Join("..", "tests", c.stdoutEqualPath)
		out, err := os.ReadFile(outFile)
		assert.NoErrorf(t, err, "failed to read test artifact file: %s", outFile)
		assert.Equal(t, string(out), stdout)
	case c.stdoutRegexPath != "":
		outFile := path.Join("..", "tests", c.stdoutRegexPath)
		regexBytes, err := os.ReadFile(outFile)
		assert.NoErrorf(t, err, "failed to read test artifact file: %s", outFile)
		regex := `(?ms)` + string(regexBytes)
		assert.Regexp(t, regex, stdout)
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
			stderrRegex: `the server rejected our request for an unknown reason`,
		},
		{
			name:        "missing file should fail",
			args:        []string{"-f", "non-existing.yaml"},
			stderrRegex: `error: the path \"non-existing.yaml\" does not exist\n$`,
		},
		{
			name:        "file without local should fail",
			args:        []string{"-f", "../tests/artifacts/deployment-healthy.yaml"},
			stderrRegex: `the server rejected our request for an unknown reason\n$`,
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
			tt.assert(t, nil)
		})
	}
}

func TestE2EAgainstVanillaMinikube(t *testing.T) {
	e2eMinikubeTest(t)
	testHack(t)
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
			viperTestHack(t)
			tt.assert(t, nil)
		})
	}
}

func testHack(t *testing.T) {
	t.Helper()
	durationRevert := plugin.SetDurationRound(func(_ interface{}) string { return "1m" })
	t.Cleanup(func() {
		durationRevert()
	})
}

func viperTestHack(t *testing.T) {
	t.Helper()
	viper.Reset()
	viper.Set("test-hack", true)
	t.Cleanup(func() {
		viper.Reset()
	})
}

func TestAllArtifactsLocal(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	testHack(t)
	viperTestHack(t)
	artifacts, err := filepath.Glob("../tests/artifacts/*.yaml")
	assert.NoError(t, err)
	for _, artifact := range artifacts {
		name := strings.Replace(artifact, "../tests/", "", 1)
		name = strings.Replace(name, ".yaml", "", 1)
		t.Run(name, func(t *testing.T) {
			test := cmdTest{
				args:            []string{"-f", artifact, "--local", "--shallow", "--v", "255"},
				stdoutEqualPath: name + ".out",
			}
			test.assert(t, nil) // to update the out files check /tests/artifacts/README.md
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

func startMinikube(t *testing.T) {
	t.Helper()
	clusterName := t.Name()
	t.Logf("Creating temp folder for minikube.kubeconfig for minikube %s ...", clusterName)
	dir, err := os.MkdirTemp("", clusterName)
	require.NoError(t, err)
	kubeconfig := path.Join(dir, "minikube.kubeconfig")
	t.Setenv("KUBECONFIG", kubeconfig)
	t.Logf("Starting Minikube cluster %s with %s ...", clusterName, kubeconfig)
	startMinikube := exec.Command("minikube", "start", "-p", clusterName)
	require.NoError(t, startMinikube.Run())
	t.Cleanup(func() {
		cmd := exec.Command("minikube", "delete", "-p", clusterName)
		t.Logf("Deleting Minikube cluster %s...", clusterName)
		if err := cmd.Run(); err != nil {
			t.Log("Error deleting Minikube cluster:", err)
		}
		t.Logf("Deleting temp folder for minikube %s: %s ...", clusterName, dir)
		if err := os.RemoveAll(dir); err != nil {
			t.Log("Error deleting temp folder of minikube.kubeconfig:", err)
		}
	})
}

func e2eMinikubeTest(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping e2e test as RUN_E2E_TESTS is not set to true")
	}
	if os.Getenv("ASSUME_MINIKUBE_IS_CONFIGURED") == "true" {
		t.Logf("assuming current kubeconfig context is pointng a minikube to run e2e tests")
	} else {
		startMinikube(t)
	}
}

func TestE2EDynamicManifests(t *testing.T) {
	e2eMinikubeTest(t)
	testHack(t)
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
		viperTestHack(t)
		owner := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner",
				Namespace: "default",
			},
		}
		owner, err := clientset.CoreV1().Secrets("default").Create(context.TODO(), owner, metav1.CreateOptions{})
		defer clientset.CoreV1().Secrets("default").Delete(context.TODO(), "owner", metav1.DeleteOptions{})
		require.NoError(t, err)
		uid := owner.GetUID()
		t.Logf("owner secret is created, uid is %s", uid)
		// Create the child secret with owner reference
		child := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "child",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: "v1",
						Kind:       "Secret",
						Name:       "owner",
						UID:        uid,
					},
				},
			},
		}
		_, err = clientset.CoreV1().Secrets("default").Create(context.TODO(), child, metav1.CreateOptions{})
		t.Log("child secret is created")
		defer clientset.CoreV1().Secrets("default").Delete(context.TODO(), "child", metav1.DeleteOptions{})
		require.NoError(t, err)

		test := cmdTest{
			args: []string{"secret/child", "--deep", "--v", "7"},
			stdoutRegex: `(?ms)
Secret\/child -n default, created 1m ago by Secret/owner
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
		test.assert(t, nil) // to update the out files check /tests/artifacts/README.md
	})
	t.Run("sts-with-ingress", func(t *testing.T) {
		viperTestHack(t)
		// using sts here as the pod name is predictable in that case, not true for deployments and ds
		applyManifest(t, "e2e-artifacts/sts-with-ingress.yaml")
		waitFor(t, "sts/sts-with-ingress", "jsonpath={.status.readyReplicas}=1")
		cmdTest{
			args:            []string{"pod/sts-with-ingress-0", "--include-events=false", "--v", "5"},
			stdoutEqualPath: "e2e-artifacts/sts-with-ingress.pod.out",
		}.assert(t, nodeNameModifier)
	})
}

func applyManifest(t *testing.T, filepath string) {
	t.Helper()
	filepath = path.Join("..", "tests", filepath)
	cmd := exec.Command("kubectl", "apply", "-f", filepath)
	output, err := cmd.CombinedOutput()
	t.Cleanup(func() {
		t.Logf("deleting manifest %s", filepath)
		cmd := exec.Command("kubectl", "delete", "-f", filepath)
		output, err := cmd.CombinedOutput()
		assert.NoError(t, err)
		t.Logf("manifest deleted %s: %s", filepath, string(output))
	})
	require.NoError(t, err)
	t.Logf("applied manifest %s: %s", filepath, string(output))
}

func waitFor(t *testing.T, resource, forParam string) {
	t.Helper()
	cmd := exec.Command("kubectl", "wait", "--for", forParam, resource)
	output, err := cmd.CombinedOutput()
	t.Logf("wait result for %s: %s", resource, string(output))
	require.NoError(t, err)
}
