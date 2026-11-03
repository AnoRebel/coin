# Design

## Context

See `proposal.md` for motivation. The repository currently contains two unrelated educational implementations (Python and Go) of a SavjeeCoin-style proof-of-work chain, a Flask demo, no persistence, no networking, no tests, no OpenSpec capabilities, and dependencies that are years out of date. There is no live chain, no user data, and no compatibility obligation.

The design target is a currency-agnostic settlement and team-communication network that runs as a public network or a consortium network on the same protocol. Go is the canonical full node on `main`; Python is a full parity node on the `python` branch. Both implementations must agree byte-for-byte on consensus-critical behavior or the network can fork by implementation.

## Goals / Non-Goals

**Goals:**

- A single protocol that supports public permissionless and consortium/permissioned deployments.
- Native multi-asset settlement for any registered fiat, crypto, commodity, point, or security asset.
- Deterministic finality with BFT proof-of-stake and a clear validator lifecycle.
- Opt-in confidentiality without making transparency impossible or creating a universal backdoor.
- Compliance hooks that deployers can activate without the protocol becoming a regulated intermediary.
- End-to-end encrypted team messaging alongside payments.
- Two full implementations (Go and Python) that are provably equivalent on consensus-critical paths.
- Operational simplicity from a laptop devnet to a multi-org consortium.

**Non-Goals:**

- No initial coin offering, token sale, or investment product.
- No custody of fiat or customer funds by the protocol.
- No legal advice or claim of regulatory approval.
- No anonymity guarantee beyond the shielded pool's documented properties.
- No EVM compatibility or Solidity execution.
- No proof-of-work mining.
- No mobile native apps in the first release; responsive web and SDKs only.
- No general-purpose file storage or media hosting.

## Decisions

### 1. Naming, identity, and namespacing

- Project names: **AnoRebelCoin** and **AnoRebelNetwork**.
- Tickers: **ARCE** (AnoRebelCoin Eve) and **ARNE** (AnoRebelNetwork Eve). This avoids the heavily contested `ARC`/`ARN` tickers while preserving the requested names.
- Canonical asset ID: `arce:<network-id>`, for example `arce:mainnet`.
- Canonical network ID: `arne:<class>/<network-id>`, for example `arne:mainnet`, `arne:testnet`, `arne:devnet`, `arne:consortium/acme-pay`.
- CometBFT chain IDs: `arne-mainnet-1`, `arne-testnet-1`, `arne-devnet-1`, `arne-consortium-<id>-1`.
- Display aliases are allowed; storage, signing, and settlement always use canonical IDs.

**Alternative considered:** keep `ARC`/`ARN`. Rejected because Circle's Arc L1 and AI Rig Complex already use ARC, and ambiguity in wallets and exchanges is a security risk.

### 2. Process and repository architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                        ARNE node process(es)                        │
│                                                                     │
│  ┌──────────────┐   ABCI 2.0    ┌───────────────────────────────┐  │
│  │  CometBFT    │◄─────────────►│  Application state machine    │  │
│  │  consensus   │   protobuf     │  Go (main) / Python (branch)  │  │
│  │  P2P, mempool│                │  ledger, contracts, governance│  │
│  └──────┬───────┘                └──────┬────────────────────────┘  │
│         │                               │                           │
│         │ QUIC/TCP                      │                           │
│  ┌──────▼───────┐                ┌──────▼────────────────────────┐  │
│  │  Peer network │                │  State storage (Pebble/Rocks) │  │
│  │  gossip, sync │                │  SMT, snapshots, pruning      │  │
│  └──────────────┘                └──────┬────────────────────────┘  │
│                                         │                           │
│  ┌──────────────┐  ┌──────────────┐  ┌──▼──────────┐  ┌──────────┐  │
│  │ REST/JSON-RPC│  │  WebSocket   │  │  Indexer    │  │Connectors│  │
│  │  Echo v5 /   │  │  SSE/WS      │  │  Postgres   │  │ ISO/ILP/ │  │
│  │  FastAPI     │  │              │  │  optional   │  │ EVM      │  │
│  └──────────────┘  └──────────────┘  └─────────────┘  └──────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

CometBFT handles consensus, peer discovery, gossip, block propagation, and light-client support. Both language implementations plug into the same consensus engine through ABCI 2.0, which is language-neutral protobuf over a socket or gRPC. This is the key decision that makes a full Python node tractable: Python does not need a production-grade BFT or libp2p stack, and both implementations share the same consensus and networking code.

