package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

func TestRootCmdWithoutACluster(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	opts := []func(*plugin.RenderConfig){
		func(cfg *plugin.RenderConfig) {
			cfg.DurationRound = func(_ interface{}) string { return "1m" }
		},
	}
	tests := []cmdTest{
		{
			name:        "empty call should print an error and simple usage",
			stderrRegex: `You must provide one or more resources by argument or filename.\nExample resource specifications`,
		},
		{
			name:        "pods against a non-configured client should print an error",
			args:        []string{"pods"},
			stderrRegex: `the server.*(rejected|refused)`,
		},
		{
			name:        "missing file should fail",
			args:        []string{"-f", "non-existing.yaml"},
			stderrRegex: `error: the path \"non-existing.yaml\" does not exist\n$`,
		},
		{
			name:        "file without local should fail",
			args:        []string{"-f", "../tests/artifacts/deployment-healthy.yaml"},
			stderrRegex: `the server rejected our request for an unknown reason|connect: connection refused`,
		},
		{
			name:            "file with local should succeed",
			args:            []string{"-f", "../tests/artifacts/deployment-healthy.yaml", "--local"},
			stdoutRegexPath: "artifacts/deployment-healthy.local.regex",
		},
		{
			name:            "cr file with local with status should render ready conditions",
			args:            []string{"-f", "../tests/artifacts/cr-dbconn-mymysql.yaml", "--local"},
			stdoutRegexPath: "artifacts/cr-dbconn-mymysql.local.regex",
		},
		{
			name:            "file with 'kind: List' should list all resources",
			args:            []string{"-f", "../tests/artifacts/multiple-2-pods-list.yaml", "--local"},
			stdoutRegexPath: "artifacts/multiple-2-pods-list.local.regex",
		},
		{
			name:            "file with multiple yaml documents should list all resources",
			args:            []string{"-f", "../tests/artifacts/multiple-2-pods-docs.yaml", "--local"},
			stdoutRegexPath: "artifacts/multiple-2-pods-docs.local.regex",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.assert(t, nil, opts...)
		})
	}
}

// TestDeepRespectsExplicitIncludeFalse guards a fix to --deep: it used to force every
// --include-* flag to true unconditionally (enableAllIncludes), silently overriding an explicit
// --include-events=false/--include-managed-fields=false passed alongside it on the same command
// line -- exactly the flags the e2e "--deep" test cases rely on to keep their fixtures
// deterministic. --deep now only fills in flags the user didn't set explicitly, mirroring how
// --shallow already respects an explicit --include-*=true. This is a pure viper/pflag check, no
// live cluster or even a real render involved, so it runs unconditionally.
func TestDeepRespectsExplicitIncludeFalse(t *testing.T) {
	v := viper.New()
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	addRenderFlags(flags)
	require.NoError(t, v.BindPFlags(flags))
	require.NoError(t, flags.Parse([]string{"--deep", "--include-events=false", "--include-managed-fields=false"}))

	require.True(t, v.GetBool("deep"))
	enableAllIncludes(v)

	assert.False(t, v.GetBool("include-events"), "explicit --include-events=false must survive --deep")
	assert.False(t, v.GetBool("include-managed-fields"), "explicit --include-managed-fields=false must survive --deep")
	assert.True(t, v.GetBool("include-owners"), "--deep must still enable flags the user didn't set explicitly")
}

// TestE2ERegexFixturesAreAnchored guards the whole-output convention documented in
// CONTRIBUTING.md: a fixture under tests/e2e-artifacts/ either pins the full rendered output,
// anchored at both ends with `\A`...`\z`, or is a deliberately partial one-off-lines match with
// neither anchor. A fixture with only one of the two anchors is neither -- almost always a fixture
// that was meant to be anchored but lost its `\z` (or gained a stray `\A`) while being edited, so
// it silently stopped verifying the parts of the output past/before the missing anchor. This
// doesn't require a live cluster, so it runs unconditionally.
func TestE2ERegexFixturesAreAnchored(t *testing.T) {
	fixtures, err := filepath.Glob("../tests/e2e-artifacts/*.regex")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures)
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			content, err := os.ReadFile(fixture)
			require.NoError(t, err)
			startsAnchored := bytes.HasPrefix(content, []byte(`\A`))
			endsAnchored := bytes.HasSuffix(content, []byte(`\z`))
			if startsAnchored != endsAnchored {
				t.Errorf("%s: has `\\A` at the start=%v but `\\z` at the end=%v -- a whole-output "+
					"fixture needs both anchors (see CONTRIBUTING.md), a partial one-off-lines "+
					"fixture needs neither; use the --include-* flags to trim sections you don't "+
					"want to pin instead of matching only part of the output",
					fixture, startsAnchored, endsAnchored)
			}
		})
	}
}

func TestAllArtifactsLocal(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	opts := combineOpts(testHackOpts(t), viperTestHackOpts())
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
			test.assert(t, nil, opts...) // to update the out files check /tests/artifacts/README.md
		})
	}
}

func TestAllArtifactsLocalWithIncludeAllVolumes(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	opts := combineOpts(testHackOpts(t), viperTestHackOpts())
	artifacts := []string{
		"../tests/artifacts/pod-standalone.yaml",
		"../tests/artifacts/pod-missing-pvc.yaml",
	}
	for _, artifact := range artifacts {
		artifact := artifact
		name := strings.Replace(artifact, "../tests/", "", 1)
		name = strings.Replace(name, ".yaml", "", 1)
		t.Run(name, func(t *testing.T) {
			test := cmdTest{
				args:            []string{"-f", artifact, "--local", "--shallow", "--v", "255", "--include-all-volumes"},
				stdoutEqualPath: name + ".include-all-volumes.out",
			}
			test.assert(t, nil, opts...)
		})
	}
}

func TestAllArtifactsLocalWithAbsoluteTime(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	opts := combineOpts(viperTestHackOpts())
	artifacts := []string{
		"../tests/artifacts/pod-standalone.yaml",
	}
	for _, artifact := range artifacts {
		artifact := artifact
		name := strings.Replace(artifact, "../tests/", "", 1)
		name = strings.Replace(name, ".yaml", "", 1)
		t.Run(name, func(t *testing.T) {
			test := cmdTest{
				args:            []string{"-f", artifact, "--local", "--shallow", "--v", "255", "--absolute-time"},
				stdoutEqualPath: name + ".absolute-time.out",
			}
			test.assert(t, nil, opts...)
		})
	}
}
