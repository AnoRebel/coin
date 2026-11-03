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

The shared Rust crypto core is optional for normal operation. Build and test it
with `make test-crypto`; it needs a Rust toolchain and a C compiler. Without the
`cryptocore` Go build tag the node uses the pure-Go crypto packages.

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

## Multi-node devnet

`arned multi-devnet` initializes several CometBFT validators under one root,
shares one genesis between them, and points every node's persistent peers at the
others. Each node gets its own home (`node-0`, `node-1`, ...), validator key,
node key, and ports.

```sh
./bin/arned multi-devnet --nodes 2
./bin/arned multi-devnet --nodes 3 --p2p-base 26656 --rpc-base 26657
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--root` | `.arne-multidevnet` | Devnet root directory |
| `--network` | `arne:devnet` | Network ID used in genesis and addresses |
| `--nodes` | 2 | Number of validators |
| `--block-interval` | 300ms | CometBFT `timeout_commit` |
| `--p2p-base` | 0 | First P2P port; 0 picks free ports per node |
| `--rpc-base` | 0 | First RPC port; 0 picks free ports per node |
| `--keep-blocks` | 0 | Keep only the newest N ledger blocks; 0 archives all |

The command prints each node's kind, home, P2P, and RPC address, then runs until
Ctrl-C. With `--p2p-base`/`--rpc-base`, ports are allocated as `base + index`.
Every node uses the Go app by default. Stop the process before deleting the root.

### Mixed Go and Python devnet

`--python-nodes N` runs the first N validators with the Python ABCI app instead
of the Go app, so a single chain has validators executing different
implementations. Consensus only advances while both apps return the same app
hash, which makes any divergence a hard stop rather than a silent fork.

```sh
./bin/arned multi-devnet --nodes 2 --python-nodes 1
```

Run it from the repository root so `uv` can find `pyproject.toml`, or pass an
explicit command:

```sh
./bin/arned multi-devnet --nodes 2 --python-nodes 1 \
  --python-cmd "uv run python -m arne.abci"
```

The Python node stores its ledger under `<node home>/python`; the Go node uses
`<node home>/app`. Both read the same `config/genesis.json`. If the Python
process exits, its node stops producing blocks.

## Documentation site

The project documentation is published at <https://coin.anorebel.net> from the
`AnoRebel/coin` repository. The maintainer deploys it on a Dokploy instance:
create or connect the repository (the GitHub CLI works:
`gh repo create AnoRebel/coin --source . --push`), point a Dokploy application
at it, set the domain `coin.anorebel.net`, and enable automatic deployment on
the default branch. Build only from committed sources and never put validator
keys or tokens in the Dokploy build environment.

The site lives in `apps/docs` and is a Docus (Nuxt 4, `compatibilityVersion: 5`)
app. It reads `docs/*.md` in place; there is no copied content directory. It
needs Node 26 and Bun:

```sh
cd apps/docs
bun install
bun run dev      # http://localhost:3000
bun run build
node .output/server/index.mjs
```

For Dokploy, use `apps/docs/Dockerfile` with the repository root as the build
context; the image serves on port 3000. `llms.txt`, `llms-full.txt`, and
per-page Markdown (`/user-guide.md`) are generated for agents.

## Data and persistence

- Go: Pebble at `--data-dir`. Blocks are keyed `b<height>`, plus `meta/last` and
  `state/current`. Each record is SHA-256 checksummed. Block + state snapshot
  are written in one atomic batch.
- Python file store: `blocks/<height>.json` and a checksummed `state.json` under
  `--data-dir`. Use one process per directory; there is no lock.
- Python RocksDB: `--store rocksdb` uses the same keys as Go (`b<height>`,
  `meta/last`, `state/current`, `meta/schema`) with the same checksummed record
  format. A commit writes the block, height, and state in one synced batch, so a
  crash cannot expose a block without its state. RocksDB itself takes the
  directory lock.

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
- `--workspace <file>` enables workspace roles. Every request then needs an API
  key listed in the file's `principals` map; roles gate payments, payment reads,
  membership changes, and audit access. Keep the file at mode `0600` because it
  contains API keys. Audit records are appended to `<file>.audit.jsonl`.

Workspace file:

```json
{
  "members": {"alice": "owner", "bob": "admin", "carol": "member", "dave": "auditor"},
  "principals": {"key-alice": "alice", "key-bob": "bob"}
}
```

Roles: `owner` (everything, including managing owners), `admin` (channels,
payments, members below owner, audit read), `member` (send and pay, read
payment records), `auditor` (read payments and audit only). Only an owner can
change or remove an owner, and the last owner cannot be demoted or removed.
Membership changes are written back atomically; audit entries include actor,
action, target, time, and result.

Auditors are scoped to explicit grants. Start with `--audit-grants <file>`
(requires `--workspace`) and create a grant per audit engagement:

```sh
curl -X POST -H "Authorization: Bearer key-alice" \
  -d '{"auditor":"dave","assets":["usd:devnet"],"from":1790000000,"until":1798000000,"purpose":"Q1 audit"}' \
  http://127.0.0.1:8080/v1/audit/grants
```

