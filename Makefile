.PHONY: build test test-integration lint proto docker release-snapshot clean help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build server and CLI into bin/
	go build -o bin/laredo-server ./cmd/laredo-server
	go build -o bin/laredo ./cmd/laredo

test: ## Run unit tests
	go test ./...

test-integration: ## Run integration tests (requires PostgreSQL)
	go test -tags=integration ./test/integration/...

lint: ## Run golangci-lint
	golangci-lint run ./...

proto: ## Regenerate protobuf (requires buf)
	buf generate

docker: ## Build Docker image
	docker build -t laredo-server .

release-snapshot: ## Local goreleaser dry-run
	goreleaser release --snapshot --clean

clean: ## Remove build artifacts
	rm -rf dist/ bin/
	go clean ./...