The Go node runs CometBFT in-process by default and supports an external CometBFT process. The Python node always runs CometBFT as a separate process and connects over ABCI. A Docker Compose profile packages `cometbft + arned` and `cometbft + arne-py` identically.

**Alternative considered:** build consensus and P2P in both languages. Rejected: it doubles the highest-risk code and guarantees eventual divergence.

**Alternative considered:** use the Cosmos SDK. Rejected: the multi-asset, shielded-pool, compliance, and messaging state machines are bespoke, and the SDK would add framework constraints without removing the custom work.

### 3. Repository layout

```
/
├── cmd/arned/                 # Go node, CLI entrypoints
├── internal/
│   ├── app/                   # ABCI application, state transitions
│   ├── ledger/                # accounts, assets, fees, escrow, allowances
│   ├── shield/                # confidential pool
│   ├── consensus/             # CometBFT wiring, validator module
│   ├── governance/            # proposals, voting, timelock
│   ├── contracts/             # WASM runtime, templates, ABI
│   ├── compliance/            # policy engine, providers, profiles
│   ├── messaging/             # MLS channels, delivery, workspaces
│   ├── connectors/            # ISO 20022, ILP, EVM
│   ├── p2p/                   # CometBFT config, peer policy
│   ├── storage/               # Pebble, SMT, snapshots, migrations
│   ├── api/                   # REST, JSON-RPC, WebSocket
│   └── indexer/               # Postgres indexer
├── spec/                      # shared schemas, vectors, OpenAPI, protobuf
│   ├── proto/
│   ├── cbor/
│   ├── vectors/
│   └── openapi/
├── crypto/core/               # shared Rust crypto core (C ABI)
├── python/                    # Python full node and SDK (also on `python` branch)
├── sdk/go/                    # Go SDK
├── web/                       # FastAPI console
├── connectors/                # connector process templates
├── deploy/                    # compose, helm, terraform
├── docs/                      # versioned documentation
└── tests/                     # integration, conformance, load, chaos
```

Shared schemas and vectors are the synchronization contract between branches. Generated code is never hand-edited.

### 4. Ledger model: account core plus shielded pool

Transparent state is account-based because payments, payroll, escrow, allowances, governance, and team workflows are naturally expressed as accounts. Every account stores a nonce, an authorization policy, and a map of `asset_id -> balance` in integer base units.

Confidentiality is a separate shielded UTXO pool. Notes are commitments to `(asset_id, amount, owner, randomness)`. Transfers consume notes and create notes, revealing only nullifiers and commitments. This hybrid avoids forcing every user into UTXO UX while still giving confidential transfers the properties they need.

State is committed in a sparse Merkle tree with namespaced keys:

```
account/<address>/nonce
account/<address>/auth
balance/<address>/<asset_id>
asset/<asset_id>
escrow/<escrow_id>
allowance/<owner>/<spender>/<asset_id>
contract/<address>/code_hash
contract/<address>/state/<key>
validator/<operator>
proposal/<proposal_id>
shield/commitment/<commitment>
shield/nullifier/<nullifier>
shield/liability/<asset_id>
```

Supply conservation is enforced per asset per block: `sum(balances) + escrow + shielded_liabilities + burned = minted`. A violation is consensus-critical and halts the node.

### 5. Consensus and validator economics

- Engine: CometBFT with ABCI 2.0, deterministic single-block finality.
- Target block time: 2 seconds; commit timeout 1.5 seconds.
- Epoch: 1,000 blocks (about 33 minutes at target).
- Unbonding period: 21 days; evidence window: 21 days.
- Minimum self-bond: 100,000 ARCE; minimum validator stake: 100,000 ARCE.
- Per-validator voting-power cap: 10% of active stake, governance-adjustable between 5% and 20%.
- Slashing: double-sign 5% of bonded stake and permanent jailing; downtime 0.1% and temporary jailing; reporter reward 10% of the slash, treasury 60%, burn 30%.
- Public mode: any validator meeting stake and registration requirements can join.
- Consortium mode: validator admission requires the consortium charter's approval; the same consensus and state-transition rules apply.
- Light clients use CometBFT commit certificates and validator-set proofs, plus ARNE state proofs.

**Alternative considered:** HotStuff/Bullshark. Rejected for the first release because CometBFT already provides production-grade BFT, light clients, and tooling. The application is not coupled to CometBFT internals, so a future consensus engine can be added behind the ABCI boundary.

### 6. Cryptography and cross-language parity

Canonical suites:

