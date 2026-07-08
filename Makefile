
export GO111MODULE=on

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
# is kept as a readable prefix; a hash carries the actual uniqueness so truncating
# long/ULID-suffixed branch names (e.g. worktree-*-<ulid>) can't collide.
E2E_GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
ifeq ($(E2E_GIT_BRANCH),HEAD)
E2E_GIT_BRANCH := $(shell git rev-parse --short HEAD 2>/dev/null)
endif
ifeq ($(E2E_GIT_BRANCH),)
E2E_GIT_BRANCH := local
endif
E2E_BRANCH_SLUG := $(shell ./hack/e2e-branch-slug.sh '$(E2E_GIT_BRANCH)')
E2E_IDENTITY_HASH := $(shell printf '%s' '$(E2E_GIT_BRANCH):$(CLAUDE_CODE_SESSION_ID)' | (sha1sum 2>/dev/null || shasum) | cut -c1-8)
E2E_PROFILE := kstat-e2e-$(E2E_BRANCH_SLUG)-$(E2E_IDENTITY_HASH)
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
	$(E2E_KUBECONFIG_ENV) minikube start -p $(E2E_PROFILE) --addons=metrics-server

.PHONY: e2e-minikube-down
e2e-minikube-down:
	$(E2E_KUBECONFIG_ENV) minikube delete -p $(E2E_PROFILE)
	@rm -f $(E2E_KUBECONFIG)

.PHONY: install-e2e-deps
install-e2e-deps:
	# metrics-server is needed by e2e scenarios exercising pod/node metrics rendering.
	$(E2E_KUBECONFIG_ENV) minikube addons enable metrics-server $(E2E_PROFILE_FLAG)
	$(E2E_KUBECONFIG_ENV) kubectl -n kube-system rollout status deployment/metrics-server --timeout=120s
	# cert-manager and Gateway API CRDs are needed by e2e TLS-validation test scenarios.
	# Pinned to latest stable at time of writing; bump these tags periodically.
	$(E2E_KUBECONFIG_ENV) kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.20.3/cert-manager.yaml
	$(E2E_KUBECONFIG_ENV) kubectl wait --for=condition=Available --timeout=300s deployment --all -n cert-manager
	# CRDs only (no controller needed): e2e tests only exercise kubectl-status's own
	# read-only rendering of these objects, they don't need a controller reconciling them.
	# Experimental channel is a superset of standard and adds TCPRoute/UDPRoute/
	# BackendTLSPolicy/ListenerSet, which some e2e scenarios also render.
	# --server-side: the experimental bundle's CRDs (e.g. HTTPRoute) are large enough that
	# client-side apply's kubectl.kubernetes.io/last-applied-configuration annotation trips
	# the 262144-byte annotation limit; server-side apply doesn't need that annotation.
	$(E2E_KUBECONFIG_ENV) kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.0/experimental-install.yaml

.PHONY: test-e2e
ifeq ($(ASSUME_MINIKUBE_IS_CONFIGURED),true)
test-e2e: vet staticcheck install-e2e-deps
	# using count to prevent caching
	RUN_E2E_TESTS=true ASSUME_MINIKUBE_IS_CONFIGURED=true go test -v ./... -count=1 -run 'TestE2E*'
else
test-e2e: vet staticcheck e2e-minikube-up install-e2e-deps
	# The cluster is left running (profile: $(E2E_PROFILE)) for fast reruns; tear it
	# down explicitly with `make e2e-minikube-down` once you're done with this branch.
	# using count to prevent caching
	$(E2E_KUBECONFIG_ENV) RUN_E2E_TESTS=true ASSUME_MINIKUBE_IS_CONFIGURED=true go test -v ./... -count=1 -run 'TestE2E*'
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
	# disposable/dev cluster (e.g. minikube) and freeze (charmbracelet/freeze) on PATH.
	./hack/generate-screenshots.sh
