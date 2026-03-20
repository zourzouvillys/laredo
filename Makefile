.PHONY: build test test-integration lint proto docker release-snapshot clean

build:
	go build ./...

test:
	go test ./...

test-integration:
	go test -tags=integration ./test/integration/...

lint:
	golangci-lint run ./...

proto:
	buf generate

docker:
	docker build -t laredo-server .

release-snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf dist/ bin/
	go clean ./...