- The window is half-open (`from <= now < until`); after `until` reads fail
  automatically with `403 AUDIT_SCOPE`, so no cleanup job is needed.
- Grants are per asset. An auditor with no grant can read no payment records.
- Only an owner may create or revoke grants; only an owner can read the access
  log at `/v1/audit/grants/log`.
- Every allowed and denied check is appended to `<file>.log.jsonl`. Without
  `--audit-grants`, grants are in memory and disappear on restart.
- Grants are local to this node, not on-chain. Multi-node deployments need the
  same file (or a later on-chain grant feature) on each API node.

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

## Optional Postgres indexer

`--indexer-url postgres://…` (on `arned start` and `arned devnet`) mirrors
committed blocks and transaction hashes into Postgres for explorers and
reporting. The node creates these tables on first use:

- `indexer_schema` — read-model version (currently 1).
- `blocks(height primary key, hash, prev_root, state_root, tx_root, time, proposer)`.
- `transactions(hash primary key, height references blocks, position)`.

The indexer is never a consensus dependency:

- `Submit` only signals a worker; block production never waits for Postgres.
- Reapplying the same block is a no-op, so retries are safe.
- A block whose predecessor state root no longer matches, or a different block
  at the same height, deletes the orphaned suffix before writing the new chain.
- If the database is down at startup or during a run, the node logs a warning
  and keeps producing; the worker retries every 5 seconds.
- `--keep-blocks` can prune blocks the indexer has not copied yet. In that case
  the worker logs `indexer: source block is pruned` and the missing history
  cannot be recovered; catch it up before lowering the window.

Indexing is local to the node that runs it. Point one node per network at the
database, or give each node its own schema/database, and never feed the same
tables from two networks.

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

## Compliance reference profiles

FATF, MiCA, and ISO 20022 reference profiles are available as a library
(`internal/compliance/profile`, `python/arne/profile.py`) for deployers who need
to collect or validate the fields those regimes expect:

| Profile | Use it for |
| --- | --- |
| `fatf` | Travel Rule originator/beneficiary data above a threshold |
| `mica` | Crypto-asset metadata: DTI, LEI, white paper |
| `iso20022` | UETR, end-to-end ID, and instruction ID on payments |

Two operator decisions matter:

- The FATF trigger defaults to `1000` base units. That default is a placeholder:
  set `JurisdictionThreshold` per jurisdiction in the asset's own units, because
  the real trigger is a fiat amount and differs by country.
- The profiles validate shape and presence only. They do not check the ISO 24165
  registry, screen sanctions, exchange Travel Rule data, or make a deployment
  compliant. Every profile carries a disclaimer; do not remove it from user-facing
  output or present a profile as legal advice.

Enable only the profiles your deployment actually follows. Selecting a profile
does not change consensus or the public ledger.

Transaction monitoring is available as a library with threshold and velocity
rules, deterministic alert IDs, and analyst dispositions (`no_action`,
`escalate`, `report`) that require a reason and reviewer. A rule that asks for a
freeze only gets one when its ID is in the freeze policy; every other request is
escalated for a human, never applied automatically. The node does not evaluate
transfers or freeze funds yet, so treat this as the decision layer, not case
management.

Grant-scoped disclosure is the supported path for opening a sealed memo on
someone else's behalf. A user grant names the transfers a viewer may open; an
exceptional grant needs two approvers and a legal-process reference. Every grant,
open, and denied open is logged with its scope and purpose. There is no operator
key: without a grant the attempt fails, and a grant never reaches transfers
outside its scope.

`shortcut:` this policy layer currently wraps sealed memos; it is not yet
attached to a shielded transfer type.

Retention is available as a library that applies a per-class policy to off-chain
personal data. Expired records are deleted or anonymized. A deletion leaves a
tombstone holding the ledger reference and a content hash, so on-chain proofs
stay verifiable; keep those tombstones. A legal hold suspends deletion and
anonymization until it is released and the policy is applied again. On-chain
records are never touched.

`shortcut:` retention is not wired into this node's logs or caches yet. Do not
claim automated data expiry until your own storage calls it and the event log is
persisted.

Sanctions screening is available as a provider-agnostic library with a local,
versioned list provider. Its contract is fail-closed: a provider outage denies
settlement, and a potential match is a hold for human review, never an automatic
freeze. Screening is not wired into settlement yet, so do not describe a node as
sanctions-screened until that integration and its operational runbook exist.

## Hybrid account keys

A hybrid account holds an Ed25519 key and an ML-DSA-65 key. Verification requires
both, so you cannot downgrade an account by dropping one signature or one key:
doing so fails as a partial signature. Back up both keys together; losing the
ML-DSA key locks the account just as losing the Ed25519 key would.

`shortcut:` the node does not create hybrid accounts yet. This applies when your
integration calls the hybrid library directly.

## Missing before production

Do not describe this node as production-ready until these exist:

- multi-node consensus networking, validator admission, staking and slashing;
- TLS, OIDC/JWT, mTLS, and secrets management;
- metrics, tracing, structured logs, dashboards, alerts;
- online snapshots, CometBFT blockstore pruning, and migrations;
- validator key separation, HSM/KMS support, slashing and staking;
- audits and a bug-bounty program.
