VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/xudipta/edge-mcp/internal/server.Version=$(VERSION)

.PHONY: build test e2e cover lint demo docker clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/edge-mcp ./cmd/edge-mcp

test:
	go test -race ./...

# Needs a cluster prepared with test/e2e/setup.sh (see test/e2e/kind.yaml); CI does this with kind.
e2e: build
	E2E_BIN=$(CURDIR)/bin/edge-mcp E2E_READONLY_KUBECONFIG=$(CURDIR)/test/e2e/.out/readonly.kubeconfig \
		go test -tags e2e -v -count=1 -timeout 20m ./test/e2e

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	@command -v golangci-lint >/dev/null && golangci-lint run ./... || echo "golangci-lint not installed; skipping (CI runs it)"

demo: build
	./bin/edge-mcp --demo

docker:
	docker build --build-arg VERSION=$(VERSION) -t edge-mcp:$(VERSION) .

clean:
	rm -rf bin coverage.out
