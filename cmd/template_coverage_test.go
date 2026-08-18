package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/bergerx/kubectl-status/pkg/plugin"
)

// TestMain flushes this process's accumulated template coverage after every test in the package
// has run. cmd's e2e tests (TestE2E*) call into pkg/plugin's render path in-process via
// RootCmd/plugin.Run, so any KUBECTL_STATUS_TEMPLATE_COVERAGE-gated instrumentation in
// pkg/plugin.buildTemplateSet fires here too -- but cmd's test binary is a separate OS process from
// pkg/plugin's own, with its own copy of pkg/plugin's package-level coverage recorder, so it needs
// its own flush call. See pkg/plugin.FlushTemplateCoverageProfile's doc for why this is a no-op
// (rather than clobbering a sibling process's profile) when this process never actually recorded a
// hit, e.g. `make test-e2e`'s `go test ./... -run 'TestE2E*'` also runs pkg/plugin's suite with
// zero matching tests.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := plugin.FlushTemplateCoverageProfile(); err != nil {
		fmt.Fprintln(os.Stderr, "template coverage: failed to write profile:", err)
	}
	os.Exit(code)
}
