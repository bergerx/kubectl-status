
export GO111MODULE=on

.DEFAULT_GOAL := bin

.PHONY: test
test:
	go test ./pkg/... ./cmd/... -coverprofile cover.out

.PHONY: bin
bin: fmt vet
	go build -o bin/status github.com/bergerx/kubectl-status/cmd/plugin

.PHONY: generate
generate:
	go generate ./pkg/... ./cmd/...

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
