package main

import (
	"testing"
)

// TestE2EParallel runs the pool: the e2e subtests that share one cluster and run concurrently on
// it, which is nearly all of them, and which is where a new subtest goes by default (see
// CONTRIBUTING.md). RootCmd (cmd/main.go) and pkg/plugin no longer read a process-global viper
// singleton or package-level Now/DurationRound/StartedAfterClause overrides -- each RootCmd() call
// owns its own *viper.Viper and plugin.RenderConfig (see #694), and testHackOpts/
// viperTestHackOpts just build option values rather than mutating shared state, so calling them
// from concurrent subtests is safe. The two remaining process-global sinks on the render path --
// cmdutil.BehaviorOnFatal in RootCmd's RunE and slog.SetDefault in newRenderEngine's
// setupDeprecationFilter -- are also now safe under concurrent RootCmd().Execute() calls: the
// former is guarded by cmd/main.go's fatalMu, held only around installing/consuming the handler
// rather than around the render itself; the latter installs its filtering handler once per
// process (sync.Once) instead of rebinding it on every render (see #701).
//
// A subtest is safe to run here once it:
//   - needs no namespace, or creates/uses a namespace dedicated to that subtest (never `default`,
//     and never a namespace another subtest might also touch)
//   - never relies on a fixed cluster-scoped resource name (Node, CustomResourceDefinition,
//     ClusterRole, ...) another subtest could also use -- generate one instead, e.g. with
//     GenerateName (see createBadNode)
//
// Those are properties of the subtest itself, and a subtest that can't have them (because it
// mutates cluster-wide state its neighbours read, or because its own fixtures pin cluster-wide
// state its neighbours move) isn't a serial subtest -- it belongs on the other cluster, in
// TestE2EClusterWide (cmd/e2e_clusterwide_test.go). Nothing in this suite is exempt from
// t.Parallel() any more (#867).
//
// Add a subtest with t.Run(name, func(t *testing.T) { t.Parallel(); ... }) so it actually runs
// alongside its siblings instead of just living next to them; that subtest-level t.Parallel() is
// what makes siblings run concurrently, regardless of this function's own.
//
// This function's own t.Parallel() overlaps the whole pool with TestE2EClusterWide, which runs on
// a different cluster -- worth real minutes, since much of the e2e wall clock is latency (image
// pulls, rollout waits, metrics scrape intervals, the 15s ScheduleWindow, CrashLoopBackOff
// backoffs) rather than CPU. It became possible once the startCluster fallback stopped calling
// t.Setenv (which panics on an already-parallel test) in favour of a per-test kubeconfig path.
//
// Subtests are grouped topically into runXSubtests functions in cmd/e2e_*_test.go, each called
// once below -- see #719 for why they can't be split into separate top-level Test* functions
// instead: that would break the single e2eClients() setup / shared parallel pool this function
// provides them.
func TestE2EParallel(t *testing.T) {
	t.Parallel()
	e2eClusterTest(t, poolCluster)
	hackOpts, clientset, dynamicClient := e2eClients(t)
	runOwnersSubtests(t, hackOpts, clientset)
	runPodSchedulingSubtests(t, hackOpts, clientset)
	runNetworkPolicySubtests(t, hackOpts, clientset, dynamicClient)
	runRolloutSubtests(t, hackOpts, clientset)
	runServiceRoutingSubtests(t, hackOpts, clientset)
	runMiscFixtureSubtests(t, hackOpts, clientset)
	runTLSValidationSubtests(t, hackOpts, clientset)
	runPodVolumeSubtests(t, hackOpts, clientset)
	runPodLogsAndMetricsSubtests(t, hackOpts, clientset)
	runFluxSubtests(t, hackOpts, clientset)
	runIstioSubtests(t, hackOpts, clientset)
	runKyvernoSubtests(t, hackOpts, clientset)
	runStorageSubtests(t, hackOpts, clientset, dynamicClient)
	runCrossplaneSubtests(t, hackOpts, clientset)
	runHelmReleaseSubtests(t, hackOpts, clientset)
	runDRASubtests(t, hackOpts, clientset)
}
