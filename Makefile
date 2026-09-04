BUILD_DIR=./build
SERVER_BIN=khelam-server
AGENT_BIN=khelam-agent

.PHONY: build-server build-agent  run-client run-server clean

build-server:
	go build -o $(BUILD_DIR)/$(SERVER_BIN) ./cmd/server/main.go

build-agent:
	go build -o $(BUILD_DIR)/$(AGENT_BIN) ./cmd/agent/main.go

run-server: build-server
	exec $(BUILD_DIR)/$(SERVER_BIN)

run-client: build-client
	exec $(BUILD_DIR)/$(AGENT_BIN)

lint:
	@go vet ./...

format:
	@go fmt ./...

clean:
	rm -rf $(BUILD_DIR)
