BINARY  := garmin-papi-mcp
CMD     := ./cmd/garmin-papi-mcp
BIN_DIR := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := build
.PHONY: build test live-test vet lint fmt clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD)

test:
	go test -race ./...

# Opt-in: runs every tool against the real Garmin API with the key in
# test/api.key (or GARMIN_API_KEY_FILE). Prints names, outcomes and sizes only.
live-test:
	GARMIN_LIVE=1 go test -count=1 -v -run Live ./internal/e2e/

vet:
	go vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; falling back to go vet"; \
		go vet ./...; \
	fi

fmt:
	gofmt -w .

clean:
	rm -rf $(BIN_DIR) coverage.out
