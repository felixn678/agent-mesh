MODULE   := github.com/felixn678/agent-mesh
VERSION  := $(shell git describe --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)
GOBIN    := $(shell go env GOPATH)/bin
GITLEAKS := $(or $(shell command -v gitleaks 2>/dev/null),$(wildcard $(GOBIN)/gitleaks))

.PHONY: build test vet cross lint-secrets clean

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/agent-mesh ./cmd/agent-mesh

vet:
	go vet ./...

test: vet
	go test -race ./...

# Release binaries, one per supported platform.
cross:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/agent-mesh-linux-amd64  ./cmd/agent-mesh
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/agent-mesh-darwin-arm64 ./cmd/agent-mesh
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/agent-mesh-darwin-amd64 ./cmd/agent-mesh

lint-secrets:
	@if [ -z "$(GITLEAKS)" ]; then echo "gitleaks not found (install: go install github.com/zricethezav/gitleaks/v8@latest)"; exit 1; fi
	$(GITLEAKS) detect --source . --no-banner

clean:
	rm -rf bin dist
