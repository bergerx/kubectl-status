# Contributing to kubectl-status

First off, thanks for taking the time to contribute! ❤️

All types of contributions are encouraged and valued. See the [Table of Contents](#table-of-contents) for different ways
to help and details about how this project handles them. Please make sure to read the relevant section before making
your contribution. It will make it a lot easier for us maintainers and smooth out the experience for all involved. The
community looks forward to your contributions. 🎉

> And if you like the project, but just don't have time to contribute, that's fine. There are other easy ways to support the project and show your appreciation, which we would also be very happy about:
> - Star the project
> - Tweet about it
> - Refer this project in your project's readme
> - Mention the project at local meetups and tell your friends/colleagues

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [I Have a Question](#i-have-a-question)
- [I Want To Contribute](#i-want-to-contribute)
  - [Legal Notice](#legal-notice)
  - [Reporting Bugs](#reporting-bugs)
    - [Before Submitting a Bug Report](#before-submitting-a-bug-report)
    - [How Do I Submit a Good Bug Report?](#how-do-i-submit-a-good-bug-report-)
  - [Suggesting Enhancements](#suggesting-enhancements)
    - [Before Submitting an Enhancement](#before-submitting-an-enhancement)
    - [How Do I Submit a Good Enhancement Suggestion?](#how-do-i-submit-a-good-enhancement-suggestion-)
  - [Your First Code Contribution](#your-first-code-contribution)
  - [Claude Code Integration](#claude-code-integration)
  - [Improving The Documentation](#improving-the-documentation)
- [Styleguides](#styleguides)
  - [Commit Messages](#commit-messages)
- [Releasing a new version](#releasing-a-new-version)

## Code of Conduct

This project and everyone participating in it is governed by the
[kubectl-status Code of Conduct](https://github.com/bergerx/kubectl-status/blob/master/CODE_OF_CONDUCT.md). By
participating, you are expected to uphold this code. Please report unacceptable behavior to <>.

## I Have a Question

> If you want to ask a question, we assume that you have read the
> [README.md](https://github.com/bergerx/kubectl-status/blob/master/README.md) and [CONTRIBUTING.md](https://github.com/bergerx/kubectl-status/blob/master/CONTRIBUTING.md) (this file).

Before you ask a question, it is best to search for existing [Issues](https://github.com/bergerx/kubectl-status/issues)
that might help you. In case you have found a suitable issue and still need clarification, you can write your question
in this issue. It is also advisable to search the internet for answers first.

If you then still feel the need to ask a question and need clarification, we recommend the following:

- Open an [Issue](https://github.com/bergerx/kubectl-status/issues/new).
- Provide as much context as you can about what you're running into.

We will then take care of the issue as soon as possible.

## I Want To Contribute

> ### Legal Notice
> When contributing to this project, you must agree that you have authored 100% of the content,
> that you have the necessary rights to the content and that the content you contribute may be provided under
> the project license.

### Reporting Bugs

#### Before Submitting a Bug Report

A good bug report shouldn't leave others needing to chase you up for more information. Therefore, we ask you to
investigate carefully, collect information and describe the issue in detail in your report. Please complete the
following steps in advance to help us fix any potential bug as fast as possible.

- Make sure that you are using the latest version (`kubectl krew upgrade status`).
- Try running the faulty command with these extra flags as this would provide more details about what's going on behind
  the scenes:
    - `-v 3` reports warnings and ignored/silenced errors,
    - `-v 5` enables verbose logging
    - `-v 8` also logs Kubernetes all API requests and truncated responses, or
    - `-v 10` same with previous but doesn't truncate responses
- Determine if your bug is really a bug and not a missing component in your cluster (e.g. when metrics-server is not
  deployed, the node and pod outputs won't have usage details). If you are looking for support, you might want to
  check [this section](#i-have-a-question).
- To see if other users have experienced (and potentially already solved) the same issue you are having, check if there
  is not already a bug report existing for your bug or error in
  the [bug tracker](https://github.com/bergerx/kubectl-status/issues?q=label%3Abug).
- Also make sure to search the internet (including Stack Overflow) to see if users outside the GitHub community have
  discussed the issue.
- Familiarize with the [general guidelines](#general-guidelines).
- Collect information about the bug.
- Can you reliably reproduce the issue? And, can you also reproduce it with older versions?

#### How Do I Submit a Good Bug Report?

> You must never report security related issues, vulnerabilities or bugs to the issue tracker, or elsewhere in public.
> Instead sensitive bugs must be sent by email to <bekirdo at gmail.com>.
<!-- You may add a PGP key to allow the messages to be sent encrypted as well. -->

We use GitHub issues to track bugs and errors. If you run into an issue with the project:

- Open an [Issue](https://github.com/bergerx/kubectl-status/issues/new). (Since we can't be sure at this point whether
  it is a bug or not, we ask you not to talk about a bug yet and not to label the issue.)
- Explain the behavior you would expect and the actual behavior.
- Include these versions:
    - `kubectl status --version`
    - `kubectl version -o yaml`
    - `kubectl krew version` (only if you installed using krew)
- Try to include the output with the `-v 5` flag, also try to include the un-truncated response yamls
  (`kubectl get -o yaml ...`) for individual resources as they greatly help us to understand the issue.
  Please don't forget to mask any sensitive values.
- Please provide as much context as possible and describe the *reproduction steps* that someone else can follow to
  recreate the issue on their own. This usually includes your code. For good bug reports you should isolate the problem
  and create a reduced test case.
- Provide the information you collected in the previous section.

Once it's filed:

- The project team will label the issue accordingly.
- A team member will try to reproduce the issue with your provided steps. If there are no reproduction steps or no
  obvious way to reproduce the issue, the team will ask you for those steps and mark the issue as `needs-repro`. Bugs
  with the `needs-repro` tag will not be addressed until they are reproduced.
- If the team is able to reproduce the issue, it will be marked `needs-fix`, as well as possibly other tags (such
  as `critical`), and the issue will be left to be [implemented by someone](#your-first-code-contribution).

<!-- You might want to create an issue template for bugs and errors that can be used as a guide and that defines the
     structure of the information to be included. If you do so, reference it here in the description. -->
### Suggesting Enhancements

This section guides you through submitting an enhancement suggestion for kubectl-status, **including completely new
features and minor improvements to existing functionality**. Following these guidelines will help maintainers and the
community to understand your suggestion and find related suggestions.

#### Before Submitting an Enhancement

- Make sure that you are using the latest version.
- Familiarize with the [general guidelines](#general-guidelines).
- Read the [documentation](https://github.com/bergerx/kubectl-status/blob/master/README.md) carefully and find out if
  the functionality is already covered, maybe by an individual configuration.
- Perform a [search](https://github.com/bergerx/kubectl-status/issues) to see if the enhancement has already been
  suggested. If it has, add a comment to the existing issue instead of opening a new one.
- Find out whether your idea fits with the scope and aims of the project. It's up to you to make a strong case to
  convince the project's developers of the merits of this feature. Keep in mind that we want features that will be
  useful to the majority of our users and not just a small subset. If you're just targeting a minority of users,
  consider writing an add-on/plugin library.

#### How Do I Submit a Good Enhancement Suggestion?

Enhancement suggestions are tracked as [GitHub issues](https://github.com/bergerx/kubectl-status/issues).

- Use a **clear and descriptive title** for the issue to identify the suggestion.
- Provide a **step-by-step description of the suggested enhancement** in as many details as possible.
- **Describe the current behavior** and **explain which behavior you expected to see instead** and why. At this point
  you can also tell which alternatives do not work for you.
- You may want to **include screenshots** which help you demonstrate the steps or point out the part which the
  suggestion is related to.
- **Explain why this enhancement would be useful** to most kubectl-status users. You may also want to point out the
  other projects that solved it better and which could serve as inspiration.

### Design conventions

Before writing or reviewing any template output, read [**CONVENTIONS.md**](CONVENTIONS.md). It covers output philosophy, color coding, template section order, prose style, value highlighting, and the shallow/default/deep rendering pattern.

### Your First Code Contribution

Then use `make` to get the compiled binary:

```bash
make
# the binary will be linked into the bin/ folder
bin/status pods
```

When working on a specific object, it may be easier to save the object and work on it locally:

```bash
kubectl get pod test-pod -o yaml > test-pod.yaml
# make changes on the output
make
bin/status --local -f test-pod.yaml
```

Before submitting a PR, ensure tests pass:

```bash
make test
```

### Security Checks

`make security-check` runs the two repo-wide security scans:

```bash
make security-check   # gitleaks (full git history) + govulncheck (module graph)
```

- **gitleaks** scans the whole git history for committed secrets. The synthetic secrets in
  `tests/artifacts/` are allowlisted by fingerprint in the committed `.gitleaksignore`, so only
  genuinely new findings fail the scan. When you add a fixture that trips a rule, run
  `make gitleaks-allow` to append its fingerprint(s) (fingerprints only — no secret content or
  commit metadata) and review the resulting `.gitleaksignore` diff before committing; that same
  command would silence a real leak, so don't run it reflexively on a red scan.
- **govulncheck** reports known vulnerabilities that are actually reachable from this module's
  code, so a finding usually means bumping the dependency in `go.mod`.

These are deliberately **not** part of `make test`/the pre-commit hook: both scan the repo as a
whole rather than the change in front of you, so per-commit runs cost time without telling you
anything new. They run at pre-push instead (the `make-security-check` hook in
`.pre-commit-config.yaml`, installed by the `install-pre-push-hook` hook the first time
pre-commit runs), so you find out before the push rather than from CI afterwards. CI runs the
same pair in `.github/workflows/security-checks.yml` — on every PR and push to `master`, plus
daily, since both checks can start failing without anyone touching the repo (a new entry in the
vulnerability DB, a new gitleaks rule matching something already in history).

### Claude Code Integration

The project ships a [Claude Code](https://claude.ai/code) skill and project-level settings under `.claude/`.

**`/generate-template`** — generates a kubectl-status Go template for any CRD present in your current kubectl context. Run it in Claude Code and provide the resource kind; the skill reads the CRD schema, samples live instances, and writes a ready-to-use template to `~/.kubectl-status/templates/<Kind>.tmpl` following all output and color-coding guidelines in this file.

```bash
# Inside Claude Code — example invocation:
/generate-template HTTPRoute
```

### Working with Test Artifacts

Test artifacts in `tests/artifacts/` verify template output changes. When modifying templates:

1. **Regenerate outputs** after template changes:
   ```bash
   make update-artifacts
   ```

2. **Add new test cases** when adding support for new resource types:
   ```bash
   make new-artifact CMD='-n default pod/my-pod' FILE='pod-example'
   make test
   ```

   Never make up, hand-craft, or guess `status` fields in a test artifact — a `status` subresource
   is written by the resource's controller/operator, not by a test author, and a fabricated one can
   describe a state the real controller would never produce. Get it from a real reconcile instead:
   if the controller isn't already running against the e2e cluster, deploy it during test setup
   (see the `ensureX` installer pattern under
   [Cluster Dependencies](#cluster-dependencies)), create the actual resource against a live
   minikube cluster, and capture whatever status the controller writes. This applies to both
   `tests/artifacts/` fixtures and `tests/e2e-artifacts/` manifests.

3. **Include updated artifacts in PRs** - reviewers use `.out` file diffs to verify template changes.

4. **Check the template survives a partial object.** Artifacts captured from a live cluster are
   fully populated, so they never exercise the missing-field paths — and a rendering error aborts
   the whole object, not just the offending line (see CONVENTIONS.md § Never trust a field to be
   present). Hand-write a stripped manifest with empty/absent `spec`/`metadata` fields and render
   it — this is the one place hand-authoring a manifest is fine, since it's only exercising the
   template's handling of missing input, not standing in for a controller's output. It still never
   extends to `status`: don't add a hand-written `status` block to this manifest, since that's
   exactly the fabrication the rule above forbids.

   ```bash
   cat > /tmp/partial.yaml <<'EOF'
   apiVersion: <group>/<version>
   kind: <Kind>
   metadata:
     name: bare
     namespace: default
     creationTimestamp: "2026-06-27T09:12:04Z"
   spec: {}
   EOF
   bin/status -f /tmp/partial.yaml --local --shallow
   ```

   Then add a second document dropping individual `required` sub-fields from the lists and refs
   the template renders. Any `Failed to render:` line, or a literal `<nil>` in the output, is a
   bug. Worth keeping as a `tests/artifacts/` case when the kind has many optional fields.

### Running e2e Tests Locally

`make test-e2e` runs the `TestE2E*` suite against a real cluster. That suite has three top-level
entry points — `TestE2EParallel` (`cmd/main_test.go`), which calls one topical `runXSubtests`
function per `cmd/e2e_*_test.go` file, plus `TestE2EDynamicManifests` (`cmd/e2e_dynamic_test.go`)
and `TestE2EAgainstVanillaMinikube` (`cmd/e2e_vanilla_test.go`) — sharing the harness in
`cmd/e2e_helpers_test.go` (`cmdTest`, `applyManifest`/`waitFor`, the `ensureX` dependency
installers). `cmd/local_test.go` holds the
tests that need no cluster and so run under plain `make test` instead. `make test-e2e` manages one
**shared** minikube cluster/profile (`kstat-e2e-shared`), reused across every worktree, branch, and
session on your machine — not one per branch/session. Run `make print-e2e-profile` to see the
profile name and kubeconfig path (`~/.kstat-e2e/shared.kubeconfig`) it uses. `install-e2e-deps`
runs against that same cluster right after it's created, so deps always land on the cluster the
tests actually use (see [Cluster Dependencies](#cluster-dependencies) below for what it does and
doesn't install). The cluster is left running after the tests finish, for fast reruns from any
worktree/session; delete it explicitly with `make e2e-minikube-down` when you're sure no other
worktree/session still needs it — this tears it down for everyone sharing it, not just you.

Because the cluster is shared and the e2e suite uses fixed (not generated) scratch namespace
names, two `test-e2e`/`test-e2e-quick` runs against it at the same time would collide with
"already exists" errors. Both targets guard against that with `flock` on a host-global lockfile:
a second invocation (from another worktree, another terminal, a background Claude Code task)
simply waits for the first to finish rather than racing it or getting its own cluster. This trades
some concurrency for a much smaller footprint — one 4 CPU/6 GB VM total instead of one per
worktree/branch/session, which is what actually hogs the host if left unchecked. `flock` (from
`util-linux`) is required; it's standard on Linux but not present on macOS by default.

If a run is killed ungracefully (Ctrl+C, OOM) mid-subtest, its cleanup won't run and it can leave
a stale namespace behind that collides with the next run's fixed name. Recover with
`kubectl --kubeconfig ~/.kstat-e2e/shared.kubeconfig delete ns <name>`, or nuke and recreate the
whole cluster with `make e2e-minikube-down e2e-minikube-up`.

CI instead sets `ASSUME_MINIKUBE_IS_CONFIGURED=true`, which makes `make test-e2e` skip all of the
above and use whatever cluster your current kubeconfig context already points at (that's what
`medyagh/setup-minikube` in `ci-test.yml` provisions) — set the same var locally if you'd rather
manage the cluster yourself.

Note: `TestE2E*` functions invoked directly via `go test -run TestE2E...` (bypassing the
Makefile) fall back to using the bare test function name as the minikube profile if `E2E_PROFILE`
isn't set in the environment, which starts (and leaks) a one-off cluster instead of using the
shared one — export `E2E_PROFILE` yourself (e.g. from `make print-e2e-profile`) to land on the
shared cluster too.

#### Cluster Dependencies

metrics-server is the only dependency installed upfront, by the `install-e2e-deps` target — see
the comment there for why it has to be an invariant of the whole run rather than one group's
concern. Everything else (cert-manager, Gateway API CRDs, Cilium/Calico CRDs, VolumeSnapshot CRDs,
Karpenter CRDs, Istio CRDs, VPA, Crossplane, Flux) is installed on demand by the test that needs it,
so a cluster only ever grows the dependencies the suite actually exercises. No manual setup either
way.

Note that "on demand" tracks the *entry point*, not your `-run` pattern. The `runXSubtests`
functions are plain calls, and `-run` filters at `t.Run` below them, so any
`RUN='TestE2EParallel/...'` still runs every group's install regardless of which subtests survive
the filter — cert-manager, Gateway API CRDs and Cilium/Calico CRDs go in even for a pattern that
matches nothing. On a warm cluster that's a few seconds and not worth restructuring for; on a fresh
one it's the bulk of a narrow run's wall clock. `TestE2EDynamicManifests` doesn't have this
property: its `ensureX` calls sit inside the subtest bodies, so filtering does skip them.
`runFluxSubtests` is the one pool group that does the same, for the same reason — `ensureFlux` is
the suite's heaviest install, so it sits inside the `t.Run` rather than at the top of the group,
where it would go in on any `TestE2EParallel` run whose pattern doesn't even reach that subtest.

The installers are the `ensureX(t)` functions in `cmd/e2e_helpers_test.go`; keep new ones there
rather than inline in a test file. A topical group in `TestE2EParallel` calls the ones it needs at
the top of its `runXSubtests` function; under `TestE2EDynamicManifests`, where a dependency usually
serves a single scenario, the call goes at the top of that subtest instead — including when the
scenario lives in its own file behind a `runXSubtests` function. Most install CRDs only —
kubectl-status reads and matches these objects client-side, so no real controller is needed;
`ensureVPA`/`ensureCrossplane`/`ensureFlux` are the exceptions, where the test asserts on state
only a running controller writes.

Each installer is a package-level `onceInstaller` (`sync.Once` plus a cached error), so a
dependency two groups share installs exactly once per run, and the underlying `kubectl apply
--server-side`/`helm upgrade --install` commands are idempotent against the shared cluster across
reruns. Subtests run in parallel; installs never do — a package-level mutex serializes every
install against every other, so a cold run brings dependencies up one at a time instead of putting
several controller rollouts on the cluster at once. That costs nothing on a warm cluster, where
re-running every installer totals well under a minute.

When adding one, keep its install closure free of `t`/`require` and return a plain `error`:
`sync.Once` marks itself done even when its function exits via `runtime.Goexit` (which `require`
uses), so a `t`-based failure inside would leave every later caller believing the install
succeeded. `onceInstaller.ensure` replays the cached error to all of them instead, and keeps the
assertion outside the lock so a stray `require` can't strand it.

Versions are pinned in `hack/versions.env`, shared with `hack/generate-screenshots.sh` so both stay
on the same releases — bump them there periodically to track upstream stable releases. CI uses the
same `make test-e2e` target, so it stays in sync automatically.

#### Fast Iteration: `make test-e2e-quick`

`make test-e2e` reruns `vet`/`staticcheck`/`install-e2e-deps` and the whole ~60-subtest `TestE2E*`
pattern every time, which is wasteful while iterating on a single scenario. Once you have a cluster
up (`make e2e-minikube-up install-e2e-deps`, one time — it's left running afterwards, see above),
use `make test-e2e-quick RUN='<pattern>'` to rerun just the subtest(s) you're working on against
that same cluster, skipping the lint steps and the deps install:

```bash
make e2e-minikube-up install-e2e-deps   # once per shared cluster
make test-e2e-quick RUN='TestE2EParallel/podscheduling'
```

`TestE2EParallel`'s subtests are grouped topically by the `runXSubtests` functions in
`cmd/e2e_*_test.go` (e.g. `runPodSchedulingSubtests`, `runNetworkPolicySubtests`) — match the
`-run` pattern to whichever group (or specific subtest name within it) covers the template/package
you touched, rather than rerunning the full pattern. This is also the right tool for Claude Code to
verify an e2e-covered change before committing: run the narrowest `RUN` pattern that covers the
touched scenario instead of the full suite, so a dev-loop check doesn't tie up the shared cluster
or the machine's resources for longer than necessary. `make test-e2e` (the full suite) stays the
pre-push/CI gate — the targeted `test-e2e-quick` checks above are what a dev-loop/commit-time
check should use, not the full suite.

When a template change adds or touches `$.KubeGetFirst`, `$.IncludeRenderableObject`/`$.Include`,
or any other interaction with a live cluster, add or extend a live e2e subtest plus matching
manifests/regex fixtures under `tests/e2e-artifacts/`. The offline golden-file tests
(`TestAllArtifactsLocal*`) run with `--shallow` (alongside `--local`, since there's no live cluster
to query either way), which makes `KubeGetFirst` a no-op — they can't exercise the "found the
related object" or `--deep` include branches, so the live e2e suite is the only place that covers
them.

**Put new subtests in `TestE2EParallel`'s pool by default.** `TestE2EDynamicManifests` is the
exception, and a subtest only earns a place in it by tripping one of the two criteria in
`TestE2EParallel`'s doc comment (`cmd/main_test.go`) — with the reason written on the subtest, so
the next person inherits a rule rather than a precedent. The qualifying reasons are all about what
a subtest does *to* its neighbours: it perturbs cluster-wide state they read (deleting the
metrics-server `APIService`), it starves a shared dependency (the VPA subtest pegs a full CPU and
takes metrics-server's readiness probe down with it on a single-node cluster), or it can't be given
a namespace/generated name of its own.

Several things look disqualifying but aren't, and shouldn't be used to justify a serial subtest:

- **Needing a live-cluster query.** Most of the pool needs one; that's the normal case, not an
  exception.
- **Installing a real controller and waiting for it to reconcile.** The `ensureX` installers are
  shared `onceInstaller`s serialized by `installMu`, so calling one from inside a `t.Parallel()`
  subtest is safe — `ensureFlux` does exactly that.
- **Depending on metrics.** Fine as long as the subtest *consumes* metrics rather than threatening
  them, and its fixtures pin values that are a function of its own workload rather than of cluster
  load. Relative timestamps aren't a hazard either: `ApplyTestHack` freezes `DurationRound`, so
  `created 1m ago` is a constant, not elapsed time.
- **How the objects come into being** (built in Go, applied from static YAML, or patched in place).
  This distinguishes nothing, whatever the `DynamicManifests` name suggests.

See #784: the Flux scenario was a standalone top-level test, then a serial subtest, before anyone
checked it against the criteria — which it met, so it's a pool group now. Don't add a fourth
top-level `TestE2E*` function either; a scenario long enough to crowd its host file can live in its
own `cmd/e2e_*_test.go` behind a `runXSubtests` function, the way `runFluxSubtests` does. The file a
subtest sits in and the entry point it runs under are separate choices.

Assert on stdout with a whole-output `.regex` fixture (`stdoutRegexPath`, or
`assertStdoutMatchesRegexFixture` when the subtest also needs to assert something the fixture can't
express) rather than ad hoc `assert.Contains`/`assert.Regexp` calls scattered through the subtest.
A single regex covering the full rendered output, matched with `\A`...`\z` anchors
(see the existing `tests/e2e-artifacts/*.regex` files for the pattern), lets a reviewer diff the
expected output like a golden `.out` file and catches regressions anywhere in the render, not just
in the specific substrings an inline assertion happens to check. Use `[0-9.]+[A-Za-z]+` and similar
tolerant patterns for the non-deterministic parts (log/volume usage bytes, backoff windows, restart
counts, container/pod UIDs); `viperTestHack(t)`/`testHack(t)` freeze relative durations to `1m` so
those can stay literal. Prefer real line breaks over literal `\n` escapes when a pattern needs to
match across multiple output lines (e.g. inside a `(?:...|...)` alternation) — a raw newline in the
fixture file matches a newline in the output just as well and keeps the file readable as plain
text. One gotcha: the regex file must not have a trailing newline after the final `\z` — `\z`
asserts absolute end-of-text, so a trailing newline in the fixture file makes the pattern
impossible to satisfy.

Every fixture is a whole-output match, anchored at both ends with `\A`...`\z` — partial fixtures
that pin a line or two out of the middle of the render aren't allowed. A partial fixture silently
stops verifying everything outside what it happens to match, and reads to the next person like
"this is the full output" when it isn't. `TestE2ERegexFixturesAreAnchored` in `cmd/local_test.go`
enforces both anchors across every fixture under `tests/e2e-artifacts/`; it doesn't need a live
cluster, so it also runs in `make test`/CI. If some part of an object's output is noisy or not
worth pinning, don't drop to a partial fixture to dodge it — trim that section from the render
instead with the relevant `--include-*` flag (`--include-events=false`,
`--include-managed-fields=false`, etc.), or match it with a tolerant pattern, and keep the fixture
anchored and whole.

Pass `--include-events=false --include-managed-fields=false` on every e2e `cmdTest` unless the
subtest is specifically exercising events or managed fields. Both sections list real cluster data
whose relative order isn't guaranteed to be stable across runs — Events by nature, and
`managedFields` because two updates that land in the same wall-clock second sort as ties (`recent_updates`
in `pkg/plugin/templates/common.tmpl` sorts by `time`, which only has 1-second resolution) — so
leaving them enabled makes an otherwise-pinned fixture flaky. Real-usage default for both flags is
`true`; the tests opt out per-invocation rather than the CLI changing its default.

Similarly, the status summary line's `, started after <duration>` clause (also in `common.tmpl`)
measures live scheduling latency between a Pod's `creationTimestamp` and `status.startTime` — both
1-second-resolution timestamps, so on a real cluster whether the clause clears the "at least 1s
apart" threshold, and therefore whether it renders at all, is a coin flip e2e tests can't control.
`testHackOpts(t)` overrides `plugin.RenderConfig.StartedAfterClause` to force the clause present (as
`, started after 1m`) whenever `status.startTime` is set, so fixtures can pin it as a literal instead
of wrapping it in an optional group.

### Parallel-Safe e2e Subtests

`make test-e2e` currently runs `TestE2E*` as one sequential `go test` invocation.
`TestE2EParallel` (`cmd/main_test.go`) is a dedicated home for subtests that are independent enough
to run concurrently instead -- see the doc comment on that function for exactly what a subtest needs
(a dedicated namespace or none at all, no shared cluster-scoped resource names) before it can move
there with `t.Run(name, func(t *testing.T) { t.Parallel(); ... })`. `RootCmd`/`pkg/plugin` no longer
depend on a global `viper` singleton or package-level `Now`/`DurationRound`/`StartedAfterClause`
overrides (each `RootCmd()` call owns its own `*viper.Viper`/`plugin.RenderConfig`, see #694), so
`testHackOpts`/`viperTestHackOpts` are safe to use from concurrent subtests too. Two process-global
sinks still remain on the render path, though: `cmdutil.BehaviorOnFatal` in `RootCmd`'s `RunE`
(installs a global fatal handler capturing that invocation's `err`) and `slog.SetDefault` in
`newRenderEngine`'s `setupDeprecationFilter` (rebinds the global slog default per render). A
subtest touching either of those isn't parallel-safe yet -- moving one means checking both its
Kubernetes-side isolation and these two.

### Improving The Documentation

We don't yet have a comprehensive documentation, we maintain just a few Markdown files in the repo. We aim to keep the
examples in the [README.md](README.md#demo) up-to-date as we add new features, but this process is not automated.

## Styleguides

### Commit Messages

We don't yet have a convention for commit messages.

## Releasing a new version

Pushing a git tag will trigger
[goreleaser GitHub action](https://github.com/bergerx/kubectl-status/actions/workflows/release.yml)
to build and publish a new release to krew index.

```bash
git tag vX.X.X
git push --tags
```

## Attribution

This guide is based on the **contributing-gen**. [Make your own](https://github.com/bttger/contributing-gen)!
