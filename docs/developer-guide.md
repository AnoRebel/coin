# Developer guide

## Prerequisites

- Go 1.27+ (`go.mod`)
- Python 3.11+ and [uv](https://docs.astral.sh/uv/)
- Node 26 and [Bun](https://bun.sh/) for `apps/docs` (only to build the docs site)
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
| `internal/memo` | Opt-in sealed memos (X25519 + ChaCha20-Poly1305) |
| `internal/paymenturi` | Strict `arce:`/`arne:` payment URI parsing |
| `internal/preview` | Pre-signing preview and safety warnings |
| `internal/webhook` | Signed webhook delivery, retries, replay checks |
| `internal/compliance/policy` | Deterministic versioned jurisdiction policy engine |
| `internal/compliance/profile` | FATF, MiCA, and ISO 20022 reference profiles |
| `internal/compliance/access` | Scoped, time-bounded auditor grants and access log |
| `internal/compliance/sanctions` | Provider-agnostic sanctions screening, fail-closed |
| `internal/compliance/travelrule` | IVMS101 Travel Rule collection and signed exchange |
| `internal/compliance/vc` | W3C VC 2.0/SD-JWT selective disclosure and revocation |
| `internal/connector` | Connector handshake: versions, assets, health |
| `internal/connector/iso20022` | pacs.008, pain.001, camt.053 mapping and validation |
| `internal/connector/ilp` | ILPv4 prepare/fulfill, Open Payments quotes and grants, HTLC, STREAM |
| `internal/connector/evm` | EVM bridge guard: finality, replay, limits, pause |
| `internal/connector/assetmap` | Versioned external-to-canonical asset mapping |
| `internal/compliance/retention` | Per-class retention, legal holds, deletion/anonymization tombstones |
| `internal/compliance/disclosure` | Grant-scoped memo disclosure with an audit log |
| `internal/compliance/monitoring` | Threshold/velocity alerts, freeze policy, analyst dispositions |
| `internal/crypto/bls` | BLS12-381 signatures with proof-of-possession registry |
| `internal/crypto/suites` | Signature-suite lifecycle: active, deprecated, disabled |
| `internal/crypto/redact` | Secret wrapper that redacts formatting, logs, and JSON |
| `internal/crypto/hybrid` | Hybrid Ed25519 + ML-DSA-65 registration and signatures |
| `internal/crypto/core` | cgo bindings to the shared Rust crypto core |
| `crypto/core` | Rust crypto core and its C ABI |
| `internal/snapshot` | Full/incremental app-store snapshots with digests and restore |
| `internal/indexer` | Optional Postgres read model with reorg-safe, idempotent writes |
| `internal/staking` | Validator lifecycle, stake, delegation, and epoch-boundary set changes |
| `internal/slashing` | Equivocation verification, downtime penalties, and reporter rewards |
| `internal/lightclient` | Commit certificates, validator-set proofs, and state inclusion |
| `internal/admission` | Public join and threshold-based consortium validator approval |
| `internal/workspace` | Roles, permissions, and the audit log |
| `internal/storage` | Pebble and in-memory stores |
| `internal/node` | Mempool, block production, events, faucet |
| `internal/api` | Echo REST `/v1`, JSON-RPC, SSE, auth, rate limit |
| `python/arne` | Full parity node (same module split) |
| `python/arne/rocksstore.py` | RocksDB store with the Go key schema and atomic commits |
| `apps/docs` | Docus documentation site for `coin.anorebel.net` |
| `spec/vectors` | Cross-language conformance anchors |
| `tests` | Go vector tests, Python vector/ledger/API tests |
| `openspec/changes/arce-arne-network` | Requirements, design, task tracker |

## Canonical encoding

Everything consensus-critical uses deterministic CBOR:

- shortest integer/length forms only;
- map keys sorted by encoded length then bytewise;
- no floats, tags, indefinite lengths, or duplicate keys;
- depth limit 64.

`protocol.Encode`/`protocol.Decode` reject non-canonical input. Version-1
envelope and signature maps reject unknown fields; payload maps are open and
each transaction handler validates its own fields. A new envelope field needs
a transaction-version change rather than relying on compatibility.

Go is the source of truth. If Go and Python disagree, fix Python and add a
vector.

Payment intents live at `intent/<id>` in the state tree. Their canonical CBOR
stores the request and, after payment, the payer, transaction hash, and block
time. `create_intent` and intent-linked transfers run within the existing
transaction journal, including simulation rollback. Shared ledger vectors
exercise creation, payment, and a rejected duplicate with identical Go/Python
roots and block hashes. Upgrade every validator before admitting the new
transaction type; older binaries reject `create_intent`.

REST idempotency is an API concern, separate from consensus. Only accepted
transaction submissions are cached, using unwrapped signed bytes and a scope
derived from credential/network/signer/key. A lock serializes cache lookup
and admission. Entries expire after 24 hours; capacity is bounded and never
silently evicts a live key. This cache is neither durable nor shared between
API processes. On-chain intent state remains the permanent payable-once guard.

Refunds add two state-key families: `transfer/<tx-hash>` indexes every direct
`transfer` and `transfer_from`, and `refunded/<original-hash>` accumulates
successful refund amounts. `refund_of` reads the index inside the transaction
journal, so a failed refund leaves both the original entry and the total
unchanged. Because nonce ordering matters, a refund and its original cannot
share a block unless the original appears earlier in the same block. These keys
are consensus state: enabling them changes state roots, so regenerate shared
vectors and upgrade validators together. Batch items and escrow releases are
not indexed.

Subscriptions live at `subscription/<id>`, keyed by
`SubscriptionID(network, payer, payee, asset, payer nonce)`. The record holds
the caps, the current period start, the per-period and cumulative counters, and
the status. `charge_subscription` advances `period_start` by whole periods and
resets `charged_period` before checking limits, so retries land on the same
decision for the same block time. State writes and transfers share the
transaction journal: a rejected charge moves no value and leaves the counters
untouched. They are consensus state and change state roots, so regenerate
vectors and upgrade validators together. There is no scheduler in the node;
the payee submits every charge.

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
No route or an underfunded pool → `NO_FEE_ROUTE`. An optional payload field
`max_arce_fee` caps the ARCE amount (converted fee, or the native fee itself);
exceeding it fails with `LIMIT_EXCEEDED` before any balance changes.

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

## Multi-node and mixed-language devnet

`internal/multinode` builds N CometBFT homes that share one genesis and peer
with each other. `Cluster.Start` takes a factory so the caller decides which
application each node runs:

```go
cluster, err := multinode.Init(root, "arne:devnet", 2, 300*time.Millisecond, 0, 0)
nodes, err := cluster.Start(func(i int) (abci.Application, func() error, error) {
    if i == 0 {
        app, err := consensus.NewPythonApp("uv run python -m arne.abci", "arne:devnet", chain)
        return app, app.Close, err
    }
    n, err := node.New(nodeConfig, cluster.Genesis, store)
    if err != nil {
        return nil, nil, err
    }
    return consensus.NewApp(n, "arne:devnet", chain, 500), nil, nil
}, logger)
defer multinode.Stop(nodes)
```

Ports are free when both bases are 0; otherwise node `i` listens on
`base+i`. Each node gets `node-<i>/config`, `data`, and its own validator and
node keys. `AllowDuplicateIP`, `AddrBookStrict=false`, and `PexReactor=false`
make a loopback cluster work without discovery.

`internal/consensus.PythonApp` implements `abci.Application` by piping
line-delimited JSON to a child process. The command must implement
`Info`, `InitChain`, `CheckTx`, `PrepareProposal`, `ProcessProposal`,
`FinalizeBlock`, `Commit`, and `Query`; `python -m arne.abci` does. Every
request includes the network and chain ID. The bridge compares the Python app
hash with what CometBFT expects, so a divergence halts rather than forks.

The Python app accepts `--genesis <file>` and `--data-dir <dir>`; without
`--data-dir` it keeps the ledger in memory. Run the command from the repository
root (or give an absolute project path) so `uv` resolves the environment.

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

The conformance runner loads `spec/vectors/{cbor,keys,smt,ledger,tx,memo,uri}.json` and
requires both languages to reproduce every byte:

- CBOR encode/decode and non-canonical rejection
- mnemonic → key → per-network address
- tree roots and inclusion/exclusion proofs
- genesis, block state roots, tx roots, block hashes, receipts
- transaction envelope bytes, hashes, signatures, and closed-schema rejection
- memo key derivation and sealed ciphertext bytes
- payment URI parsing, warnings, canonical form, and rejection cases

To prove a runner is real, temporarily corrupt one vector value; both suites
must fail. Do not commit the corruption.

## Storage

Pebble keys: `b<height>` for blocks, `meta/last`, `state/current`, `meta/schema`.
Each value is `SHA-256(payload) || JSON payload`; a checksum mismatch is
`ErrCorrupt` and the node refuses to load. A block write and its state snapshot
are committed in one atomic batch, so a crash leaves the last fully committed
height.

`internal/storage.SchemaVersion` is the newest schema this binary understands;
an unversioned store reports 0. Add a step to `schemaMigrations` with a unique
`Version`, `Name`, and replay-safe `Up` (plus `Down` when rollback is
meaningful). Each step runs in a batch that also writes `meta/schema`, so an
interrupted migration leaves the previous version. Startup calls
`Store.Migrate(SchemaVersion)`; `arned migrate` does the same on demand. Never
delete `meta/schema` to "reset" a store — it hides a half-applied migration.

`Store.Prune(before)` deletes blocks below a height in one synced batch; the
state snapshot and last-height marker are untouched. The node calls it after
each commit when `Config.KeepBlocks` is non-zero, and evicts the matching
in-memory blocks and receipts. Archive mode is `KeepBlocks == 0`.

The Python node uses `FileStore` (one checksummed snapshot file plus one JSON
file per block). Its schema version lives in `schema.json`, with
`SCHEMA_VERSION` and `_MIGRATIONS` mirroring the Go registry; opening a store
auto-migrates. `shortcut:` a file store is not RocksDB; upgrade when block
volume or concurrent writers matter.

`internal/snapshot` exports the app store as gzipped JSON. `Create` reads the
last block and state, optionally from a full base, and stamps a SHA-256 digest
over the whole document. `Verify` recomputes the digest and state root, checks
block heights, `tx_root` values, and predecessor links. `Restore` requires a
nonexistent destination, writes into a temporary sibling store, migrates it to
the current schema, and renames it into place. Incrementals require the full
base for both verification and restore. `shortcut:` snapshots are built in
memory; stream the state map if production block volume makes that costly.

`internal/indexer` is an optional Postgres read model. `CatchUp` finds the
indexed height, walks back over any stored block that no longer matches the
node, applies blocks in order, and stops with `ErrPruned` when the node no
longer has a needed block. `Postgres.Apply` deletes the orphaned suffix and
upserts in one transaction, so retries are idempotent. `Async` serializes
catch-up on a worker: `Submit` only signals it, a 5-second ticker retries after
an outage, and schema creation is lazy, so a database that is down at startup
does not stop the node. The indexer is not consensus state; a lost or stale
index is rebuilt from the node. `shortcut:` the read model stores block headers
and transaction positions only; add receipts and balances when an explorer
needs them.

## Validator staking

`internal/staking` (mirrored by `python/arne/staking.py`) is the deterministic
staking state machine. A validator registers with an address, a BLS consensus
key, a matching proof of possession, a commission, and at least the minimum
self-bond. Registration verifies the PoP against the network and validator
address, so the same rogue-key rule as `internal/crypto/bls` applies.

`AdvanceEpoch` is the only place voting power changes. It requires the exact
first height of the next epoch, releases completed unbonding entries, moves
eligible pending validators to `active`, moves ineligible ones back to
`pending`, and drops jailed, unbonding, and unbonded validators from the set.
Voting power is the validator's bonded stake, capped at `max_power_bps` of the
active set's total stake; the cap never increases anyone's power. Mid-epoch
delegations and undelegations are visible immediately in `Stake` but not in
`VotingPower` until the next epoch.

`spec/vectors/staking.json` drives both implementations through activation,
mid-epoch delegation, the voting-power cap, self-bond removal, jail/unjail,
unbonding, and rejection cases. Keys and PoPs come from `spec/vectors/bls.json`.

`shortcut:` the package tracks stake accounting, not ledger balances, and the
node still gives CometBFT a static validator set. Wire `AdvanceEpoch` into
`FinalizeBlock` validator updates before treating on-chain staking as live.

## Slashing

`internal/slashing` (mirrored by `python/arne/slashing.py`) accepts evidence,
applies the penalty through `staking.Penalize`, and jails the validator. Every
penalty is split into a reporter share and a treasury remainder.

Double-sign evidence contains two signed votes at the same height and round
with different block hashes. Both signatures are checked against the
validator's registered BLS key over `VoteMessage`, so changing a hash, height,
round, or signer invalidates the evidence. Evidence must arrive within
`window_blocks` of the equivocation height; stale, duplicated, malformed, and
forged evidence is rejected before any stake changes.

Downtime evidence is structural: a complete window with `total` equal to its
span, a missed share at or above `downtime_threshold_bps`, and a window of at
least `downtime_min_blocks`. The consensus adapter must derive `missed` from
finalized commit data; the library cannot prove an absence from signatures, so
do not accept downtime evidence from an untrusted caller.

`spec/vectors/slashing.json` covers valid, stale, duplicate, forged, and
below-threshold cases in both implementations. `shortcut:` penalties are
computed but not moved in the ledger, and the engine is not yet fed by
CometBFT evidence.

## Light client

`internal/lightclient` (mirrored by `python/arne/lightclient.py`) verifies a
commit certificate against a trusted validator set. Every signature is checked
over `Message` (network, height, round, block hash, state root, next validator
root). Unknown and duplicate signers are rejected, and the signed power must
exceed two-thirds of the trusted total. A certificate at or below the trusted
height is rejected as outdated.

Validator-set roots are sparse Merkle roots over `validator/<id>` entries whose
values bind the BLS public key and power. `Prove` and `VerifyMember` give an
inclusion proof for one validator; `Advance` accepts a next set only when its
root matches the certificate and its height is exactly one greater. State
inclusion reuses `internal/smt` against the certificate's state root.

`spec/vectors/lightclient.json` covers a valid certificate, insufficient
quorum, an outdated set, a forged signature, an unknown signer, and a duplicate
signer. `shortcut:` the node does not yet produce these certificates; CometBFT
finality is still the live source of truth.

## Validator admission

`internal/admission` (mirrored by `python/arne/admission.py`) implements the two
network admission modes. Public mode admits any validator that passes staking
registration; consortium mode requires distinct approvals from the policy's
authorized approvers.

`Approve` records the approver and stamps the height when the threshold is
reached. Unknown approvers, duplicate approvals, and changes after approval are
rejected. `Approved()` returns the map consumed by `staking.SetAdmission`; a
nil map means public admission, and a non-nil map restricts activation to the
listed addresses at the next epoch. That is how an unapproved consortium
validator stays pending while an approved one enters the active set.

`spec/vectors/admission.json` covers public join, a threshold-two consortium,
duplicate and unauthorized approvals, and unapproved rejection. The Go and
Python tests also drive the staking epoch to prove admission gates activation.

`shortcut:` approval records live in this library, not in ledger state; submit
them through a governance transaction before calling admission authoritative.

## Workspace roles and audit

`internal/workspace` (mirrored by `python/arne/workspace.py`) owns roles,
permissions, and the audit log. `Load(path)` reads `members` and `principals`
maps from JSON, loads `<path>.audit.jsonl`, and saves membership changes
atomically with mode `0600`. The file contains API keys; treat it as a secret.

The permission matrix is data, not scattered checks: `Authorize(member,
action)` is the single source of truth. `Require` audits denials, while mutating
methods audit successes with the previous and new role. `SetRole`/`Remove`
reject non-managers, block admins from touching owners, and keep at least one
owner. The API wraps payment submit/read and workspace routes with
`workspaceAction`, so a missing or unmapped key is `401` and a denied role is
`403`. Payment submission also appends an `accepted` entry with the tx hash.

`shortcut:` the audit log is held in memory as well as appended to disk; stream
it when audit volume makes that costly. Membership storage is a JSON file, not
an external IAM system.

## Sanctions screening

`internal/compliance/sanctions` (mirrored by `python/arne/sanctions.py`) wraps a
provider and turns its answer into a settlement decision:

```go
provider, _ := sanctions.NewListProvider(sanctions.List{
    Name: "ofac-demo", Version: "2026-10-01", UpdatedAt: now,
    Entries: []sanctions.Entry{{ID: "e1", Address: "arce-devnet1…"}},
})
screener, _ := sanctions.New(provider)
d := screener.Screen(ctx, sanctions.Subject{Address: "arce-devnet1…"})
// d.Outcome: allow | hold | deny; d.Provider, d.ListVersion, d.Matches, d.Score
```

- `clear` -> `allow`; `potential_match` -> `hold` with `RequiresReview: true`;
  any provider error or unknown result -> `deny` with result `unavailable`.
  Screening never returns an error, so a caller cannot settle by ignoring one.
- A potential match is a review hold, never an automatic freeze. Do not turn
  `hold` into a balance lock without a policy that authorizes it.
- The decision records the provider, list version, matched entry IDs, and score.
  Persist the decision with the transfer; the list itself can change later.
- `ListProvider` matches addresses case-insensitively and names/aliases after
  lowercasing and collapsing whitespace. Exact normalized match only; near
  names are `clear`, so pair this with a fuzzy provider if you need one.
- Entry IDs are derived from the normalized address and name when omitted:
  first 8 bytes of `SHA-256("arne-sanctions-entry-v1" || 0 || address || 0 || name)`.
  Keep explicit IDs for list entries that must stay stable across edits.
- `UnavailableProvider` is an explicit fail-closed provider for tests or a
  disabled deployment. Both suites read `spec/vectors/sanctions.json`.

`shortcut:` screening is a library only; settlement does not call it yet. Wire
it into regulated asset transfers together with the policy engine before
claiming screening coverage.

## Travel Rule exchange

`internal/compliance/travelrule` (mirrored by `python/arne/travelrule.py`)
collects IVMS101-shaped originator and beneficiary data and exchanges it above a
threshold. The data never enters the ledger or consensus state; the package
validates it, signs an envelope, and hands that envelope to a transport you
supply:

```go
policy := travelrule.Policy{Threshold: 1000} // base units, strict >
decision, err := travelrule.Exchange(ctx, policy, transfer, key, transport, now)
// decision.Required, decision.Status: exchanged | below_threshold | unavailable
```

- `Policy.Requires` is strict: exactly at the threshold is below it. A threshold
  of 0 means every positive amount qualifies.
- `Validate` requires network, asset, reference, positive amount, originator and
  beneficiary name and account, and both VASP IDs. Country, when present, is
  ISO 3166-1 alpha-2 uppercase. Optional names and addresses are bounded.
- `Seal` signs `SHA-256("arne-travelrule-v1" || 0 || canonical-CBOR(transfer))`
  with the sender's Ed25519 key. `Open` checks the version, signature, and
  payload, so a receiver attributes the data to `Sender` regardless of how the
  transport authenticated the connection.
- A transport error produces `unavailable` with the cause in `detail`; the
  caller holds or rejects the transfer according to its policy and records the
  exception. Below-threshold decisions are still returned so they can be logged.
- `Transport` is the authenticated channel: mTLS, an authenticated HTTPS
  endpoint, or an internal queue. Authentication is not implied by the envelope
  signature; the signature proves who authored the data, not who received it.

`spec/vectors/travelrule.json` covers a qualifying exchange, exactly-at and
below-threshold amounts, an unavailable counterparty, and invalid country, VASP,
and amount cases. Both suites verify the same sender key and signature, so the
canonical message and signature are cross-language anchors.

`shortcut:` this is a library only; transfers do not call it yet, and there is
no HTTP endpoint for counterparty lookup. Wire it into regulated asset transfers
with the policy engine and FATF profile before claiming Travel Rule coverage.

## Verifiable credentials

`internal/compliance/vc` (mirrored by `python/arne/vc.py`) verifies W3C
Verifiable Credentials 2.0 using the SD-JWT compact form. The issuer signs a
payload whose claims are salted and committed into `_sd`; the holder presents
only the disclosures a verifier needs:

```go
credential, err := vc.Issue(
    map[string]any{"country": "TZ", "accredited": true},
    vc.IssueOptions{Issuer: "did:example:issuer-1", ID: "cred-1",
        Type: "https://coin.anorebel.net/credentials/kyc",
        IssuedAt: now, ExpiresAt: now + 365*24*3600},
    issuerKey,
)
presented := vc.Present(credential, "country")
result, err := vc.Verify(presented, issuerKey.Public, vc.VerifyOptions{
    Now: now, Require: map[string]any{"country": "TZ"}, Disclose: []string{"country"},
})
// result.Claims == {"country": "TZ"}; accredited stays hidden
```

- The header is fixed (`alg: EdDSA`, `typ: vc+sd-jwt`). Payload, disclosures, and
  salts are canonical JSON, so Go and Python produce identical signatures.
- `_sd` holds base64url SHA-256 commitments to `[salt, name, value]`. Every
  presented disclosure must match a commitment; unreferenced disclosures,
  duplicate claims, and malformed salts are rejected.
- `Verify` checks the issuer signature, `now >= exp`, the revocation list, and
  every required claim before returning anything. `Disclose` narrows the result
  to the claims the verifier asked for, so a jurisdiction check does not yield
  the accreditation flag.
- Revocation is a list of credential IDs (`jti`) from the issuer's status list.
  `exp` is required; there is no indefinite credential.
- Credentials are off-chain. Store only a credential reference and hash in
  application records, and nothing but references on-chain.

`spec/vectors/vc.json` pins one issuer key and credential; Go and Python both
reproduce the exact credential and the same disclosed results. Cases cover
jurisdiction-only, accreditation-only, no claims, an undisclosed required claim,
a value mismatch, expiry at the boundary, revocation, and a tampered signature.

`shortcut:` presentations are not holder-bound: there is no KB-JWT, so a
presented credential proves the issuer's claims but not who is presenting it.
Add key binding before using a credential for authorization. Issuer DID
resolution and status-list bitstrings are also out of scope; pass the issuer's
public key and revoked IDs from your resolver.

## Connector handshake

`internal/connector` (mirrored by `python/arne/connector.py`) is the contract
every external rail implements before any quote or settlement:

```go
type Adapter interface {
    Name() string
    Versions() []uint32          // protocol versions this rail speaks
    Assets() []string            // canonical asset IDs it supports
    Health(context.Context) Health
}

session, err := connector.Negotiate(ctx, adapter, connector.Request{
    Versions: []uint32{2, 3}, Assets: []string{"arce:testnet/usdc"}, Network: "arne:testnet",
})
// session.Version is the highest common version; session.Assets is the
// intersection, sorted. A degraded adapter still negotiates.
```

- Capabilities are static and validated every call: empty names, version 0,
  duplicate versions or assets, and empty asset lists are rejected, so a
  misconfigured adapter cannot claim support it cannot honour.
- The request must name at least one version and one asset. No shared version is
  `unsupported protocol version`; no shared asset is `unsupported asset`.
- Health status is `healthy`, `degraded`, or `unhealthy`. Only `unhealthy`
  blocks negotiation; an unknown status is invalid. A health check error is the
  adapter's responsibility and must surface as `unhealthy`.
- Asset IDs are canonical ledger IDs, matched exactly and case-sensitively.
  External symbol mapping is a separate, versioned concern; do not treat a
  rail's ticker as a canonical ID.
- The session names the connector, version, assets, and network. Concrete
  adapters take their operations from the session rather than re-reading
  configuration, so a negotiated capability set cannot drift mid-flow.

`spec/vectors/connector.json` covers highest-common version, degraded health,
missing version and asset, unhealthy and unknown health, duplicate capabilities,
and empty requests. Both suites read it.

`shortcut:` this package negotiates only; there is no quote, transfer, or retry
loop yet. Those arrive with the ISO 20022, ILP, and EVM adapters.

## ISO 20022 mapping

`internal/connector/iso20022` (mirrored by `python/arne/iso20022.py`) maps ARNE
transfers to and from three ISO 20022 messages:

| Message | Direction | Use |
| --- | --- | --- |
| `pacs.008.001.13` | build and parse | Customer credit transfer to or from a bank |
| `pain.001.001.12` | build and parse | Payment initiation from a corporate account |
| `camt.053.001.13` | build and parse | Account statement with balances and entries |

```go
raw, err := iso20022.BuildPacs008(iso20022.Transfer{
    MessageID: "ARNE-MSG-0001", EndToEndID: "ARNE-E2E-0001",
    CreatedAt: "2026-11-03T10:00:00Z",
    Amount:    iso20022.Amount{Currency: "TZS", Value: "1500.50"},
    Debtor:    iso20022.Party{Name: "Amina Hassan", Account: "DE89370400440532013000",
        Agent: "DEUTDEFF", Country: "TZ"},
    Creditor:  iso20022.Party{Name: "John Mwangi", Account: "DE89370400440532013001",
        Agent: "DEUTDEFF500", Country: "KE"},
    Remittance: "invoice 42",
})
parsed, err := iso20022.ParsePacs008(raw) // identical model
```

- Amounts are decimal **strings** with an ISO 4217 currency; nothing is parsed
  into a float. At most 13 integer and 5 fraction digits, and the value must be
  positive.
- Text limits are rune counts, not bytes: 35 for message/end-to-end IDs, 140 for
  names and remittance, 4..34 uppercase alphanumerics for accounts, 8 or 11
  characters for a BIC, ISO 3166-1 alpha-2 for country, RFC 3339 for
  `created_at`, and a real calendar date for a booking date.
- Each message carries exactly one transfer or statement. A batch is refused
  rather than partially settled, and `CountTransactions` reports what an inbound
  message claims so a caller can reject it before parsing.
- `pain.001` cross-checks `CtrlSum` against the instructed amount; a mismatch is
  an error, because a reconciliation break must not be silently accepted.
- Optional blocks are omitted entirely when empty. Emitting `<Nm></Nm>` or an
  empty `<Ustrd>` would be invalid ISO 20022, so the builder drops the parent
  element instead.
- `ParsePacs008` and friends validate the parsed model, so a syntactically valid
  but semantically wrong message (lowercase country, mismatched currency) is
  rejected with the offending field named.

`spec/vectors/iso20022/` holds the three canonical messages, and Go and Python
both build those exact bytes and parse them back to the same model.
`spec/vectors/iso20022.json` also lists 19 malformed mutations (batch counts,
bad currency, lowercase country or account, short BIC, bad timestamp, zero
amount, over-long ID, control-sum mismatch, bad balance type, direction, status,
date, currency mismatch, both parties on one entry, empty statement, wrong
message type); both suites apply the same edit and expect the same error text.

`shortcut:` the mapping is a pure translation layer: it does not choose which
canonical asset backs a fiat currency, does not persist messages, and each
message is single-transaction. Wire it to `internal/connector` asset mapping and
a durable message log before claiming a live bank rail.

## ILPv4 and Open Payments

`internal/connector/ilp` (mirrored by `python/arne/ilp.py`) covers the settlement
primitives for an ILP rail:

```go
condition, _ := ilp.Condition(fulfillment)   // SHA-256 of a 32-byte preimage
prepare := ilp.Prepare{
    Amount: 1500, ExpiresAt: now + 60, Condition: condition,
    Destination: "g.arne.testnet.amina",
}
if err := prepare.Validate(now); err != nil { /* expired, bad address */ }
revealed, err := prepare.Fulfill(now, fulfillment) // fails on a wrong preimage
```

- `ValidateAddress` enforces the ILP shape: at most 1023 bytes, at least two
  segments, each 1..64 characters of `[A-Za-z0-9_~-]`, with one trailing dot
  allowed for a prefix.
- The execution condition is `SHA-256(fulfillment)` over exactly 32 bytes.
  `Fulfill` re-checks the condition, so a relayer cannot substitute a preimage.
- `Quote.Validate(now)` requires a positive debit and receive amount, an
  optional positive fee, and `now < ExpiresAt`. `Grant.Check(now, kind, action)`
  validates the token, access types (`incoming-payment`, `quote`,
  `outgoing-payment`), actions, and expiry before answering.
- `Escrow` is the HTLC. `Claim(now, fulfillment)` works only strictly before
  expiry and only with the right preimage; `Refund(now)` works only at or after
  expiry and only while held. A claim that lands before expiry always wins, and
  a second claim or a refund after a claim is a state error.
- `Stream.Apply(receipt)` accepts a STREAM receipt only when it advances the
  sequence and does not reduce the cumulative total, so replayed or reordered
  receipts are refused.

Times are unix seconds in the model; an ILP adapter converts to and from ISO
8601 at the wire boundary.

`spec/vectors/ilp.json` pins one preimage and condition and drives 26 cases:
quote valid and at expiry, prepare valid, expired, wrong preimage, bad
destination, grant allowed and denied by action and type, grant expiry, claim
success and at expiry, claim twice, wrong preimage, refund before/at expiry and
after a claim, stream success, replay, regression and foreign stream, reject
code validation, and address shapes.

`shortcut:` this is a library, not a running connector: there is no transport,
no peer routing, no liquidity account, and no persistence. Go's escrow is a
mutable pointer; Python's is a frozen dataclass whose `claim`/`refund` return
the new escrow. Wire it to a transport and a durable escrow table before moving
real value.

## EVM bridge

`internal/connector/evm` (mirrored by `python/arne/evm_bridge.py`) is the guard
in front of a lock/mint or burn/release relayer. It never holds keys and never
talks to a chain; it decides whether an observed event may execute, then records
that decision so the relayer acts exactly once:

```go
bridge, err := evm.New(evm.Config{
    ChainID: 1, Finality: 12,
    Limits: []evm.Limit{{Asset: "arce:testnet/arce", Max: 2000, Window: 60}},
})
err = bridge.Submit(evm.Message{
    Direction: evm.Inbound, ChainID: 1, TxHash: "0x…", LogIndex: 0,
    Asset: "arce:testnet/arce", Amount: 1500,
    Sender: "0x…", Recipient: "amina", Confirmations: 12,
}, now)
```

- The message ID is deterministic: the first 16 bytes of
  `SHA-256("arne-bridge-v1" || 0 || chainID || 0 || txHash || 0 || logIndex)`.
  Two logs in one transaction are different messages, and an ID that does not
  match the event is rejected rather than trusted.
- `Confirmations` must reach `Finality` (inclusive). A reorg below the
  threshold cannot execute, and a later retry of the same event is a replay.
- The replay set is permanent for the process and survives pause/resume. Submit
  is not safe for concurrent use: serialize it, because replay and limit
  decisions are stateful.
- Limits are per asset over a rolling unix-second window. A failed submission
  charges nothing, so a rejected message does not consume capacity.
- `Pause` blocks every submission with the reason in the error; `Resume` clears
  it. Pausing is the emergency stop for a suspected bridge fault; it does not
  reverse an executed message.
- Input shape is checked: direction, chain, `0x` + 64 lowercase hex transaction
  hash, non-empty accounts, positive amount.

`spec/vectors/evm_bridge.json` pins one message ID and drives 16 ordered cases
against one bridge: accepted message, replay, same transaction with another log
index, below finality, wrong chain, bad hash, zero amount, unknown direction,
tampered ID, rate limit, pause, submission while paused, double pause, resume,
double resume, and window rollover.

`shortcut:` confirmed events are held in memory only; restarting the process
forgets replays and windows. Persist the seen set and windows (or the messages
themselves) before running a relayer in production, and wire pause to an
operator key.

## External asset mapping

`internal/connector/assetmap` (mirrored by `python/arne/assetmap.py`) maps a
rail-specific identifier to a canonical ARNE asset, and back again for audit:

```go
registry, err := assetmap.New(1, []assetmap.Mapping{
    {
        External: assetmap.External{Rail: assetmap.EVM, ChainID: 1,
            Address: "0xA0b8…eB48"},
        Canonical: "arce:testnet/usdc", Decimals: 6,
    },
    {
        External: assetmap.External{Rail: assetmap.Fiat, Scheme: "iso4217", Code: "usd"},
        Canonical: "arce:testnet/usdc", Decimals: 2,
        MergePolicy: "usd-fiat-rail",
    },
})

got, err := registry.Resolve(assetmap.External{Rail: assetmap.EVM, ChainID: 1,
    Address: "0xa0b8…eB48"})          // EVM and fiat casing both normalize
list := registry.Reverse("arce:testnet/usdc") // both rails
```

- Two rails: `evm` (`chain_id` plus `0x` and 40 lowercase hex) and `fiat`
  (`scheme: "iso4217"` plus a three-letter uppercase code). Anything else is
  invalid, and a fiat mapping carrying a chain or address is refused.
- Normalization happens before duplicate detection: EVM addresses are
  lowercased and fiat codes uppercased, so casing cannot smuggle in a second
  mapping for the same reference.
- Two external references may share one canonical asset only with an explicit
  `MergePolicy` on every mapping after the first. Without it the set is
  ambiguous and is rejected, because a caller must not get a different answer
  depending on table order.
- Every version is a **full snapshot**, and versions apply strictly in sequence
  (`1`, then `2`, …). Resolving against an old version is supported, so a past
  settlement can be audited against the table that was current then. Re-applying
  a version and skipping a version are both errors.
- `Reverse` returns a list, because one canonical asset can legitimately have a
  mapping per rail or chain.

`spec/vectors/assetmap.json` drives 24 cases against one registry: EVM and fiat
lookups, mixed-case input, unknown reference and rail, reverse before and after
an apply, a full-snapshot v2 that adds Polygon without dropping Ethereum,
historical v1 vs v2 resolution, a version gap, duplicate external references,
missing and over-long merge policies, zero version, empty set, over-long
canonical, excessive decimals, bad fiat code, bad EVM address, an EVM mapping
with a fiat field, and a fiat mapping with a chain. Both implementations must
agree on every result and error.

`shortcut:` this is a lookup table, not an issuer. It does not prove that a
canonical asset exists, does not check decimals against an on-chain contract,
and keeps versions in memory only. Persist the registry and verify decimals
against the source chain before moving value.

## Data retention and legal holds

`internal/compliance/retention` (mirrored by `python/arne/retention.py`) applies
one policy per off-chain data class:

```go
m, _ := retention.NewManager([]retention.Policy{
    {Class: "kyc", Days: 365, Action: retention.Delete},
    {Class: "support", Days: 30, Action: retention.Anonymize, AnonymizeFields: []string{"email", "name"}},
})
_ = m.Add(retention.Record{ID: "r1", Class: "kyc", CreatedAt: now, LedgerRef: "tx-hash"})
res := m.Apply(now + 366*86400) // Deleted / Anonymized / Retained / Held / Tombstones
```

- `Apply` is idempotent per record: a deleted record is gone, an anonymized one
  stays with the configured fields cleared, and both are logged.
- Deletion writes a `Tombstone` with the record's `LedgerRef` and a content hash
  over the deleted fields. Keep the tombstone: it is what keeps an on-chain hash
  or proof verifiable after the personal data is gone.
- Content hash: `SHA-256("arne-retention-record-v1" || 0 || id || 0 || class ||
  0 || created_at || 0 || ledger_ref || sorted fields)`.
- On-chain records are rejected (`ErrOnChain`); retention never rewrites the
  ledger. A record with no policy is retained and logged as such.
- A legal hold suspends both deletion and anonymization for its record. Hold IDs
  derive from `SHA-256("arne-hold-v1" || 0 || record || 0 || reason || 0 ||
  placed_by || 0 || placed_at)` (first 16 bytes) when omitted. Release, then run
  `Apply` again to expire the record.
- Every apply, hold, and release appends a sequenced event; read them with
  `Events(after, limit)`.
- Both suites read `spec/vectors/retention.json`, which pins the tombstone hash
  and hold ID across languages.

`shortcut:` retention is a library only and in memory; it is not wired to
workspace audit logs, grant logs, webhooks, or idempotency caches. Persist
records and the event log in your own store before relying on expiry.

## Grant-scoped disclosure

`internal/compliance/disclosure` (mirrored by `python/arne/disclosure.py`) is the
only supported way to open sealed memos on someone else's behalf. There is no
operator or manager key, and no method that turns a transfer hash into plaintext
without a grant:

```go
m := disclosure.NewManager()
_ = m.AddMemo("tx-hash", sealedMemo) // ciphertext only; never decrypted here

g, _ := m.Grant(disclosure.Grant{
    Kind: disclosure.User, Subject: "alice", Scope: []string{"tx-hash"},
    From: 1790000000, Until: 1798000000, Purpose: "invoice review",
    GrantedBy: "alice", GrantedAt: 1790000000,
})
plain, err := m.Open("alice", g.ID, "tx-hash", aliceSeed, 1790000100)
```

- `Open` checks, in order: the grant exists, the actor is its subject, the time
  is inside `[From, Until)`, the transfer is in scope, the memo exists, and the
  seed decrypts it. Any failure returns an error and a `denied` event; a grant
  never substitutes for the recipient's key.
- A grant is not a master key: it reaches only the transfers it names. There is
  no bulk or wildcard open, and no API that lists plaintext.
- `Kind: User` is a disclosure grant from the data owner: no approvers, no legal
  process. `Kind: Exceptional` requires at least two distinct approvers and a
  legal-process reference, and its grant event records scope, approvers, purpose,
  and who granted it. Every open event repeats that context.
- Grant IDs derive from `SHA-256("arne-disclosure-grant-v1" || 0 || kind || 0 ||
  subject || 0 || purpose || 0 || from || 0 || until || 0 || sorted scope || 0 ||
  sorted approvers)` (first 16 bytes). Both suites share
  `spec/vectors/disclosure.json`.
- The manager stores sealed memos only; it never stores seeds. Keep seeds in the
  wallet or HSM and pass one per open.

`shortcut:` this is the policy layer for disclosure, not the shielded pool: no
shielded transfer type exists yet, so today it wraps sealed memos only. Wire it
in when shielded transfers land, and keep the rule that no key opens them all.

## Validator BLS keys

`internal/crypto/bls` (mirrored by `python/arne/bls.py`) implements BLS12-381
signatures with the proof-of-possession ciphersuite and a registry that refuses
keys which have not proved possession:

```go
secret, _ := bls.SecretFromSeed(walletSeed)
pub, _ := bls.PublicKeyOf(secret)
pop, _ := bls.SignPoP(secret, "arce:devnet", "validator-1")

reg, _ := bls.NewRegistry("arce:devnet")
_ = reg.Register("validator-1", pub, pop)
_ = reg.VerifyAggregate([]string{"validator-1", "validator-2"}, blockHash, aggregateSig)
```

- `SecretFromSeed` hashes the seed under `arne-bls-key-v1` and rehashes until the
  digest is a valid scalar; both languages produce the same key.
- A PoP signs `SHA-256("arne-bls-pop-v1" || 0 || network || 0 || validator || 0 ||
  public_key)`, binding the key to one network and validator ID.
- `Register` rejects a missing or invalid PoP (`ErrRogueKey`), a duplicate ID, and
  an empty network. `VerifyAggregate` fails with `ErrNoPoP` if any ID is not
  registered, so a rogue key cannot be slipped into an aggregate. The raw
  `VerifyAggregate(pubs, …)` helper is curve math only; do not feed it keys from
  untrusted parties.
- `spec/vectors/bls.json` contains the rogue-key construction: the forged
  aggregate passes raw math and is rejected by the registry. Both suites consume
  it.

`shortcut:` this is the signature layer only; the node still uses CometBFT's
validator keys. Wire it into the validator lifecycle before claiming BLS
consensus.

## Secret redaction

`internal/crypto/redact` (mirrored by `python/arne/redact.py`) wraps key material
so it cannot leak through formatting or logs:

```go
key := redact.New(secretBytes)
fmt.Println(key)                 // [REDACTED]
slog.Info("sign", "key", key)    // "key":"[REDACTED]"
json.Marshal(key)                // error: redact: refusing to serialize secret material
_ = key.EqualBytes(candidate)    // crypto/subtle constant-time compare
key.Zero()                       // overwrite in place
```

- `String`, `GoString`, `LogValue`, and the Python `__repr__`/`__str__`/
  `__format__` all return `[REDACTED]`; `Bytes()` returns a copy.
- JSON encoding fails instead of writing a placeholder, so a secret cannot be
  persisted by accident.
- `Equal` and `EqualBytes` use constant-time comparison; length differences
  return false.
- Tests in `internal/crypto/redact`, `internal/crypto/bls`, `internal/memo`, and
  the Python equivalents assert that errors and logs do not contain seeds,
  secrets, or memo plaintext.

`shortcut:` the wrapper is opt-in; not every key path uses it yet. Prefer it for
any new code that logs or serializes key material.

## Signature suites

`internal/crypto/suites` (mirrored by `python/arne/suites.py`) gates account
registration and verification by suite lifecycle:

```go
r, _ := suites.New([]suites.Suite{
    {ID: "ed25519", Algorithm: "ed25519", Status: suites.Active, Verify: ed25519Verify},
    {ID: "ed25519-v1", Algorithm: "ed25519", Status: suites.Deprecated, Verify: ed25519Verify},
    {ID: "rsa-legacy", Algorithm: "rsa", Status: suites.Disabled},
})
_ = r.CanRegister("ed25519")      // ok
_ = r.Verify("ed25519-v1", …)     // ok: history still verifies
_ = r.CanRegister("ed25519-v1")   // ErrDeprecated
_ = r.Verify("rsa-legacy", …)     // ErrDisabled
```

- An `active` suite can register and verify. A `deprecated` suite verifies
  existing signatures but refuses new accounts. A `disabled` suite never
  verifies, even for history.
- Unknown IDs fail with `ErrUnknown`; a missing verifier on a non-disabled suite
  is rejected at construction.
- `spec/vectors/suites.json` pins the register/verify matrix and both suites
  consume it.

`shortcut:` the ledger still accepts only Ed25519 envelopes; the registry is the
policy layer that a future multi-suite envelope will call.

## Hybrid signatures

`internal/crypto/hybrid` (mirrored by `python/arne/hybrid.py`) pairs Ed25519 with
ML-DSA-65 so an account survives the day a large quantum computer can break
Ed25519:

```go
err := hybrid.Register(hybrid.Key{Ed25519: edPub, MLDSA: mlPub})
sig, err := hybrid.Sign(edPriv, mlPriv, message)
err = hybrid.Verify(key, message, sig)
```

- Registration needs both public keys; a key with one algorithm is rejected with
  `ErrInvalid`.
- `Sign` is deterministic in both languages: Ed25519 is deterministic and ML-DSA
  runs in its deterministic mode, so the same seed and message reproduce the
  same envelope. `spec/vectors/hybrid.json` is consumed by both test suites.
- `Verify` requires both components. A signature with one component stripped is
  `ErrPartial`; a corrupted or mismatched component is `ErrVerify`. There is no
  code path that falls back to Ed25519 alone.
- ML-DSA signs under the `arne-hybrid-v1` context, which keeps these signatures
  from being replayed in another protocol.

`shortcut:` this is a library only; accounts and transaction envelopes still
carry Ed25519. Do not describe an account as hybrid until the envelope and
account registration call these functions.

## Shared Rust crypto core

`crypto/core` is the single implementation of SHA-256, BLAKE3, Ed25519,
BLS12-381, and ML-DSA-65 behind a C ABI (`crypto/core/arne_crypto.h`). Go reaches
it through `internal/crypto/core`; Python through `python/arne/core.py` (cffi).

```sh
make test-crypto   # cargo test, C smoke test, Go bindings, Python bindings
```

```go
sum, _ := core.SHA256([]byte("abc"))
seed := make([]byte, core.Ed25519SeedLen)
sig, _ := core.Ed25519Sign(seed, message)
ok, _ := core.Ed25519Verify(publicKey, message, sig)
```

- The Go bindings live behind the `cryptocore` build tag, so `go build ./...`
  works on a machine without Rust. `make crypto` builds the static library first.
- `spec/vectors/core.json` holds known SHA-256/BLAKE3/Ed25519 vectors; the BLS and
  ML-DSA vectors are the existing `bls.json` and `hybrid.json`. Rust, Go, and
  Python all assert the same bytes, including the deterministic ML-DSA signature.
- The C smoke test compiles `c_smoke.c` against the static library and exercises
  every primitive, so a broken ABI fails before the language bindings do.
- Python raises `CoreUnavailable` when the library is absent; its tests skip
  instead of silently passing.

`shortcut:` the node still uses the pure-Go and pure-Python crypto packages for
transactions. The core is ready to replace them but is not wired into the ledger
or consensus paths.

## Transaction monitoring

`internal/compliance/monitoring` (mirrored by `python/arne/monitoring.py`) turns
rules into alerts and records what an analyst decided:

```go
m, _ := monitoring.NewMonitor([]monitoring.Rule{
    {ID: "large", Type: monitoring.Threshold, Amount: 10_000, Severity: monitoring.High, Enabled: true},
    {ID: "burst", Type: monitoring.Velocity, Count: 3, Window: 3600, Severity: monitoring.High,
        Freeze: true, Enabled: true},
}, []string{"burst"}) // only "burst" may freeze

alerts, _ := m.Evaluate(monitoring.Transaction{
    ID: "tx-hash", From: "arce1…", Asset: "usd:devnet", Amount: 12_000, At: now,
})
_, _ = m.Dispose(alerts[0].ID, monitoring.Report, "SAR filed", "ana", now)
```

- Threshold rules fire at or above `Amount`; velocity rules fire when a sender
  reaches `Count` transfers inside `Window` seconds (half-open window, current
  transfer included). `Asset` empty means any asset.
- `Evaluate` is idempotent per transaction ID and keeps per-sender history in
  memory. Alerts carry a deterministic ID:
  `SHA-256("arne-alert-v1" || 0 || rule || 0 || tx)` first 16 bytes.
- A rule with `Freeze: true` applies a freeze only if its ID is listed in the
  freeze policy passed to `NewMonitor`. Otherwise the alert is escalated with
  `Escalated: true` and `FreezeApplied: false`. No rule freezes funds by itself.
- `Dispose` closes an open alert with `no_action`, `escalate`, or `report`, and
  requires both a reason and a reviewer. A closed alert cannot be reopened, so
  the disposition trail stays append-only.
- Both suites read `spec/vectors/monitoring.json`, which pins alert IDs and the
  freeze/escalate split.

`shortcut:` monitoring is a library only; the node does not evaluate transfers
or apply freezes. Wire it in with the policy engine and persist alerts before
treating it as case management.

## Auditor access grants

`internal/compliance/access` (mirrored by `python/arne/access.py`) restricts
auditors to named assets and a half-open window:

```go
r, _ := access.Load("grants.json")   // empty path keeps grants in memory
g, err := r.Grant(access.Grant{
    Auditor: "dave", Assets: []string{"usd:devnet"},
    From: 1790000000, Until: 1798000000, Purpose: "Q1 audit",
})
_, err = r.Check("dave", "usd:devnet", now) // ErrExpired / ErrOutOfScope / ErrNoGrant
```

- `Grant` normalizes assets (trim, dedupe, sort) and derives the ID from
  `SHA-256("arne-grant-v1" || 0 || auditor || 0 || purpose || 0 || from || 0 || until || 0 || assets)`,
  truncated to 16 bytes. Identical grants collide by design (`ErrExists`).
- `Check` picks the first covering grant by ID, treats `at < From` as
  `not-yet-active` and `at >= Until` as `expired`, and logs every outcome with
  the grant ID and asset. There is no sweeper: expiry is evaluated at check time.
- Grants persist to the given file atomically with mode `0600`; the log appends
  to `<path>.log.jsonl`. `Load` tolerates a missing file and replays the log.
- The API auto-creates an in-memory registry whenever a workspace is configured,
  so an auditor with no grant is denied. Owners and admins are not scoped.
- Both suites read `spec/vectors/audit-grants.json`; add a case for any change
  to matching or expiry.

`shortcut:` grants are local to one node and not on-chain yet; replicate the
grants file across API nodes until governance-signed grants land.

## Jurisdiction policy engine

`internal/compliance/policy` implements the deterministic engine behind
compliance hooks. A policy set is a network-wide document with one or more
versions; each version activates at a block height and remains effective until
the next one:

```json
{
  "network": "arne:devnet",
  "versions": [{
    "version": 1,
    "activation_height": 0,
    "default": {"decision": "allow", "reason": "no restrictions"},
    "rules": [{
      "id": "high-value-hold",
      "priority": 100,
      "decision": "hold",
      "reason": "travel rule review",
      "requirements": ["travel_rule"],
      "match": {"assets": ["arce:devnet"], "min_amount": 1000000000, "max_amount": 0}
    }]
  }]
}
```

- Decisions are `allow`, `deny`, or `hold`; the result also carries the rule ID,
  policy version, activation height, reasons, and requirements.
- A rule matches on asset, originator/beneficiary jurisdiction, transfer type,
  and an inclusive base-unit amount range (`max_amount: 0` is unbounded). Empty
  lists match anything.
- The highest-priority matching rule wins; ties break by ascending rule ID. If no
  rule matches, the version's explicit `default` applies. Before the first
  activation the engine returns `deny` with rule `no-active-policy`.
- `Parse` rejects unknown fields, duplicate versions or activation heights,
  duplicate rule IDs, invalid decisions, and inverted amount ranges.
- `Evaluate(height, ctx)` selects the version active at that height, so a
  historical decision can be reproduced with its original policy version.
  `History()` returns every retained version.

The Python parity implementation is `python/arne/policy.py`. Both suites read
`spec/vectors/policy.json`; keep Go and Python behavior aligned when changing
the matcher.

## Compliance reference profiles

`internal/compliance/profile` (mirrored by `python/arne/profile.py`) provides
selectable FATF, MiCA, and ISO 20022 field sets. A profile is required fields
plus a disclaimer, never a compliance certification:

| Profile | Required fields | Optional |
| --- | --- | --- |
| `fatf` | `originator_name`, `originator_account`, `beneficiary_name`, `beneficiary_account` | addresses, `counterparty_vasp`, `purpose` |
| `mica` | `iso24165`, `iso17442`, `white_paper` | `white_paper_hash`, `casp_lei`, `order_book_venue` |
| `iso20022` | `uetr`, `end_to_end_id`, `instruction_id` | `purpose_code`, `remittance_information` |

```go
cfg, err := profile.New("arne:devnet", profile.FATF, profile.MiCA)
err = cfg.ValidateAsset(metadata)              // MiCA fields and formats
err = cfg.ValidateTransfer(profile.Transfer{   // FATF above threshold only
    Jurisdiction: "us", Amount: 1500, Fields: travelRule,
})
err = cfg.ValidatePayment(isoFields)           // ISO 20022 identifiers
```

- FATF applies only when `amount > threshold`. `DefaultFATFThreshold` is `1000`
  base units; override per jurisdiction with `JurisdictionThreshold`, because
  the trigger is a fiat value and therefore asset- and jurisdiction-specific.
  The comparison is strict, so an amount equal to the threshold is exempt.
- `iso24165` is checked syntactically (9–32 uppercase alphanumerics), not against
  the DTI registry. `iso17442` is 20 uppercase alphanumerics. `uetr` must be a
  lowercase UUIDv4; `end_to_end_id` and `instruction_id` are 1–35 characters.
- Validation returns a `ValidationError` naming the profile and the first failing
  field, so callers can report it directly instead of guessing.
- `Config.Validate` rejects unknown or duplicate profiles, an empty profile list,
  a zero threshold, and bad jurisdiction overrides. `Config.Disclaimers()`
  returns the disclaimer text for every enabled profile.
- Both suites read `spec/vectors/profiles.json`; add a vector for every new
  rule. Disclaimers must contain the word "deployer" so no profile can be
  presented as legal advice.

`shortcut:` the profiles are validation libraries only; asset registration and
payment submission do not call them yet.

## Known shortcuts

Search the tree for `shortcut:`:

- failed transactions are free;
- SMT leaf padding is O(256-depth) per leaf rather than a cached subtree hash;
- Python persistence is file-based unless RocksDB is selected;
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
