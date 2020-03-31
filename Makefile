
export GO111MODULE=on

.DEFAULT_GOAL := bin

.PHONY: test
test: generate
	go test ./pkg/... ./cmd/... -coverprofile cover.out

.PHONY: bin
bin: fmt vet
	go build -o bin/status github.com/bergerx/kubectl-status/cmd/plugin

.PHONY: generate
generate: pkg/plugin/statik/statik.go

pkg/plugin/statik/statik.go: pkg/plugin/templates/templates.tmpl
	go get github.com/rakyll/statik@v0.1.7
	go generate ./pkg/... ./cmd/...
	# statik generates non-fmt compliant files, so we have an extra "go fmt" here
	go fmt pkg/plugin/statik/statik.go

.PHONY: fmt
fmt: generate
	go fmt ./pkg/... ./cmd/...

.PHONY: vet
vet: generate
	go vet ./pkg/... ./cmd/...

.PHONY: kubernetes-deps
kubernetes-deps:
	go get k8s.io/client-go@v11.0.0
	go get k8s.io/api@kubernetes-1.14.0
	go get k8s.io/apimachinery@kubernetes-1.14.0
	go get k8s.io/cli-runtime@kubernetes-1.14.0

.PHONY: setup
setup:
	make -C setup

.PHONY: clean
clean:
	@rm -fv bin/status
	@rm -fv pkg/plugin/statik/statik.go
