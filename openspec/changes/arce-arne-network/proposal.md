# Proposal

## Why

The current AnorebelCoin codebase is a single-node, in-memory, proof-of-work learning toy ported from SavjeeCoin. It cannot carry multiple assets, cannot connect nodes, cannot survive a restart, has no tests, uses unmaintained dependencies, and exposes raw panics and custom key encodings that are unsafe for real value. Small teams and interconnected organizations need a shared settlement and communication network that is currency-neutral, auditable, private where required, and usable without operating a bank.

This change replaces the toy with a production-grade, currency-agnostic settlement and team-communication network: **AnorebelCoin (ARCE)** as the native accounting and fee asset, and **AnorebelNetwork (ARNE)** as the public-and-consortium network that carries any registered currency, token, commodity, or point value.

The requested tickers `ARC` and `ARN` already collide with Circle's Arc L1 and AI Rig Complex (ARC) and with unrelated ARN projects. The project keeps the names AnorebelCoin/AnorebelNetwork but uses the unambiguous tickers **ARCE** (AnorebelCoin Eve) and **ARNE** (AnorebelNetwork Eve), with namespaced identifiers such as `arce:mainnet`, `arne:mainnet`, `arne:testnet`, and `arne:consortium/<id>`.

## What Changes

- **BREAKING**: Discard the legacy RebelCoin chain, address format, transaction format, genesis, and wire protocol. No migration or compatibility layer is retained.
- **BREAKING**: Replace the Python-only Flask demo and the Go `main` package with a Go canonical full node (`main` branch) and a full Python node (parity module / `python` branch) that share schemas, test vectors, and wire formats.
- Introduce ARCE as a capped-supply utility asset with tail emission, used for fees and staking, while any registered asset can be transferred and can pay fees through auto-conversion.
- Introduce the ARNE network with public permissionless and consortium/permissioned deployments running the same protocol.
- Add a multi-asset account ledger with an asset registry, supply invariants, nonces, replay protection, and deterministic fee accounting.
- Add an opt-in shielded pool for confidential amounts and asset types, with auditor view keys and selective disclosure.
- Add CometBFT-based BFT proof-of-stake consensus with deterministic finality, validator lifecycle, staking, slashing, and light-client support.
- Add Ed25519 user signatures, BLS12-381 aggregate validator signatures, SHA-256/BLAKE3 hashing, and an optional hybrid ML-DSA (FIPS 204) post-quantum path.
- Add a deterministic WASM execution environment plus declarative transfer templates for escrow, subscriptions, payroll, multisig, and invoices.
- Add payments and encrypted team messaging: invoices, payment intents, batch/payroll transfers, escrow, MLS (RFC 9420) group channels, and encrypted memos.
- Add compliance-ready hooks: FATF Travel Rule/IVMS101, EU MiCA reference profiles, sanctions and KYC provider interfaces, jurisdiction policy engine, audit keys, and retention controls. The protocol is not itself a regulated entity.
- Add interoperability connectors for ISO 20022 (pacs.008/camt.053), ILPv4/Open Payments, and EVM chains.
- Add on-chain parameter governance, timelocked upgrades, treasury, and emergency veto.
- Add a libp2p peer network, deterministic block/tx propagation, DoS protection, and snapshot-based state sync.
- Add embedded KV consensus storage, sparse Merkle state proofs, pruning, snapshots, and an optional Postgres indexer.
- Add versioned REST and JSON-RPC 2.0 APIs, WebSocket subscriptions, OpenAPI, authentication, idempotency, and rate limiting.
- Add HD wallets, passkey/WebAuthn signing, social recovery, hardware-wallet support, and human-readable addresses.
- Add observability, security assurance, fuzzing, supply-chain controls, and incident-response runbooks.
- Add a Go/Python SDK, CLI, code generation, documentation, and examples.
- Add a FastAPI-based web console (explorer, wallet, team workspace) with accessible server-rendered pages.
- Add deployment assets for single binary, Docker Compose, Helm, and Terraform across devnet, testnet, consortium, and mainnet profiles.
- Update all dependencies to maintained, current versions: Go 1.27, Python 3.14, FastAPI/Litestar-class ASGI, coincurve/libsecp256k1, Pydantic 2, Echo v5, CometBFT, and current protobuf/OpenTelemetry stacks. Remove `console-logging`, `pkg-resources`, Flask 1.x, and other unmaintained packages.

