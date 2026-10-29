# AnorebelCoin (ARCE) / AnorebelNetwork (ARNE)

A production-oriented, currency-agnostic settlement and team-communication
network. ARCE is the native fee and staking asset; ARNE is the network and
protocol. Any team, business, or consortium can register and move its own
assets on the same chain.

Status: **early reference implementation**. The ledger, protocol, REST/JSON-RPC
API, Go node, Python parity node, and a single-validator CometBFT consensus node
are implemented and tested. Multi-node consensus, smart contracts, the shielded
pool, messaging, compliance connectors, and the web console are specified but
**not yet implemented**. Nothing here is audited; do not put real value on it.

## What works today

| Area | Status |
| --- | --- |
| Canonical CBOR, hashing, domain separation | Implemented (Go + Python) |
| Bech32m network-scoped addresses | Implemented (Go + Python) |
| BIP-39 / SLIP-0010 Ed25519 HD keys | Implemented (Go + Python) |
| Sparse Merkle state tree + proofs | Implemented (Go + Python) |
| Multi-asset ledger, transfers, batches | Implemented (Go + Python) |
| Fees, split, foreign-asset fee conversion | Implemented (Go + Python) |
| Escrow, allowances, mint/burn, supply caps | Implemented (Go + Python) |
| Blocks, deterministic ordering, receipts | Implemented (Go + Python) |
| REST `/v1`, JSON-RPC 2.0, SSE, API keys | Implemented (Go + Python) |
| Single-node devnet + faucet + CLI | Implemented (Go + Python) |
| Cross-language conformance vectors | Implemented |
| CometBFT / BFT PoS consensus | ABCI app wired; single-validator devnet verified, multi-node not yet |
| WASM contracts, templates | Specified |
| Shielded transfers | Specified |
| Encrypted team messaging (MLS) | Specified |
| ISO 20022 / ILP / EVM connectors | Specified |
| Governance, staking lifecycle, slashing | Specified |
| Web console, SDKs | Specified |

The Go node in `cmd/arned` is canonical. The Python node in `python/arne` must
produce byte-identical state roots, transaction roots, and block hashes; that is
enforced by `tests/`.

## Quickstart (Go node)

Requires Go 1.27+.

```sh
go run ./cmd/arned devnet --ephemeral
```

In another shell:

```sh
go run ./cmd/arned wallet new --network arne:devnet --json
go run ./cmd/arned faucet --address arce-devnet1...      # prints a tx hash
go run ./cmd/arned balance --address arce-devnet1... --asset arce:devnet
go run ./cmd/arned pay --mnemonic "..." --to arce-devnet1... \
  --asset arce:devnet --amount 12.5 --memo "invoice 42"
go run ./cmd/arned status --json
```

`devnet` produces blocks in-process. To run the same ledger under CometBFT
consensus, use `arned start` instead; see `docs/operator-guide.md`.

The API listens on `http://127.0.0.1:8080`; interactive docs are at `/docs` on
the Python node and the OpenAPI schema at `/openapi.json`.

## Quickstart (Python node)

Requires Python 3.11+ and [uv](https://docs.astral.sh/uv/).

```sh
uv sync --extra dev
uv run arne devnet --ephemeral
```

```sh
uv run arne wallet new --network arne:devnet
uv run arne faucet --address arce-devnet1... --wait
uv run arne balance --address arce-devnet1... --asset arce:devnet
uv run arne pay --mnemonic "..." --to arce-devnet1... \
  --asset arce:devnet --amount 12.5 --memo "invoice 42" --wait
```

## Architecture

```
                    REST /v1 · JSON-RPC · SSE · CLI
                                 |
                    +------------+------------+
                    |                         |
              cmd/arned (Go)           python/arne (parity)
                    |                         |
              internal/api              arne/api.py
                    |                         |
              internal/node             arne/node.py
                    |                         |
              internal/ledger  <-- vectors -->  arne/ledger.py
                    |                         |
        internal/protocol + internal/smt   arne/{types,smt,cbor}.py
                    |                         |
              internal/storage          FileStore / MemoryStore
                  (Pebble)
```

Every consensus-critical byte is defined once and checked by both languages
against `spec/vectors/`:

- deterministic CBOR (canonical map ordering, no floats/tags)
- Ed25519 signatures over a canonical transaction body with network/chain binding
- SHA-256 hashed sparse Merkle tree with namespaced string keys
- account-based multi-asset state, with a separate shielded pool planned

## Layout

```
cmd/arned/        canonical Go node + CLI
cmd/genvectors/   vector generator (Go is the source of truth)
internal/         protocol, crypto, smt, ledger, storage, node, api, consensus
python/arne/      full Python parity node (CLI, API, ledger, node)
spec/vectors/     cross-language conformance vectors
spec/cbor/        schema notes            (planned)
spec/openapi/     OpenAPI export          (planned)
tests/            cross-language + HTTP + ledger tests
deploy/           Docker Compose
docs/             user, developer, integration, operator, API, security docs
openspec/         specification change and capability requirements
legacy/           retired RebelCoin code, not part of the build
```

## Key concepts

- **Network IDs** are `arne:mainnet`, `arne:testnet`, `arne:devnet`, or
  `arne:consortium/<name>`. Chain IDs are derived from them.
- **Asset IDs** are `<symbol>:<network-short-name>`, e.g. `arce:devnet`,
  `usd:devnet`, `eur:acme` for `arne:consortium/acme`. Addresses are Bech32m
  with an `arce*` HRP so a mainnet address cannot be spent on devnet.
- **Fees** are paid in ARCE by default, or in any registered asset that has a
  `route/<asset>` rate and a funded `fee_swap` pool. The fee is split 50% burn,
  30% validators, 20% treasury.
- **Failed transactions are free and fully rolled back** (documented shortcut;
  fee-on-failure is a later change once mempool spam matters).
- **State proofs**: any namespaced key (`balance/<addr>/<asset>`,
  `account/<addr>/nonce`, `escrow/<id>`, ...) can be proven against the state
  root via `/v1/proofs/*`.

## Development

```sh
make lint       # go vet + ruff
make test       # go test -race + pytest
make conformance
make generate   # regenerate vectors after an intentional protocol change
make dev        # air live reload
```

See [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md), and
[docs/](docs/README.md).

## Documentation

- [User guide](docs/user-guide.md) — wallets, payments, escrow, assets
- [Developer guide](docs/developer-guide.md) — architecture, ledger, tests
- [Embedding guide](docs/embedding.md) — run or embed a node in a service
- [Integration guide](docs/integration-guide.md) — REST, JSON-RPC, SSE, CBOR
- [API reference](docs/api-reference.md) — endpoints, methods, error codes
- [Operator guide](docs/operator-guide.md) — deployment, storage, hardening
- [Security, privacy, compliance](docs/security.md)
- [Tokenomics](docs/tokenomics.md)

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
