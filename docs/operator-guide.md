# Operator guide

Scope: running the implemented reference node, either the in-process `devnet`
or the CometBFT-backed `start` node. Multi-node consensus, snapshots,
CometBFT blockstore pruning, and production key management are **not
implemented**; see the "Missing before production" section.

## Build

```sh
make build          # bin/arned
docker build -t arne-node .
```

Python:

```sh
uv sync --no-dev
uv run arne --version
```

## Run

Go:

```sh
./bin/arned devnet \
  --network arne:devnet \
  --listen 127.0.0.1:8080 \
  --data-dir /var/lib/arne \
  --block-interval 2s \
  --rate-limit 200 \
  --api-key "ops=read,write,admin" \
  --allow-origin https://app.example
```

Flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--network` | `arne:devnet` | `arne:mainnet`, `arne:testnet`, `arne:devnet`, `arne:consortium/<name>` |
| `--listen` | `127.0.0.1:8080` | REST/JSON-RPC/SSE listen address |
| `--data-dir` | `.arne-devnet` | Pebble directory |
| `--mnemonic` | generated | Deterministic funder/faucet derivation; keep secret |
| `--block-interval` | `2s` | Idle block interval (blocks are also produced when the mempool is non-empty) |
| `--ephemeral` | false | In-memory store; nothing survives restart |
| `--api-key` | none | Repeatable `key=read,write[,admin]`; when set, writes require `Authorization: Bearer <key>` |
| `--allow-origin` | none | Repeatable CORS allowlist entry |
| `--rate-limit` | 200 | Requests/second per IP; 0 disables |
| `--keep-blocks` | 0 | Keep only the newest N ledger blocks; 0 archives all |
| `--webhook` | none | Repeatable webhook endpoint URL |
| `--webhook-key` | none | Hex Ed25519 seed used to sign webhook bodies; required with `--webhook` |

There is **no config file and no environment-variable config** yet. Flags are
the whole surface. Keep a unit file or Compose file as the source of truth.

Python uses the same flags on `serve`/`devnet`; `--wait` is available on client
commands.

## Consensus node (CometBFT)

`arned start` runs the same ledger as an ABCI 2.0 application under a real
CometBFT node. The app produces no blocks itself: CometBFT orders the mempool,
`PrepareProposal`/`ProcessProposal` filter and accept it, `FinalizeBlock` executes
the decided order, and `Commit` persists the block and state snapshot.

```sh
./bin/arned start \
  --home /var/lib/arne-consensus \
  --network arne:devnet \
  --listen 127.0.0.1:8080 \
  --rpc tcp://127.0.0.1:26657 \
  --p2p tcp://0.0.0.0:26656 \
  --block-interval 1s
```

Flags beyond the `devnet` set:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--home` | `.arne` | CometBFT home: `config/`, `data/`, keys |
| `--rpc` | `tcp://127.0.0.1:26657` | CometBFT RPC listen address |
| `--p2p` | `tcp://0.0.0.0:26656` | CometBFT P2P listen address |
| `--block-interval` | `1s` | Consensus `timeout_commit` |
| `--data-dir` | `<home>/app` | Pebble directory for ledger blocks/state |
| `--ephemeral` | false | Temporary home plus in-memory ledger |
| `--keep-blocks` | 0 | Keep only the newest N ledger blocks; 0 archives all |
| `--webhook` | none | Repeatable webhook endpoint URL |
| `--webhook-key` | none | Hex Ed25519 seed used to sign webhook bodies |

Layout under `--home`: `config/genesis.json`, `config/priv_validator_key.json`,
`config/node_key.json`, `data/` (CometBFT blockstore/state), and `app/` (Pebble).

Genesis is created automatically **only for `arne:devnet`**; it funds the funder
and faucet accounts derived from `--mnemonic`, so pass the same mnemonic on
restart if the faucet must keep working. For every other network, place the
network's `genesis.json` in `<home>/config/` before first start. Genesis
`app_state` must be the canonical ledger genesis with a matching `chain` field.

REST `/v1` still works as documented; transactions submitted there are forwarded
to CometBFT via `broadcast_tx_sync`. `/v1/status` reports the ledger height and
the CometBFT mempool size. CometBFT's own endpoints (`/status`, `/block`,
`/tx`) are on the RPC address.

Restart the node at the last committed height; do not delete `data/` to "fix" a
startup error. Back up both `data/` and `app/` while stopped.

## Data and persistence

