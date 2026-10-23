VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/xudipta/edge-mcp/internal/server.Version=$(VERSION)

.PHONY: build test lint demo docker clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/edge-mcp ./cmd/edge-mcp

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...

demo: build
	./bin/edge-mcp --demo

docker:
	docker build --build-arg VERSION=$(VERSION) -t edge-mcp:$(VERSION) .

clean:
	rm -rf bin
