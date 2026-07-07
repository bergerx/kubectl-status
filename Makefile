
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

.PHONY: install-e2e-deps
install-e2e-deps:
	# metrics-server is needed by e2e scenarios exercising pod/node metrics rendering.
	minikube addons enable metrics-server
	kubectl -n kube-system rollout status deployment/metrics-server --timeout=120s
	# cert-manager and Gateway API CRDs are needed by e2e TLS-validation test scenarios.
	# Pinned to latest stable at time of writing; bump these tags periodically.
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.20.3/cert-manager.yaml
	kubectl wait --for=condition=Available --timeout=180s deployment --all -n cert-manager
	# CRDs only (standard channel, no controller needed): e2e tests only exercise
	# kubectl-status's own read-only rendering of Gateway/HTTPRoute objects, they don't
	# need a controller reconciling them.
	kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.0/standard-install.yaml

.PHONY: test-e2e
test-e2e: vet staticcheck install-e2e-deps
	# using count to prevent caching
	RUN_E2E_TESTS=true go test -v ./... -count=1 -run 'TestE2E*'

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