| Purpose | Primitive | Notes |
|---|---|---|
| User signatures | Ed25519 | Default suite `ed25519-v1` |
| Validator votes | BLS12-381 aggregate | Proof-of-possession required |
| Post-quantum option | ML-DSA-44/65 (FIPS 204) | Hybrid suite `ed25519+ml-dsa-v1` |
| Consensus hash | SHA-256 | State roots, block commitments |
| Content addressing | BLAKE3 | Blobs, snapshots, documents |
| Key derivation | BIP-39 + SLIP-0010 Ed25519 | `m/44'/9999'/...` |
| Canonical encoding | Deterministic CBOR (RFC 8949) | Signed structures |
| Transport encoding | Protobuf | ABCI, gRPC, internal services |

Cryptographic operations that must be bit-identical are implemented once in a small Rust core (`crypto/core`) with a stable C ABI, and called from Go via cgo and from Python via cffi. This covers BLS12-381 aggregation, ML-DSA, shielded-pool proofs, and MLS. Ed25519 and hashing use each language's audited standard library where vectors prove equivalence.

Deterministic CBOR is used for signed structures because protobuf serialization is not canonical across implementations. Every signed message includes a domain tag, network ID, chain ID, suite ID, purpose, and expiry.

**Alternative considered:** implement every primitive independently in Go and Python. Rejected: subtle divergence in BLS aggregation, proof verification, or ML-DSA encoding would be catastrophic and hard to detect.

### 7. Programmability: WASM plus declarative templates

General contracts run in Wasmtime, configured for determinism: threads, SIMD, and non-deterministic features disabled; NaN canonicalization enabled; fuel-based gas metering. Go uses `wasmtime-go`; Python uses `wasmtime-py`; both bind the same engine and the same metered configuration, which keeps gas accounting identical.

Declarative templates are native state transitions, not contracts. They cover escrow, subscriptions, payroll, multisig, invoices, and conditional payments. Templates are audited once and produce the same ledger effects as their reference contracts. This gives small teams a safe path that does not require writing or auditing WASM.

Contract lifecycle: deploy (code hash), instantiate (state + ABI), execute, query, migrate (authority + storage compatibility), freeze. Contracts are immutable by default. Events are indexed. Reproducible builds and source attestation are supported.

**Alternative considered:** EVM. Rejected because it brings Solidity's security history and a different account model, and the multi-asset ledger does not need EVM compatibility.

### 8. Shielded pool

- Commitments: Pedersen commitments over BLS12-381 (or a dedicated curve chosen by the proof system) for amount and asset.
- Range proofs: Bulletproofs-style, batched per transaction.
- Membership: a zk-SNARK over the note commitment tree, using a transparent-setup proof system (Halo2-style) to avoid a trusted ceremony.
- Nullifiers: derived from the note's spending key and position; recorded in a compact nullifier set.
- View keys: incoming viewing key, full viewing key, and scoped auditor key.
- Pool solvency: a public commitment to liabilities per asset plus a reserve proof.
- Policy: per-asset and per-jurisdiction predicates can require allowlist membership, a Travel Rule attestation, or a disclosure key before a shielded transfer is accepted.
- Proofs are generated and verified by the shared Rust core, so Go and Python agree exactly.

**Alternative considered:** confidential accounts with ZK proofs over account balances. Rejected for the first release because UTXO notes are a better-understood privacy primitive and avoid leaking account-level activity.

### 9. Messaging and workspaces

- Group messaging uses MLS (RFC 9420) with the OpenMLS implementation wrapped by the shared Rust core.
- Channels are MLS groups; add/remove/update operations are commits that rotate keys.
- Payment references, notices, and file pointers are messages; file bytes stay off-chain.
- Delivery is at-least-once with stable message IDs and deduplication; offline catch-up is bounded by channel retention.
- Workspaces have owner, admin, member, and auditor roles, enforced server-side and recorded in the audit log.
- The server never holds group plaintext or user private keys.

### 10. Compliance architecture

Compliance is a policy layer, not a consensus backdoor:

- On-chain: policy version commitments, asset eligibility flags, credential hashes, auditor grant scopes, and decision receipts.
- Off-chain: PII, KYC documents, sanctions lists, case files, and Travel Rule payloads live in the deployer's systems, not on the ledger.
- Policy engine: deterministic evaluation of versioned rules, returning allow/deny/hold with a rule ID.
- Providers: pluggable interfaces for sanctions screening, KYC/VC verification, Travel Rule transport, and transaction monitoring.
- Reference profiles: FATF Travel Rule/IVMS101, EU MiCA metadata and order-book fields, ISO 20022 message mapping.
- Exceptional access: user-granted view keys, governance-approved legal requests, or policy predicates. No global decryption key exists.
- Retention: per-data-class, with legal hold and deletion/anonymization of off-chain data.

