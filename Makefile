
export GO111MODULE=on

.DEFAULT_GOAL := bin

.PHONY: test
test: generate
	go test ./pkg/... ./cmd/... -coverprofile cover.out
	# go tool cover -html=cover.out -o cover.html

.PHONY: bin
bin: fmt vet
	go build -o bin/status github.com/bergerx/kubectl-status/cmd/plugin

.PHONY: generate
generate: pkg/plugin/statik/statik.go

pkg/plugin/statik/statik.go: pkg/plugin/templates/*.tmpl
	go install github.com/rakyll/statik@v0.1.7
	go generate ./pkg/... ./cmd/...
	# statik generates non-fmt compliant files, so we have an extra "go fmt" here
	go fmt pkg/plugin/statik/statik.go

.PHONY: fmt
fmt: generate
	go fmt ./pkg/... ./cmd/...

.PHONY: vet
vet: generate
	go vet ./pkg/... ./cmd/...

.PHONY: setup
setup:
	make -C setup

.PHONY: clean
clean:
	@rm -fv bin/status
	@rm -fv pkg/plugin/statik/statik.go
