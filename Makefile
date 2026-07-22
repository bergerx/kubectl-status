
export GO111MODULE=on

include hack/versions.env

.DEFAULT_GOAL := bin

#--------------------------
# Build
#--------------------------
.PHONY: bin
bin: fmt vet staticcheck
	mkdir -p bin
	goreleaser build --single-target --skip=validate --clean
	@suffix=$$(go env GO$$(echo $$(go env GOARCH) | tr '[:lower:]' '[:upper:]' 2>/dev/null)); \
	if [ -n "$$suffix" ]; then suffix="_$$suffix"; fi; \
	ln -Ffs ../dist/status_$$(go env GOOS)_$$(go env GOARCH)$$suffix/status bin/

.PHONY: clean
clean:
	@rm -fv bin/status
	@rm -fvr dist

#--------------------------
# Code Quality
#--------------------------
.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: staticcheck
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@v0.6.1 ./...

#--------------------------
# Test
#--------------------------
.PHONY: test
test: vet staticcheck
	go test ./...

#--------------------------
# E2E cluster identity
#--------------------------
# All local test-e2e/test-e2e-quick runs share one minikube profile, across every
# worktree/branch/session on this machine -- a profile per branch/session (the
# previous scheme) meant every worktree you'd touched left its own 4 CPU/6 GB VM
# running, which piles up fast and starves the host. Trade-off: since the profile
# is shared, concurrent runs (two worktrees, two Claude Code sessions, a background
# task) must be serialized -- see the `flock $(E2E_LOCKFILE)` in test-e2e/
# test-e2e-quick below, which makes a second invocation wait for the first instead
# of racing it (the e2e suite uses fixed, not generated, scratch namespace names,
# so two concurrent runs would otherwise collide with "already exists" errors).
# E2E_HOME/E2E_KUBECONFIG/E2E_LOCKFILE are deliberately host-global ($(HOME)-based),
# not $(CURDIR)-relative -- each worktree has a different CURDIR, and sharing
# requires them to all agree on the same paths regardless of which one invokes make.
E2E_PROFILE := kstat-e2e-shared
E2E_HOME := $(HOME)/.kstat-e2e
E2E_KUBECONFIG := $(E2E_HOME)/shared.kubeconfig
E2E_LOCKFILE := $(E2E_HOME)/shared.lock

# CI (and anyone else who already has a suitable cluster configured) sets
# ASSUME_MINIKUBE_IS_CONFIGURED=true, in which case we deliberately fall back to the
# ambient kubeconfig/context and default minikube profile instead of the isolated
# ones above -- that's what medyagh/setup-minikube in ci-test.yml provisions.
ifeq ($(ASSUME_MINIKUBE_IS_CONFIGURED),true)
E2E_KUBECONFIG_ENV :=
E2E_PROFILE_FLAG :=
else
E2E_KUBECONFIG_ENV := KUBECONFIG=$(E2E_KUBECONFIG)
E2E_PROFILE_FLAG := -p $(E2E_PROFILE)
endif

.PHONY: print-e2e-profile
print-e2e-profile:
	@echo "profile:   $(E2E_PROFILE)"
	@echo "kubeconfig: $(E2E_KUBECONFIG)"

.PHONY: reap
reap:
	./hack/reap-worktrees.sh

.PHONY: reap-apply
reap-apply:
	./hack/reap-worktrees.sh --apply

.PHONY: install-hooks
install-hooks:
	@hooks_dir="$$(git rev-parse --git-path hooks)"; \
	mkdir -p "$$hooks_dir"; \
	install -m 755 hack/git-hooks/reference-transaction "$$hooks_dir/reference-transaction"; \
	echo "Installed hack/git-hooks/reference-transaction -> $$hooks_dir/reference-transaction"; \
	echo "(shared by all worktrees of this clone; deletes a branch's e2e minikube profile when the branch is deleted)"