### 11. Interoperability

Connectors are separate processes with a narrow, signed, idempotent protocol:

- ISO 20022: `pacs.008`, `camt.053`, and `pain.001` mapping with field-level validation and reconciliation.
- ILPv4/Open Payments: quotes, payment pointers, STREAM, and HTLC settlement.
- EVM: lock-and-mint / burn-and-release with finality checks, replay protection, rate limits, exposure caps, and emergency pause.
- Reconciliation: every external operation has a correlation ID and is matched to on-chain state; unmatched items become exceptions.

**Alternative considered:** embed connectors in the node. Rejected: connectors have different failure, credential, and scaling profiles; isolation limits blast radius.

### 12. Storage, indexing, and proofs

- Go: Pebble. Python: RocksDB. Both use the same key schema and the same SMT algorithm, so the state root is identical even though the storage engine differs.
- Snapshots: protobuf manifest + compressed chunks, tagged with height, state root, protocol version, and checksum. Full and incremental.
- Pruning: configurable; archive mode retains everything; recent proofs retained.
- Indexer: optional Postgres, idempotent, reorg-safe before finality, never a consensus dependency.
- Migrations: versioned, idempotent, with tested rollback or restore.

**Alternative considered:** use Postgres for consensus state. Rejected: block execution latency and operational coupling.

### 13. API and clients

- REST `/v1` with OpenAPI 3.1, cursor pagination, idempotency keys, and stable typed errors.
- JSON-RPC 2.0 for node operations and automation.
- WebSocket/SSE subscriptions with resumable cursors.
- Auth: API keys with scopes, OIDC/JWT for users, mTLS for admin.
- Go node uses Echo v5; Python node and console use FastAPI. Both serve the same OpenAPI contract.
- SDKs are generated from shared schemas; the CLI wraps the SDK and supports JSON output.

**Alternative considered:** gRPC-only. Rejected: browser and partner integration would be harder; gRPC is retained internally.

### 14. Key management and wallet UX

- Addresses: Bech32m with network-specific HRP (`arce` mainnet, `arce-testnet`, `arce-devnet`, `arce-consortium`), encoding a version byte and a truncated public-key hash.
- HD derivation: BIP-39 + SLIP-0010 Ed25519, path `m/44'/9999'/<account>'/<change>'/<index>'`.
- Passkeys: WebAuthn credential authorizes a scoped session or a specific transaction through account abstraction; the root key never leaves the wallet.
- Social recovery: guardian threshold plus timelock plus owner cancellation.
- Hardware/external signers: a common signer interface with human-readable confirmation.
- Session keys: scoped, expiring, spending-limited.
- Payment URIs: `arce:` and `arne:` with network, asset, amount, memo, expiry; strict validation.

### 15. Governance and economics

- On-chain proposals for parameters, upgrades, treasury, validator admission, asset policy, compliance policy, and crypto suites.
- Voting: stake-weighted with a cap; consortium role-weighted by charter.
- Timelock: 7 days for ordinary changes, 30 days for constitutional invariants.
- Constitutional invariants: supply cap, value conservation, crypto suite floor, shielded-pool solvency.
- Emergency committee: multi-party, short-lived pause/veto, auto-expiry, on-chain record.
- Treasury: on-chain inflows and outflows with budget categories and proposal references.

Genesis defaults (governance-configurable within hard bounds):

| Parameter | Default |
|---|---|
| ARCE cap | 1,000,000,000 ARCE |
| Decimals | 8 |
| Genesis allocation | 45% community/ecosystem, 20% treasury, 15% contributors (1-year cliff, 4-year vest), 10% validator bootstrap, 10% liquidity |
| Tail emission | 2% per year after cap, hard bound 0.5%–5% |
| Base fee | EIP-1559-style, target 50% block utilization |
| Fee split | 50% burn, 30% validators, 20% treasury |
| Priority fee | 100% to proposer |
| Travel Rule threshold | USD 1,000 equivalent, jurisdiction-overridable |
| Minimum fee | 0.001 ARCE equivalent in the fee asset |

These values are genesis parameters; changing them follows governance, and the cap and conservation rules are constitutional.

### 16. Security and assurance

- Threat model maintained per component and reviewed each major release.
- Secure defaults: TLS, auth, least privilege, encrypted storage, rate limits, audit logging.
- Signed releases, SBOM, reproducible builds, dependency scanning, and patch SLAs.
- Continuous fuzzing and property tests for parsers, crypto, consensus transitions, mempool, proofs, and contracts.
- Differential fuzzing between Go and Python.
- Independent audits before mainnet value for consensus, crypto, shielded pool, bridge, runtime, and compliance.
- Coordinated disclosure, bug bounty, incident-response runbooks, and annual tabletop exercises.
- Secrets in HSM/KMS/secret manager; validator keys separated from API and console workloads.

