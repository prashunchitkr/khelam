BUILD_DIR=./build
SEVER_BIN=server
CLIENT_BIN=client

.PHONY: build-client build-server run-client run-server clean

build-client:
	go build -o $(BUILD_DIR)/$(CLIENT_BIN) ./cmd/client/main.go

build-server:
	go build -o $(BUILD_DIR)/$(CLIENT_BIN) ./cmd/server/main.go

run-client: build-client
	exec $(BUILD_DIR)/$(CLIENT_BIN)

run-server: build-server
	exec $(BUILD_DIR)/$(CLIENT_BIN)

clean:
	rm -rf $(BUILD_DIR)