.PHONY: e2e-minikube-up
e2e-minikube-up:
	@mkdir -p $(E2E_HOME)
	# Reuse the shared cluster if it's already up instead of wiping it first (the old
	# per-branch profile could safely delete-first since nothing else depended on it;
	# this one is shared across worktrees/sessions, so deleting it here could yank it
	# out from under another session's run).
	@if $(E2E_KUBECONFIG_ENV) minikube status -p $(E2E_PROFILE) >/dev/null 2>&1; then \
		echo "Shared e2e cluster '$(E2E_PROFILE)' already running, reusing it."; \
	else \
		echo "$(E2E_KUBECONFIG_ENV) minikube start -p $(E2E_PROFILE) --addons=metrics-server --cpus=4 --memory=6g"; \
		$(E2E_KUBECONFIG_ENV) minikube start -p $(E2E_PROFILE) --addons=metrics-server --cpus=4 --memory=6g; \
	fi
	# --cpus/--memory above: TestE2EParallel's subtests run with -parallel=4 (see test-e2e
	# below), each doing real cluster work (pod scheduling, image pulls, rollouts)
	# concurrently -- minikube's own defaults are sized for serial usage and get
	# overwhelmed (widespread `kubectl wait` timeouts) under that load.

.PHONY: e2e-minikube-down
e2e-minikube-down:
	# Tears down the cluster every worktree/session on this machine shares -- only run
	# this when you're sure nobody else (another worktree, another Claude Code session)
	# still needs it. `flock`ed like test-e2e/test-e2e-quick so it can't race a live run.
	$(E2E_KUBECONFIG_ENV) flock $(E2E_LOCKFILE) minikube delete -p $(E2E_PROFILE)
	@rm -f $(E2E_KUBECONFIG)

.PHONY: install-e2e-deps
install-e2e-deps:
	# metrics-server is the one cluster dependency that stays here as a global, upfront install
	# rather than moving into its topical e2e test group (see cmd/e2e_helpers_test.go's
	# ensure*(t) functions for cert-manager, Gateway API CRDs, Cilium/Calico CRDs, VPA, and
	# Crossplane -- #720): pdb-empty-selector-conflict, not itself a metrics test, can render a
	# spurious "metrics-server is not available" line if the metrics API isn't queryable yet
	# when TestE2EParallel's parallel pool starts, so metrics availability is an invariant for
	# the whole pool, not a per-group concern.
	$(E2E_KUBECONFIG_ENV) minikube addons enable metrics-server $(E2E_PROFILE_FLAG)
	$(E2E_KUBECONFIG_ENV) kubectl -n kube-system rollout status deployment/metrics-server --timeout=120s
	# The Deployment/Pod going Ready above can still briefly precede the Service's EndpointSlice
	# getting its addresses -- a subtest that happens to run first in TestE2EParallel's pool (e.g.
	# pdb-empty-selector-conflict) can otherwise race that gap and render a spurious
	# "metrics-server is not available" line. Poll the actual data path instead of the rollout.
	$(E2E_KUBECONFIG_ENV) bash -c 'for ((i=1; i<=60; i++)); do kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes >/dev/null 2>&1 && exit 0; sleep 2; done; echo "metrics.k8s.io never became queryable" >&2; exit 1'

.PHONY: test-e2e
ifeq ($(ASSUME_MINIKUBE_IS_CONFIGURED),true)
test-e2e: vet staticcheck install-e2e-deps
	# using count to prevent caching; the suite's real cluster wall-clock time (image pulls,
	# rollouts, waits) runs close to go test's default 10m timeout, so it's raised explicitly
	# rather than risking a flaky timeout on a slower run.
	# gotestsum runs go test with -v under the hood (so full per-subtest logs are still
	# captured) but only prints them for failing tests, collapsing a green run to one line
	# per package (default --format=pkgname) -- the ~60 fixture/scenario subtests in
	# cmd/main_test.go otherwise flood the terminal with "=== RUN"/"--- PASS" and t.Logf
	# noise on every green run.
	# -parallel=4: bounds how many TestE2EParallel subtests hit the cluster at once. Go's
	# default (GOMAXPROCS, i.e. host core count) can far exceed what the e2e-minikube-up VM
	# above is sized for, causing widespread `kubectl wait` timeouts instead of a speedup.
	# flock: harmless here (CI runs one job at a time anyway) but keeps this branch
	# consistent with the shared-cluster branch below.
	RUN_E2E_TESTS=true ASSUME_MINIKUBE_IS_CONFIGURED=true flock $(E2E_LOCKFILE) go run gotest.tools/gotestsum@v1.13.0 -- ./... -count=1 -timeout=25m -parallel=4 -run 'TestE2E*'
