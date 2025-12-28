
export GO111MODULE=on

.DEFAULT_GOAL := bin

#--------------------------
# Build
#--------------------------
.PHONY: bin
bin: fmt vet staticcheck
	mkdir -p bin
	goreleaser build --single-target --skip=validate --clean
	ln -Ffs ../dist/status_$$(go env GOOS)_$$(go env GOARCH)_v1/status bin/

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

.PHONY: test-e2e
test-e2e: vet staticcheck
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