### 17. Deployment

- Single Go binary for devnet.
- Docker Compose for small teams: CometBFT, node, indexer, console, optional connectors.
- Helm for consortium/production with separate validator, sentry, API, indexer, console, and connector workloads.
- Terraform modules for one major cloud initially, with versioned modules and no embedded secrets.
- Network profiles: devnet, testnet, consortium, mainnet; separate genesis, seeds, and release channels.
- Upgrades activate at a coordinated height after governance and supermajority binary adoption; non-consensus services use canary/rolling deployment.
- Rollback rehearsed on a production-like network; irreversible migrations require a verified restore.

### 18. Cross-language release process

1. Change shared schema or vectors in `spec/`.
2. Regenerate Go and Python types and clients.
3. Run conformance and differential tests.
4. Tag `main` and mirror the tag on `python` with the same protocol version.
5. Publish the compatibility matrix and SBOM for both.
6. Block release on any conformance failure, state-root divergence, or API contract mismatch.

## Risks / Trade-offs

- **[Two full implementations double the attack surface and maintenance]** → Shared schemas, generated code, conformance vectors, differential fuzzing, and a hard release gate. Python is a full node but not the canonical performance target.
- **[CometBFT coupling]** → Keep the application behind ABCI and avoid CometBFT-specific state; a future engine can replace it.
- **[Bridge and connector risk]** → Isolate connectors, require finality checks, replay protection, exposure caps, emergency pause, independent audits, and insurance/limits before mainnet value.
- **[Privacy versus compliance tension]** → Opt-in shielded pool, scoped view keys, public solvency proofs, policy predicates, and no universal backdoor. Deployers choose their jurisdiction profile.
- **[Regulatory uncertainty]** → The protocol provides hooks, not legal status; deployers remain responsible. Reference profiles are versioned and clearly disclaimed.
- **[WASM determinism and gas parity]** → Same Wasmtime engine, same configuration, fuel metering, conformance vectors, and differential execution tests.
- **[Proof-system maturity and performance]** → Transparent setup, versioned circuits, shared Rust verifier, bench budget, and a staged launch with limits.
- **[Key compromise]** → HSM/KMS, separated keys, multisig for treasury and bridge, social recovery, and rehearsed incident response.
- **[Token naming and market confusion]** → ARCE/ARNE plus namespaced IDs and explicit metadata; no claim of affiliation with Circle Arc or AI Rig Complex.
- **[Supply and allocation choices are contentious]** → Genesis defaults are published, vesting is enforced, and constitutional invariants require a supermajority and long timelock to change.
- **[Python performance]** → Documented floors, benchmarks in CI, and the option to run Python as a validating node with lower throughput or as an observer/parity node.
- **[Dependency and supply-chain risk]** → Pinning, SBOM, signed releases, reproducible builds, vulnerability scanning, and patch SLAs.
- **[Complexity of one umbrella change]** → Tasks are grouped by capability and each group lands its own tests and docs; integration checks are separate from unit work.

## Migration Plan

There is no legacy data to preserve. The migration is a controlled replacement:

1. Freeze the legacy code paths and archive the old branches/tags for reference only.
2. Create the new repository layout and shared `spec/` schemas and vectors.
3. Implement the Go node on `main` behind feature flags, starting with the ledger, crypto, and state root.
4. Implement the Python parity node on `python`, consuming shared schemas and vectors.
5. Stand up a local devnet and a public testnet; run both implementations as validators.
6. Complete audits and incident-response rehearsals.
7. Launch a consortium reference network before mainnet value.
8. Launch mainnet with conservative limits, monitoring, and a published genesis.

Rollback: because no legacy state is migrated, rollback means returning to the prior release binary and its compatible state; irreversible migrations require a verified backup restore. The legacy chain is not restarted.

## Open Questions

These do not change the specs, approach, or task breakdown and can be resolved before the relevant release:

- Final genesis allocation percentages and vesting cliffs, to be fixed in the genesis file after community review.
- The first cloud provider for Terraform modules; the module interface is provider-neutral.
- The exact Bulletproof/Halo2 circuit parameters and trusted-setup alternative after the cryptography spike.
- The first consortium design partner and its charter weights.
- Whether the first public testnet uses a faucet with proof-of-personhood or rate-limited API keys.