## Capabilities

### New Capabilities

- `network-identity`: ARCE/ARNE naming, ticker and namespace rules, network IDs, genesis identity, protocol versioning, and chain registry.
- `cryptography`: canonical signature suites, key derivation, domain separation, hashing, BLS aggregation, and hybrid post-quantum migration.
- `asset-ledger`: multi-asset registry, account state, supply invariants, transfers, fees, nonces, replay protection, and asset metadata standards.
- `shielded-transfers`: opt-in confidential pool, commitments, range proofs, nullifiers, view keys, auditor disclosure, and compliance boundaries.
- `programmability`: deterministic WASM VM, declarative transfer templates, gas metering, ABI, contract lifecycle, and upgrade rules.
- `consensus`: CometBFT application, validator lifecycle, proof-of-stake, finality, epochs, slashing, evidence, and light clients.
- `governance`: on-chain parameter proposals, voting, timelocks, treasury, emergency veto, and upgrade authorization.
- `economics`: ARCE supply, tail emission, fee market, staking rewards, auto-conversion, slashing economics, and treasury flows.
- `payments-and-messaging`: invoices, payment intents, batch/payroll, escrow, MLS group channels, encrypted memos, and delivery semantics.
- `compliance`: FATF Travel Rule/IVMS101, MiCA and ISO 20022 reference profiles, sanctions/KYC interfaces, jurisdiction policy, audit access, and retention.
- `interoperability`: connector framework, ISO 20022 messages, ILPv4/Open Payments, EVM bridge, attestations, and reconciliation.
- `peer-network`: libp2p transport, discovery, gossip, block/tx propagation, DoS resistance, NAT traversal, and state sync.
- `state-storage`: embedded KV state, sparse Merkle proofs, snapshots, pruning, migrations, and optional Postgres indexing.
- `node-api`: versioned REST, JSON-RPC 2.0, WebSocket subscriptions, OpenAPI, auth, idempotency, pagination, and rate limits.
- `wallet-keys`: HD derivation, passkey/WebAuthn signing, social recovery, hardware wallets, address format, and QR/URI schemes.
- `observability`: metrics, tracing, structured logs, health/readiness, audit logs, dashboards, and SLOs.
- `security-assurance`: threat model, secure defaults, supply-chain integrity, fuzzing, audits, disclosure, and incident response.
- `deployment-and-upgrades`: binary/Compose/Helm/Terraform, devnet/testnet/consortium/mainnet profiles, genesis tooling, protocol upgrades, and rollback.
- `cross-language-parity`: Go canonical node, full Python node, shared schemas, conformance vectors, differential testing, and release synchronization.
- `developer-experience`: Go and Python SDKs, CLI, code generation, documentation, examples, and local development workflow.
- `web-console`: FastAPI explorer, wallet, and team workspace with accessible server-rendered pages and real-time updates.

### Modified Capabilities

- None. No prior OpenSpec capabilities exist; the legacy code has no spec coverage.

## Impact

- **Code**: removes `backend/python`, `backend/go`, `frontend`, and the top-level Python package as they exist today; introduces a Go node, a Python node, shared `spec/` schemas and vectors, SDKs, CLI, connectors, web console, deployment assets, and test suites.
- **APIs**: new REST `/v1`, JSON-RPC 2.0, WebSocket, and gRPC/protobuf interfaces. No legacy API compatibility.
- **Data**: new genesis, address format, state schema, and storage layout. No legacy chain or wallet data is retained.
- **Dependencies**: Go 1.27, Python 3.14, CometBFT, libp2p, Pebble/Badger, coincurve, FastAPI or Litestar, Pydantic 2, Echo v5, OpenTelemetry, protobuf, MLS, BLS12-381, ML-DSA. All pins updated and locked.
- **Operations**: new node, validator, connector, indexer, and web processes; new deployment and monitoring footprint.
- **Compliance**: new data flows for identity attestations, sanctions screening, Travel Rule messages, and audit access; deployers remain responsible for their regulatory obligations.
- **Security**: cryptographic key handling, validator operations, bridge custody, and confidential-transfer proofs are high-risk surfaces requiring audits before mainnet value.
- **Compatibility**: clean break. The `main` branch becomes the Go canonical implementation; a `python` branch carries the full Python parity node. Shared schemas and conformance vectors are the synchronization contract.