else
test-e2e: vet staticcheck e2e-minikube-up install-e2e-deps
	# The cluster (profile: $(E2E_PROFILE)) is shared across every worktree/branch/session on
	# this machine and is left running afterwards -- run `make e2e-minikube-down` yourself when
	# you're sure nothing else still needs it. `flock $(E2E_LOCKFILE)` serializes this against
	# any other test-e2e/test-e2e-quick run on the machine (the suite uses fixed scratch
	# namespace names, so two concurrent runs would otherwise collide).
	# using count to prevent caching; see the timeout note in the ASSUME_MINIKUBE_IS_CONFIGURED
	# branch above.
	# See the gotestsum note, and the -parallel=4 note above the other branch's go test invocation.
	$(E2E_KUBECONFIG_ENV) RUN_E2E_TESTS=true ASSUME_MINIKUBE_IS_CONFIGURED=true flock $(E2E_LOCKFILE) go run gotest.tools/gotestsum@v1.13.0 -- ./... -count=1 -timeout=25m -parallel=4 -run 'TestE2E*'
endif

.PHONY: test-e2e-quick
test-e2e-quick:
	@if [ -z "$(RUN)" ]; then \
		echo "Usage: make test-e2e-quick RUN='<go test -run pattern>'"; \
		echo "Example: make test-e2e-quick RUN='TestE2EParallel/podscheduling'"; \
		exit 1; \
	fi
	@if [ "$(ASSUME_MINIKUBE_IS_CONFIGURED)" != "true" ] && [ ! -f "$(E2E_KUBECONFIG)" ]; then \
		echo "No shared e2e cluster found ($(E2E_KUBECONFIG))."; \
		echo "Run 'make e2e-minikube-up install-e2e-deps' once first, then reuse it with test-e2e-quick."; \
		exit 1; \
	fi
	# Skips vet/staticcheck/install-e2e-deps and the minikube up/down that test-e2e does --
	# for iterating on a single scenario against the shared cluster you already brought up
	# (and are leaving up) with e2e-minikube-up/install-e2e-deps. Same -parallel=4 as test-e2e:
	# sized for the e2e-minikube-up VM (see that target's comment), not worth changing for a
	# narrower -run since it's still the same cluster taking the load. flock $(E2E_LOCKFILE):
	# see the comment on the shared-cluster branch of test-e2e above.
	$(E2E_KUBECONFIG_ENV) RUN_E2E_TESTS=true ASSUME_MINIKUBE_IS_CONFIGURED=true flock $(E2E_LOCKFILE) go run gotest.tools/gotestsum@v1.13.0 -- ./... -count=1 -timeout=10m -parallel=4 -run '$(RUN)'

#--------------------------
# Test Artifacts
#--------------------------
.PHONY: update-artifacts
update-artifacts:
	@for yaml in ./tests/artifacts/*.yaml; do \
		out="$$(echo $${yaml} | sed 's/.yaml/.out/')"; \
		echo "$${yaml} --> $${out}"; \
		go run ./cmd --test-hack -f "$${yaml}" --local --shallow > "$${out}"; \
	done

.PHONY: new-artifact
new-artifact:
	@if [ -z "$(CMD)" ] || [ -z "$(FILE)" ]; then \
		echo "Usage: make new-artifact CMD='<kubectl get args>' FILE='<artifact-name>'"; \
		echo "Example: make new-artifact CMD='-n default node,service' FILE='node-and-service'"; \
		exit 1; \
	fi
	kubectl get -o yaml $(CMD) > "tests/artifacts/$(FILE).yaml"
	go run ./cmd --test-hack $(CMD) --shallow > "tests/artifacts/$(FILE).out"
	@echo "Created: tests/artifacts/$(FILE).yaml tests/artifacts/$(FILE).out"
	@echo "Run 'make test' then 'git add' the new files"

#--------------------------
# Demo Screenshots
#--------------------------
.PHONY: screenshots
screenshots:
	# Regenerates assets/*.png from a live cluster. Requires kubectl pointed at a
	# disposable/dev cluster (e.g. minikube).
	./hack/generate-screenshots.sh
