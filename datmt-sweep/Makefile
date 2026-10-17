VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o datmt-sweep ./cmd/datmt-sweep

test:
	go vet ./... && go test ./...

snapshot:
	goreleaser release --snapshot --clean

.PHONY: build test snapshot
