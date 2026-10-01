BUILD_DIR=./build
SERVER_BIN=khelam-server
AGENT_BIN=khelam-agent

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS = -s -w -X github.com/prashunchitkr/khelamgg/internal/version.Version=$(VERSION)

# docker or podman, whichever is installed.
CONTAINER ?= $(shell command -v docker || command -v podman)

.PHONY: build build-server build-agent run-server run-agent test lint format images snapshot clean

build: build-server build-agent

build-server:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(SERVER_BIN) ./cmd/server

build-agent:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(AGENT_BIN) ./cmd/agent

run-server: build-server
	exec $(BUILD_DIR)/$(SERVER_BIN) $(ARGS)

run-agent: build-agent
	exec $(BUILD_DIR)/$(AGENT_BIN) $(ARGS)

test:
	go test -race ./...

lint:
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

format:
	@go fmt ./...

images:
	$(CONTAINER) build --target server --build-arg VERSION=$(VERSION) -t khelam-server:$(VERSION) .
	$(CONTAINER) build --target agent --build-arg VERSION=$(VERSION) -t khelam-agent:$(VERSION) .

# Builds release archives into dist/ without publishing.
snapshot:
	go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean

clean:
	rm -rf $(BUILD_DIR) dist
