
export GO111MODULE=on

.DEFAULT_GOAL := bin

.PHONY: test
test: generate vet staticcheck
	go test ./... -coverprofile cover.out
	# go tool cover -html=cover.out -o cover.html

.PHONY: bin
bin: generate fmt vet staticcheck
	goreleaser build --single-target --skip-validate --rm-dist
	ln -Fs ../dist/status_$$(go env GOOS)_$$(go env GOARCH)_v1/status bin/

.PHONY: generate
generate: pkg/plugin/statik/statik.go

pkg/plugin/statik/statik.go: pkg/plugin/templates/*.tmpl
	go generate ./...

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: staticcheck
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@v0.3.1 ./...

.PHONY: setup
setup:
	make -C setup

.PHONY: clean
clean:
	@rm -fv bin/status
	@rm -fvr dist
	@rm -fv pkg/plugin/statik/statik.go
