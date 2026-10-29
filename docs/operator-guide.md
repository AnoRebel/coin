# Operator guide

Scope: running the implemented reference node, either the in-process `devnet`
or the CometBFT-backed `start` node. Multi-node consensus, pruning, snapshots,
and production key management are **not implemented**; see the "Missing before
production" section.

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

## API exposure

- Bind to `127.0.0.1` unless you need remote access. There is no TLS in the node;
  terminate TLS at a reverse proxy.
- Always set `--api-key` and `--allow-origin` for anything reachable.
- Reads are open by design. Do not put personal data in memos; they are public.
- The faucet exists only on devnet and is disabled by `Devnet: false`.
- Request bodies are capped at 2 MiB; keep proxy limits aligned.

Example proxy contract: forward `Authorization`, set `X-Forwarded-For` only from
trusted proxies, and do not cache `/v1/events`.

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

## Upgrades

- Code upgrades are stop, replace binary, start. Storage format is stable within
  `arne/1` but there is no migration framework; read release notes before
  jumping versions.
- Genesis or consensus-parameter changes mean a new network. There is no
  coordinated activation height yet.
- Keep the previous binary and a data backup until the new version has produced
  and served blocks.

## Incident response

- **Halted node**: a conservation/invariant check failed. Stop it, preserve the
  data directory and logs, do not restart in a loop. Restore from the last known
  good backup and report the block height.
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
- online snapshots, pruning, archive mode, and migrations;
- validator key separation, HSM/KMS support, slashing and staking;
- audits and a bug-bounty program.
