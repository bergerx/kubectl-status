
export GO111MODULE=on

PATH=/usr/bin:$$PATH

.DEFAULT_GOAL := bin

.PHONY: test
test: vet staticcheck
	go test ./... -coverprofile cover.out
	# go tool cover -html=cover.out -o cover.html

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
	go run honnef.co/go/tools/cmd/staticcheck@v0.4.7 ./...

.PHONY: clean
clean:
	@rm -fv bin/status
	@rm -fvr dist
