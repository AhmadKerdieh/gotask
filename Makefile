.PHONY: help run build test test-race fmt vet tidy clean

# Default target — `make` with no args prints help.
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

run: ## Run the API server with live env from .env
	go run ./cmd/api

build: ## Build the API binary into ./bin/api
	@mkdir -p bin
	go build -o bin/api ./cmd/api

test: ## Run unit tests
	go test ./... -count=1

test-race: ## Run tests with the race detector
	go test ./... -count=1 -race -cover

fmt: ## Format all Go source
	go fmt ./...

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

clean: ## Remove build artifacts
	rm -rf bin coverage.html coverage.txt
