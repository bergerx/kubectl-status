package tests

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/cli-utils/test/e2e/e2eutil"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

var watchTests = []struct {
	name              string
	inputManifestFile string
	waitForList       []string
	wantOutputFile    string
}{
	{
		"Healthy StatefulSet",
		"artifacts/test-manifests/statefulset-healthy.yaml",
		[]string{"jsonpath={.status.replicas}=3", "jsonpath={.status.readyReplicas}=3"},
		"statefulset-healthy.yaml",
	},
	{
		"Healthy Deployment",
		"artifacts/test-manifests/deployment-healthy.yaml",
		[]string{"jsonpath={.status.replicas}=3", "jsonpath={.status.updatedReplicas}=3"},
		"statefulset-healthy.yaml",
	},
}

func kubectl(t *testing.T, namespace, command string) ([]byte, error) {
	args := kubectlArgsFromCommand(namespace, command)
	t.Logf("running: kubectl %v", args)
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	t.Logf("finished: kubectl %v, out: %s, err: %s", args, string(out), err)
	if err != nil {
		return out, fmt.Errorf("kubectl command failed cmd: %s: err: %v, output: %v", command, err, string(out))
	}
	return out, nil
}

func kubectlArgsFromCommand(namespace string, command string) []string {
	args := strings.Fields(command)
	args = append(args, "--context", "kind-kind")
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return args
}

func TestWatchingManifestApply(t *testing.T) {
	t.Parallel()
	wg := sync.WaitGroup{}
	for _, tt := range watchTests {
		wg.Add(1)
		go t.Run(tt.name, func(t *testing.T) {
			diff, err := checkWatchingManifestApply(t, tt.inputManifestFile, tt.waitForList, tt.wantOutputFile)
			if err != nil {
				t.Errorf("unexpected error for '%s' using manifest %s, was expecting %s, err: %v", tt.name, tt.inputManifestFile, tt.wantOutputFile, err)
			}
			if diff != "" {
				t.Errorf("got different output for '%s' using manifest %s, was expecting %s, diff: %s", tt.name, tt.inputManifestFile, tt.wantOutputFile, diff)
			}
			wg.Done()
		})
	}
	wg.Wait()
}

// returns diff if any
func checkWatchingManifestApply(t *testing.T, manifestFile string, waitForList []string, expectedOutputFile string) (string, error) {
	ctx, cancelWatch := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancelWatch()
	namespace, deleteTempNamespace := createTempNamespace(t)
	defer deleteTempNamespace()
	var buffer bytes.Buffer
	go func() {
		options := plugin.NewOptions()
		options.Namespace = &namespace
		options.RenderOptions.Watch = true
		unst := unstructuredFromManifest(manifestFile)

		err := plugin.Run(ctx, options, []string{unst.GetKind()}, &buffer) // TODO need to implement ctx support for Run
		if err != nil {
			t.Logf("status watch command failed: %#v", err)
		}
	}()
	err := applyManifest(t, namespace, manifestFile)
	if err != nil {
		return "", err
	}
	time.Sleep(3 * time.Second)
	err = waitFor(ctx, t, namespace, manifestFile, waitForList)
	if err != nil {
		return "", err
	}

	cancelWatch()
	s := buffer.String()
	return s, nil
}

func waitFor(ctx context.Context, t *testing.T, namespace string, manifestFile string, waitForList []string) error {
	wg := sync.WaitGroup{}
	var errs []error
	for _, waitFor := range waitForList {
		wg.Add(1)
		go func() {
			t.Logf("waiting until condition (%s) is met for %s in %s namespace", waitFor, manifestFile, namespace)
			out, err := kubectl(t, namespace, fmt.Sprintf("wait --for=%s -f %s --allow-missing-template-keys", waitFor, manifestFile))
			if err != nil {
				errs = append(errs, fmt.Errorf("wait command failed: %v, %s", err, string(out)))
			}
			t.Logf("wait condition (%s) is met for %s in %s namespace", waitFor, manifestFile, namespace)
			wg.Done()
		}()
		wg.Wait()
	}
	if len(errs) != 0 {
		return fmt.Errorf("wait command failed: %v", errs)
	}
	return nil
}

func applyManifest(t *testing.T, namespace, manifestFile string) error {
	command := fmt.Sprintf("apply -f %s", manifestFile)
	t.Logf("applying manifest '%s' in namespace %s: kubectl %s", manifestFile, namespace, command)
	out, err := kubectl(t, namespace, command)
	if err != nil {
		return fmt.Errorf("apply failed with: %w, output: %s", err, string(out))
	}
	return nil
}

func unstructuredFromManifest(manifestFile string) *unstructured.Unstructured {
	manifest, _ := os.ReadFile(manifestFile)
	return e2eutil.ManifestToUnstructured(manifest)
}

func createTempNamespace(t *testing.T) (string, func()) {
	namespace := e2eutil.RandomString("status-e2e-test-")
	t.Logf("creating namespace %s for tests", namespace)
	kubectl(t, "", fmt.Sprintf("create namespace %s", namespace))
	kubectl(t, "", fmt.Sprintf("label namespace %s created-for=kubectl-status-e2e-tests", namespace))
	return namespace, func() {
		t.Logf("deleting namespace %s for tests", namespace)
		kubectl(t, "", fmt.Sprintf("delete namespace %s", namespace))
	}
}
