
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
# Local test-e2e runs get their own minikube profile/kubeconfig, isolated by git
# branch (worktrees can't share a branch, so this covers parallel worktrees) and,
# when running under Claude Code, by session id too (so two sessions working the
# same branch/worktree don't step on each other's cluster either). The branch name
# is kept as a readable prefix (cosmetic only, safe to truncate). Uniqueness comes
# from two hashes: BRANCH_HASH is a function of the branch name alone, so the
# reference-transaction hook -- which only ever sees a deleted branch name, never a
# session id -- can recompute it and match *only* that branch's profiles, even when
# several branches share a truncated slug (e.g. worktree-*-<ulid> branches all
# collapse to the same 20-char prefix). SESSION_HASH additionally folds in the
# session id so parallel sessions on the same branch still get separate clusters.
E2E_GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
ifeq ($(E2E_GIT_BRANCH),HEAD)
E2E_GIT_BRANCH := $(shell git rev-parse --short HEAD 2>/dev/null)
endif
ifeq ($(E2E_GIT_BRANCH),)
E2E_GIT_BRANCH := local
endif
E2E_BRANCH_SLUG := $(shell ./hack/e2e-branch-slug.sh '$(E2E_GIT_BRANCH)')
E2E_BRANCH_HASH := $(shell ./hack/e2e-hash.sh '$(E2E_GIT_BRANCH)')
E2E_SESSION_HASH := $(shell ./hack/e2e-hash.sh '$(E2E_GIT_BRANCH):$(CLAUDE_CODE_SESSION_ID)')
E2E_PROFILE := kstat-e2e-$(E2E_BRANCH_SLUG)-$(E2E_BRANCH_HASH)-$(E2E_SESSION_HASH)
E2E_KUBECONFIG := $(CURDIR)/.e2e/$(E2E_PROFILE).kubeconfig

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
	@mkdir -p $(dir $(E2E_KUBECONFIG))
	# Wipe any previous cluster for this profile first: reusing one leaks resources from a
	# prior run (e.g. killed mid-suite) into this run, causing spurious "already exists"
	# failures and cluster-load-related flakiness unrelated to the code under test.
	-$(E2E_KUBECONFIG_ENV) minikube delete -p $(E2E_PROFILE)
	# --cpus/--memory: TestE2EParallel's subtests run with -parallel=4 (see test-e2e below),
	# each doing real cluster work (pod scheduling, image pulls, rollouts) concurrently --
	# minikube's own defaults are sized for serial usage and get overwhelmed (widespread
	# `kubectl wait` timeouts) under that load.
	$(E2E_KUBECONFIG_ENV) minikube start -p $(E2E_PROFILE) --addons=metrics-server --cpus=4 --memory=6g

.PHONY: e2e-minikube-down
e2e-minikube-down:
	$(E2E_KUBECONFIG_ENV) minikube delete -p $(E2E_PROFILE)
	@rm -f $(E2E_KUBECONFIG)

