
export GO111MODULE=on

.DEFAULT_GOAL := bin

.PHONY: test
test: vet staticcheck
	go test ./...

.PHONY: test-e2e
test-e2e: vet staticcheck
	# using count to prevent caching
	RUN_E2E_TESTS=true go test -v ./... -count=1 -run 'TestE2E*'

.PHONY: bin
bin: fmt vet staticcheck
	mkdir -p bin
	goreleaser build --single-target --skip=validate --clean
	ln -Ffs ../dist/status_$$(go env GOOS)_$$(go env GOARCH)_v1/status bin/

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: staticcheck
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@v0.5.1 ./...

.PHONY: clean
clean:
	@rm -fv bin/status
	@rm -fvr dist
