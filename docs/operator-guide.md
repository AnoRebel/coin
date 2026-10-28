# Operator guide

Scope: running the implemented single-node devnet/reference node. Multi-node
consensus (CometBFT), pruning, snapshots, and production key management are
**not implemented**; see the "Missing before production" section.

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

The node has no online snapshot command. Correct procedure:

1. Stop the node (SIGTERM; it shuts down cleanly).
2. Copy the whole data directory.
3. Start the node and verify `/v1/status` height/state root.
4. Restore by stopping, replacing the directory, and starting; the checksummed
   state record is the recovery point.

Test a restore on a copy. Never copy a live Pebble directory with `cp`; you can
capture a torn write.

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
- **Divergence**: compare `state_root` at the same height with a peer. Without
  CometBFT there is no automatic fork choice; treat divergence as a bug.
- **Key exposure**: rotate the API keys and any operator mnemonic; move funds
  from the exposed account. There is no on-chain key rotation.
- **Disk full**: the node halts on storage errors. Free space, restart, verify
  the last height.

## Missing before production

Do not describe this node as production-ready until these exist:

- BFT consensus and multi-node networking (CometBFT adapter);
- TLS, OIDC/JWT, mTLS, and secrets management;
- metrics, tracing, structured logs, dashboards, alerts;
- online snapshots, pruning, archive mode, and migrations;
- validator key separation, HSM/KMS support, slashing and staking;
- audits and a bug-bounty program.