.PHONY: install-e2e-deps
install-e2e-deps:
	# metrics-server is needed by e2e scenarios exercising pod/node metrics rendering.
	$(E2E_KUBECONFIG_ENV) minikube addons enable metrics-server $(E2E_PROFILE_FLAG)
	$(E2E_KUBECONFIG_ENV) kubectl -n kube-system rollout status deployment/metrics-server --timeout=120s
	# The Deployment/Pod going Ready above can still briefly precede the Service's EndpointSlice
	# getting its addresses -- a subtest that happens to run first in TestE2EParallel's pool (e.g.
	# pdb-empty-selector-conflict) can otherwise race that gap and render a spurious
	# "metrics-server is not available" line. Poll the actual data path instead of the rollout.
	$(E2E_KUBECONFIG_ENV) bash -c 'for i in $$(seq 1 60); do kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes >/dev/null 2>&1 && exit 0; sleep 2; done; echo "metrics.k8s.io never became queryable" >&2; exit 1'
	# cert-manager and Gateway API CRDs are needed by e2e TLS-validation test scenarios.
	# Versions are pinned in hack/versions.env (shared with hack/generate-screenshots.sh);
	# bump them there periodically.
	$(E2E_KUBECONFIG_ENV) kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml
	$(E2E_KUBECONFIG_ENV) kubectl wait --for=condition=Available --timeout=300s deployment --all -n cert-manager
	# CRDs only (no controller needed): e2e tests only exercise kubectl-status's own
	# read-only rendering of these objects, they don't need a controller reconciling them.
	# Experimental channel is a superset of standard and adds TCPRoute/UDPRoute/
	# BackendTLSPolicy/ListenerSet, which some e2e scenarios also render.
	# --server-side: the experimental bundle's CRDs (e.g. HTTPRoute) are large enough that
	# client-side apply's kubectl.kubernetes.io/last-applied-configuration annotation trips
	# the 262144-byte annotation limit; server-side apply doesn't need that annotation.
	$(E2E_KUBECONFIG_ENV) kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/experimental-install.yaml
	# CiliumNetworkPolicy/CiliumClusterwideNetworkPolicy and Calico NetworkPolicy/
	# GlobalNetworkPolicy CRDs: kubectl-status only reads and matches these objects
	# client-side (selector-vs-Pod-labels), it never relies on Cilium/Calico actually
	# enforcing traffic, so the CRDs alone (no Cilium/Calico installed as CNI) are enough
	# to exercise the e2e scenarios -- same "CRDs only" reasoning as cert-manager/Gateway
	# API above. Calico's own NetworkPolicy/GlobalNetworkPolicy are served under
	# crd.projectcalico.org/v1 (the Kubernetes-datastore storage CRDs), not the
	# projectcalico.org/v3 API calicoctl/the Calico API server present -- that's the group
	# kubectl-status's KubeGetCalico*MatchingPod helpers query.
	# --server-side: these CRDs' embedded OpenAPI schemas are large enough to trip the same
	# client-side last-applied-configuration annotation limit as HTTPRoute above.
	$(E2E_KUBECONFIG_ENV) kubectl apply --server-side -f https://raw.githubusercontent.com/cilium/cilium/v1.19.5/pkg/k8s/apis/cilium.io/client/crds/v2/ciliumnetworkpolicies.yaml
	$(E2E_KUBECONFIG_ENV) kubectl apply --server-side -f https://raw.githubusercontent.com/cilium/cilium/v1.19.5/pkg/k8s/apis/cilium.io/client/crds/v2/ciliumclusterwidenetworkpolicies.yaml
	$(E2E_KUBECONFIG_ENV) kubectl apply --server-side -f https://raw.githubusercontent.com/projectcalico/calico/v3.32.1/manifests/crds.yaml
	# VerticalPodAutoscaler: unlike the CRD-only entries above, e2e scenarios exercise it
	# actually acting (the updater evicting/recreating a Pod to apply a recommendation), so its
	# controllers (recommender/updater/admission-controller) need to run for real, not just the
	# CRDs. The upstream project has no plain `kubectl apply` release bundle (its install script
	# generates webhook certs locally), so this uses the cowboysysop community Helm chart instead.
	helm repo add cowboysysop https://cowboysysop.github.io/charts/
	helm repo update cowboysysop
	$(E2E_KUBECONFIG_ENV) helm upgrade --install vpa cowboysysop/vertical-pod-autoscaler --version 11.1.1 -n kube-system --wait --timeout 5m
	$(E2E_KUBECONFIG_ENV) kubectl wait --for=condition=Available --timeout=120s deployment -l app.kubernetes.io/instance=vpa -n kube-system

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
	RUN_E2E_TESTS=true ASSUME_MINIKUBE_IS_CONFIGURED=true go run gotest.tools/gotestsum@v1.13.0 -- ./... -count=1 -timeout=25m -parallel=4 -run 'TestE2E*'
else
test-e2e: vet staticcheck e2e-minikube-up install-e2e-deps
	# The isolated cluster (profile: $(E2E_PROFILE)) is torn down afterwards whether the suite
	# passes or fails, so local/pre-push runs don't leak minikube profiles. Its exit status is
	# preserved so a failing suite still fails the target (and blocks the push).
	# using count to prevent caching; see the timeout note in the ASSUME_MINIKUBE_IS_CONFIGURED
	# branch above.
	# See the gotestsum note, and the -parallel=4 note above the other branch's go test invocation.
	$(E2E_KUBECONFIG_ENV) RUN_E2E_TESTS=true ASSUME_MINIKUBE_IS_CONFIGURED=true go run gotest.tools/gotestsum@v1.13.0 -- ./... -count=1 -timeout=25m -parallel=4 -run 'TestE2E*'; \
	status=$$?; \
	$(MAKE) e2e-minikube-down; \
	exit $$status
endif

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
