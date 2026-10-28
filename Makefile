GO ?= go
UV ?= uv
BIN := bin/arned
PKG := ./...

.PHONY: all build dev devnet test test-go test-py conformance lint fmt generate clean

all: lint test

build:
	$(GO) build -o $(BIN) ./cmd/arned

# Live-reload development node (requires `go install github.com/air-verse/air@latest`).
dev:
	air

# Single-node devnet with faucet on http://127.0.0.1:8080.
devnet:
	$(GO) run ./cmd/arned devnet --ephemeral

test: test-go test-py

test-go:
	$(GO) test -race $(PKG)

test-py:
	$(UV) run pytest tests -q

# Cross-language conformance only.
conformance:
	$(GO) test ./tests -run TestVectors -v
	$(UV) run pytest tests/test_vectors.py -q

lint:
	$(GO) vet $(PKG)
	$(UV) run ruff check python tests

fmt:
	$(GO) fmt $(PKG)
	$(UV) run ruff format python tests

# Regenerate spec/vectors from the canonical Go implementation.
generate:
	$(GO) run ./cmd/genvectors

clean:
	rm -rf bin tmp .arne-devnet .pytest_cache
	find . -name __pycache__ -type d -prune -exec rm -rf {} +
