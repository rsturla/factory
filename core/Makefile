.PHONY: all build test clean proto

all: build test

build:
	go build -o bin/factory-api ./cmd/factory-api
	go build -o bin/factory-orchestrator ./cmd/factory-orchestrator
	go build -o bin/factory-sandbox-manager ./cmd/factory-sandbox-manager
	go build -o bin/factory-git-proxy ./cmd/factory-git-proxy
	go build -o bin/factory-output-processor ./cmd/factory-output-processor

test:
	go test ./... -v

clean:
	rm -rf bin/

proto:
	buf generate