- Go: Pebble at `--data-dir`. Blocks are keyed `b<height>`, plus `meta/last` and
  `state/current`. Each record is SHA-256 checksummed. Block + state snapshot
  are written in one atomic batch.
- Python: `blocks/<height>.json` and a checksummed `state.json` under
  `--data-dir`. Use one process per directory; there is no lock.

A node reopens at the last committed height. A corrupt record is a hard error;
do not "fix" it by deleting a checksum. Restore from backup.

## Schema migrations

Every store records an on-disk schema version. An unversioned store reports 0
and the current binary supports 1. `arned start` and the devnet command migrate
automatically after opening the store, and refuse to run if the store is newer
than the binary.

```sh
arned migrate --data-dir /var/lib/arne/app --status
arned migrate --data-dir /var/lib/arne/app           # to the current version
arned migrate --data-dir /var/lib/arne/app --to 0    # roll back
```

Each step commits its data change and the new version in one synced batch, so a
crash leaves the previous version intact; rerunning the same step is a no-op.
Stop the node before running the command by hand. Version 1 only records the
baseline marker and rewrites no data. For a destructively migrated store, take
an encrypted backup before `--to 0` and keep the previous binary until the
restored node serves blocks.

The Python node does the same on open and exposes `arne migrate --data-dir DIR
[--status] [--to N]`; its version lives in `schema.json`.

## API exposure

- Bind to `127.0.0.1` unless you need remote access. There is no TLS in the node;
  terminate TLS at a reverse proxy.
- Always set `--api-key` and `--allow-origin` for anything reachable.
- Reads are open by design. Plaintext memos are public; opt-in sealed memos hide
  text only, not sender, recipient, amount, or time.
- The faucet exists only on devnet and is disabled by `Devnet: false`.
- Request bodies are capped at 2 MiB; keep proxy limits aligned.

Example proxy contract: forward `Authorization`, set `X-Forwarded-For` only from
trusted proxies, and do not cache `/v1/events`.

Transaction-submit idempotency retains up to 10,000 accepted requests for
24 hours in API-process memory. Restart clears it, and separate API instances
do not share it. Route a retry sequence to one instance, and have integrations
retain their own signed bytes and receipts. `503 IDEMPOTENCY_FULL` means the
cache has no free/expired slot; it does not discard another live request to
make room. This is distinct from the persistent payable-once intent guard.

## Docker Compose

```sh
docker compose -f deploy/docker-compose.yml up --build
docker compose -f deploy/docker-compose.yml --profile python up --build
```

The named volume holds state. The healthcheck calls `arned status`. Back up the
volume while the node is stopped.

## Health and monitoring

- `GET /v1/health` → `200 ok` or `503 halted`.
- `GET /v1/status` → height, state root, mempool, halt reason.
- Logs are plain text on stderr; there are no metrics, traces, or structured
  logs yet. Scrape `/v1/status` and alert on:
  - height not increasing,
  - `mempool` growing without blocks,
  - `halted` non-empty,
  - state root changing unexpectedly between restarts.

## Snapshots

Snapshots move or archive the app store without the encryption of a home
backup. Stop the node first.

```sh
arned snapshot create --data-dir /var/lib/arne/app --out /backup/full.snap
arned snapshot verify --in /backup/full.snap
arned snapshot restore --in /backup/full.snap --data-dir /var/lib/arne-restored
```

A full snapshot contains every block and the state at the snapshot height. An
incremental snapshot contains only blocks above a full base, plus the current
state; create and restore it with the base file:

```sh
arned snapshot create --data-dir /var/lib/arne/app --out /backup/inc.snap --base /backup/full.snap
arned snapshot verify --in /backup/inc.snap --base /backup/full.snap
arned snapshot restore --in /backup/inc.snap --base /backup/full.snap --data-dir /var/lib/arne-restored
```

`--network` defaults to `arne:devnet`; `--chain` is derived from it unless set.
The file is gzipped JSON with a SHA-256 digest over every field. Verification
also recomputes the state root from the state map, checks block order, checks
each `tx_root`, and links each block to its predecessor's state root. A
tampered file is rejected before anything is written. Restore writes into a
sibling temporary directory, verifies the result, and renames it into place;
the destination must not exist.

Snapshots do not include CometBFT keys or blockstore. For a consensus node, use
the encrypted home backup instead (or keep both copies).

## Backup and restore

The node has no online snapshot command. Stop it first (SIGTERM; it shuts down
cleanly), then archive the whole home:

```sh
export ARNE_BACKUP_PASSPHRASE='...'
arned backup --home /var/lib/arne --out /backup/arne-$(date +%F).arn
arned restore --in /backup/arne-$(date +%F).arn --home /var/lib/arne-restored
```

