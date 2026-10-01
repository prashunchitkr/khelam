BUILD_DIR=./build
SERVER_BIN=khelam-server
AGENT_BIN=khelam-agent

.PHONY: build build-server build-agent run-server run-agent test lint format clean

build: build-server build-agent

build-server:
	go build -o $(BUILD_DIR)/$(SERVER_BIN) ./cmd/server

build-agent:
	go build -o $(BUILD_DIR)/$(AGENT_BIN) ./cmd/agent

run-server: build-server
	exec $(BUILD_DIR)/$(SERVER_BIN) $(ARGS)

run-agent: build-agent
	exec $(BUILD_DIR)/$(AGENT_BIN) $(ARGS)

test:
	go test -race ./...

lint:
	@go vet ./...

format:
	@go fmt ./...

clean:
	rm -rf $(BUILD_DIR)
