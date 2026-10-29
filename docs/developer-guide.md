# Developer guide

## Prerequisites

- Go 1.27+ (`go.mod`)
- Python 3.11+ and [uv](https://docs.astral.sh/uv/)
- `pre-commit` (optional but recommended)

```sh
uv sync --extra dev
pre-commit install
make lint
make test
```

## Repository map

| Path | Purpose |
| --- | --- |
| `cmd/arned` | Canonical node, devnet runner, wallet/payment CLI |
| `cmd/genvectors` | Regenerates `spec/vectors/*.json` from Go |
| `internal/protocol` | CBOR, types, addresses, keys, signing |
| `internal/smt` | 256-bit sparse Merkle tree and proofs |
| `internal/ledger` | Params, state, execution, fees, blocks, genesis |
| `internal/storage` | Pebble and in-memory stores |
| `internal/node` | Mempool, block production, events, faucet |
| `internal/api` | Echo REST `/v1`, JSON-RPC, SSE, auth, rate limit |
| `python/arne` | Full parity node (same module split) |
| `spec/vectors` | Cross-language conformance anchors |
| `tests` | Go vector tests, Python vector/ledger/API tests |
| `openspec/changes/arce-arne-network` | Requirements, design, task tracker |

## Canonical encoding

Everything consensus-critical uses deterministic CBOR:

- shortest integer/length forms only;
- map keys sorted by encoded length then bytewise;
- no floats, tags, indefinite lengths, or duplicate keys;
- depth limit 64.

`protocol.Encode`/`protocol.Decode` reject non-canonical input. Envelope
decoders ignore unknown fields, so adding a consensus field is a protocol change:
bump the transaction version rather than relying on compatibility.

Go is the source of truth. If Go and Python disagree, fix Python and add a
vector.

## Identity and addresses

- Key derivation: BIP-39 mnemonic → SLIP-0010 Ed25519 at
  `m/44'/9999'/account'/change/index`.
- Public key: 32-byte Ed25519.
- Address: Bech32m over `0x01 || SHA-256(pubkey)[:20]`, HRP by network:
  `arce`, `arce-testnet`, `arce-devnet`, `arce-consortium`.
- Signature suite: `ed25519-v1`.
- Signing payload: canonical CBOR of
  `{dom:"arne-sig-v1", network, chain, suite, purpose, expires_at, body}`.
  `body` contains the transaction fields but not signatures. This binds a
  signature to one network, one chain, and one purpose, so a mainnet signature
  cannot replay on devnet.

Transaction hashes are SHA-256 of the canonical transaction CBOR including
signatures. The hash is stable across languages and is the receipt key.

## State

State is a sparse Merkle tree keyed by strings:

```
account/<address>/nonce       uint64, big-endian 8 bytes
account/<address>/auth        auth suite string
balance/<address>/<asset>     uint64, big-endian 8 bytes
asset/<asset-id>              canonical CBOR asset
supply/<asset-id>             uint64
burned/<asset-id>             uint64
escrow/<escrow-id>            canonical CBOR escrow
escrow_held/<asset-id>        uint64 held in escrow
allowance/<owner>/<spender>/<asset>       uint64
allowance_expiry/<owner>/<spender>/<asset> uint64
pool/<pool>/<asset>           uint64 (fee_swap, validators, treasury, faucet, community)
route/<asset>                 uint64 ARCE base units per 1e8 asset units
meta/height, meta/time        uint64
```

The state root is the SHA-256 tree root. Any key can be proven; exclusion proofs
use `depth` + `terminal` to handle a different leaf occupying the path.

## Transaction lifecycle

`State.ApplyTx`:

1. Validate schema and version.
2. Check network and chain.
3. Check `expires_at` (past → `EXPIRED`, too far future → `INVALID_SCHEMA`).
4. Verify the signature.
5. Check the exact nonce.
6. Charge the fee and split it.
7. Execute the type-specific payload.
8. Check conservation for the fee asset and the native asset.

Any error rolls back the whole journal and returns a receipt with `success:
false` and a stable code. Failed transactions are free
(`shortcut: failed txs are free, add fee-on-failure when mempool spam matters.`).

Fee:

```
fee = max(base_fee, min_fee) + priority_fee   // must be <= max_fee
burn       = fee * burn_bps      / 10000
validators = fee * validator_bps / 10000
treasury   = fee - burn - validators
```

For a non-native fee asset, the whole fee moves to the treasury in that asset
and the `fee_swap` pool pays `fee * route / 1e8` ARCE, which is split normally.
No route or an underfunded pool → `NO_FEE_ROUTE`.

## Blocks and consensus

There are two block sources:

- `arned devnet` (and the Python node) call `ApplyBlock`, which orders the
  mempool itself.
- `arned start` runs `internal/consensus` as a CometBFT ABCI 2.0 app. CometBFT
  owns the mempool and the order; the app validates, decides, and executes.

Standalone ordering (`ApplyBlock`):

1. Order mempool transactions by `(signer priority_fee desc, signer address)`,
   then by `(nonce, tx hash)` within a signer.
2. Simulate each transaction; keep only those whose nonce is exactly next for
   that signer and whose simulation succeeds.
3. Apply in order, collecting receipts.
4. Commit state and write the block + state snapshot atomically.
5. Broadcast a block event.

ABCI path:

1. `CheckTx` calls `node.CheckRaw`: decode, network/chain match, signature,
   expiration, fee, nonce window, simulation. Rejections return code 1.
2. `PrepareProposal` decodes and filters, orders by the same fee/nonce rule,
   keeps only the next nonce per signer, simulates against the pending height,
   and respects both the app transaction cap and `MaxTxBytes`.
3. `ProcessProposal` rejects only undecodable data. A well-formed transaction that
   fails execution is allowed in the block, is charged nothing, and rolls back.
4. `FinalizeBlock` calls `ApplyOrderedBlock` with the decided order, returns one
   `ExecTxResult` per transaction, and reports the post-block state root as the
   app hash. The height must be exactly `state height + 1`.
5. `Commit` writes the staged block and state snapshot, indexes receipts, and
   broadcasts the block event.

`Query` serves `/status` and `/balance` (JSON `{address,asset}`) for
`/abci_query`; the REST API remains the primary read surface. Note that the
CometBFT block header `app_hash` at height H is the app hash returned by
`FinalizeBlock` at H-1, so compare the header against the previous height's root.

The block hash covers height, time, proposer, previous state root, new state
root, transaction root, and transaction hashes. Any divergence in execution
changes the root.

## Adding a transaction type

1. Add the type string and payload schema in `internal/protocol`.
2. Add the handler and conservation implications in `internal/ledger/apply.go`.
3. Mirror it byte-for-byte in `python/arne/{types,ledger}.py`.
4. Add Go and Python tests for success and every failure path.
5. Add a vector in `cmd/genvectors`, run `make generate`, then `make conformance`.
6. Document the payload and error codes in `docs/api-reference.md`.
7. Update `openspec/changes/arce-arne-network/tasks.md` only if the whole task
   is actually complete.

## Testing

```sh
go test ./...                      # all Go packages
go test -race ./...                # CI runs this
uv run pytest tests -q             # Python suite
uv run pytest tests/test_vectors.py -q
go test ./tests -run TestVectors -v
```

The conformance runner loads `spec/vectors/{cbor,keys,smt,ledger,tx}.json` and
requires both languages to reproduce every byte:

- CBOR encode/decode and non-canonical rejection
- mnemonic → key → per-network address
- tree roots and inclusion/exclusion proofs
- genesis, block state roots, tx roots, block hashes, receipts
- transaction envelope bytes, hashes, signatures, and closed-schema rejection

To prove a runner is real, temporarily corrupt one vector value; both suites
must fail. Do not commit the corruption.

## Storage

Pebble keys: `b<height>` for blocks, `meta/last`, `state/current`. Each value is
`SHA-256(payload) || JSON payload`; a checksum mismatch is `ErrCorrupt` and the
node refuses to load. A block write and its state snapshot are committed in one
atomic batch, so a crash leaves the last fully committed height.

`Store.Prune(before)` deletes blocks below a height in one synced batch; the
state snapshot and last-height marker are untouched. The node calls it after
each commit when `Config.KeepBlocks` is non-zero, and evicts the matching
in-memory blocks and receipts. Archive mode is `KeepBlocks == 0`.

The Python node uses `FileStore` (one checksummed snapshot file plus one JSON
file per block). `shortcut:` a file store is not RocksDB; upgrade when block
volume or concurrent writers matter.

## Known shortcuts

Search the tree for `shortcut:`:

- failed transactions are free;
- SMT leaf padding is O(256-depth) per leaf rather than a cached subtree hash;
- Python persistence is file-based;
- Python block production is time-driven and has no ABCI adapter;
- the consensus app has no validator set, staking, or slashing logic (CometBFT
  holds a static validator set from genesis).

Each is a documented limit, not an accident. Upgrade it deliberately with tests.

## OpenSpec

The change `openspec/changes/arce-arne-network` holds capability requirements,
design decisions, and the task list. Validate it with:

```sh
openspec validate "arce-arne-network" --strict
```

Only tick a task when its verification step is true and tested.
