package main

import (
	"testing"
)

// TestE2EParallel is a dedicated home for e2e subtests that are independent of each other and can
// therefore run concurrently. RootCmd (cmd/main.go) and pkg/plugin no longer read a process-global
// viper singleton or package-level Now/DurationRound/StartedAfterClause overrides -- each RootCmd()
// call owns its own *viper.Viper and plugin.RenderConfig (see #694), and testHackOpts/
// viperTestHackOpts just build option values rather than mutating shared state, so calling them
// from concurrent subtests is safe. The two remaining process-global sinks on the render path --
// cmdutil.BehaviorOnFatal in RootCmd's RunE and slog.SetDefault in newRenderEngine's
// setupDeprecationFilter -- are also now safe under concurrent RootCmd().Execute() calls: the
// former is guarded by cmd/main.go's fatalMu, held only around installing/consuming the handler
// rather than around the render itself; the latter installs its filtering handler once per
// process (sync.Once) instead of rebinding it on every render (see #701). A subtest qualifies for
// t.Parallel() once it:
//   - needs no namespace, or creates/uses a namespace dedicated to that subtest (never `default`,
//     and never a namespace another subtest might also touch)
//   - never relies on a fixed cluster-scoped resource name (Node, CustomResourceDefinition,
//     ClusterRole, ...) another subtest could also use -- generate one instead, e.g. with
//     GenerateName (see createBadNode)
//
// Add a qualifying subtest with t.Run(name, func(t *testing.T) { t.Parallel(); ... }) so it
// actually runs alongside its siblings instead of just living next to them; that subtest-level
// t.Parallel() is what makes siblings run concurrently, regardless of this function's own.
//
// This function itself must NOT call t.Parallel(): e2eMinikubeTest below falls back to
// startMinikube, which calls t.Setenv("KUBECONFIG", ...) for ad hoc `go test -run TestE2E...` runs
// that don't set ASSUME_MINIKUBE_IS_CONFIGURED=true -- and t.Setenv panics if called on a test
// already marked parallel.
//
// Subtests are grouped topically into runXSubtests functions in cmd/e2e_*_test.go, each called
// once below -- see #719 for why they can't be split into separate top-level Test* functions
// instead: that would break the single e2eClients() setup / shared parallel pool this function
// provides them.
func TestE2EParallel(t *testing.T) {
	e2eMinikubeTest(t)
	hackOpts, clientset, dynamicClient := e2eClients(t)
	runOwnersSubtests(t, hackOpts, clientset)
	runPodSchedulingSubtests(t, hackOpts, clientset, dynamicClient)
	runNetworkPolicySubtests(t, hackOpts, clientset, dynamicClient)
	runRolloutSubtests(t, hackOpts, clientset)
	runServiceRoutingSubtests(t, hackOpts, clientset)
	runMiscFixtureSubtests(t, hackOpts, clientset)
	runTLSValidationSubtests(t, hackOpts, clientset)
	runPodVolumeSubtests(t, hackOpts, clientset)
	runPodLogsAndMetricsSubtests(t, hackOpts, clientset)
}