For `arned start`, `--home` also contains the CometBFT keys and blockstore, so
one archive covers the app and consensus data.

The archive is a tar of the home encrypted and authenticated with AES-256-GCM
under a scrypt-derived key. A wrong passphrase, a flipped byte, or a truncated
file fails before anything is written. Restore extracts into a sibling directory
and renames it into place only after the whole stream authenticates; it refuses
to overwrite an existing home. Start the restored node and verify
`/v1/status` height and state root.

Test a restore on a copy. Never copy a live Pebble directory with `cp`; you can
capture a torn write. Keep the passphrase out of shell history by exporting
`ARNE_BACKUP_PASSPHRASE` from a secret manager.

## Pruning and archive mode

`--keep-blocks N` keeps the newest N ledger blocks and deletes older ones from
the app store after each commit; the default `0` archives every block. State,
balances, and proofs are never pruned, so a proof for a current key always
works.

Pruned history is gone, not hidden:

- `GET /v1/blocks/<height>` returns `410 PRUNED` with the oldest available
  height; the JSON-RPC method returns `-32005` with the same message.
- `GET /v1/transactions/<hash>` returns `404 TX_NOT_FOUND` once its block falls
  outside the window; the receipt no longer records the pruned height.
- Recent proofs still verify against `/v1/status`; a proof is a state claim, not
  a block-history claim.

Pruning only touches the app's Pebble store. CometBFT's own blockstore under
`<home>/data/` still retains everything; there is no CometBFT pruning command
or snapshot sync yet. Back up before changing `--keep-blocks`, because lowering
the window is irreversible.

## Signed webhooks

Enable delivery per endpoint with a stable signing key:

```sh
arned webhook keygen                 # prints seed and public key; store the seed
arned devnet --webhook https://ops.example/arne \
  --webhook-key <64-hex-seed>
```

Every finalized transaction is delivered as a JSON `transaction` event. The
event body carries `delivery_id`, `type`, `created_at`, and `data` with the
block height/hash and full receipt. Retries reuse the same `delivery_id`, so a
receiver can deduplicate; each attempt is signed with a fresh timestamp.

Delivery is at-least-once. A 2xx response acknowledges; 429 and 5xx are retried
with exponential backoff up to 5 attempts. Other 4xx responses are not retried.
The in-memory queue holds 256 events and drops new events when full; receivers
should reconcile from the chain after downtime.

Verify deliveries with the published key:

- `GET /v1/webhooks/key` returns `{"algorithm":"ed25519","public_key":"<hex>"}`.
- Headers: `X-Arne-Delivery`, `X-Arne-Timestamp`, `X-Arne-Signature`.
- The signature is Ed25519 over `<timestamp>\n<body>`; reject timestamps more
  than 5 minutes from local time and deduplicate `X-Arne-Delivery`.

The key is printed at startup and served by the API, so receivers do not need
out-of-band distribution. Keep the seed secret: anyone holding it can forge
deliveries.

## Upgrades

- Code upgrades are stop, replace binary, start. Opening the store runs any
  pending schema migration automatically; a store written by a newer binary is
  refused. Use `arned migrate --status` to inspect and `--to 0` to roll back
  after taking an encrypted backup. Read release notes before jumping versions.
- Genesis or consensus-parameter changes mean a new network. There is no
  coordinated activation height yet.
- Keep the previous binary and a data backup until the new version has produced
  and served blocks.

## Incident response

- **Halted node**: a conservation/invariant check failed, or the last persisted
  block's state root does not match the loaded state. Stop it, preserve the data
  directory and logs, do not restart in a loop. Restore from the last known
  good backup and report the block height and state root.
- **Divergence**: compare `state_root` at the same height with a peer. CometBFT
  decides the chain, but a divergent app hash is still a bug; preserve both
  data directories and report the height.
- **Key exposure**: rotate the API keys and any operator mnemonic; move funds
  from the exposed account. There is no on-chain key rotation.
- **Disk full**: the node halts on storage errors. Free space, restart, verify
  the last height.

## Missing before production

Do not describe this node as production-ready until these exist:

- multi-node consensus networking, validator admission, staking and slashing;
- TLS, OIDC/JWT, mTLS, and secrets management;
- metrics, tracing, structured logs, dashboards, alerts;
- online snapshots, CometBFT blockstore pruning, and migrations;
- validator key separation, HSM/KMS support, slashing and staking;
- audits and a bug-bounty program.
