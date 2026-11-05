GO ?= go
UV ?= uv
BIN := bin/arned
PKG := ./...

.PHONY: all build dev devnet test test-go test-py test-crypto crypto conformance lint fmt generate clean

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

# Shared Rust core, C ABI, and the Go/Python bindings.
crypto:
	cd crypto/core && cargo build --release

test-crypto: crypto
	cd crypto/core && cargo test --release
	cd crypto/core && cc c_smoke.c target/release/libarne_crypto.a -lpthread -ldl -lm -o target/c_smoke
	cd crypto/core && ./target/c_smoke
	$(GO) test -tags cryptocore ./internal/crypto/core/
	$(UV) run pytest tests/test_core.py -q

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

# Fail when checked-in generated files are stale. CI runs this on every push.
check-generated:
	$(GO) run ./cmd/genvectors -check
	$(GO) test ./internal/api -run TestServedDocumentMatchesSpec -count=1

clean:
	rm -rf bin tmp .arne-devnet .pytest_cache crypto/core/target
	find . -name __pycache__ -type d -prune -exec rm -rf {} +
