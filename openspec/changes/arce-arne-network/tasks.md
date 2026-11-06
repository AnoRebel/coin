# Tasks

## 1. Repository foundation and dependency modernization

- [ ] 1.1 Replace the legacy tree with the target layout (`cmd/`, `internal/`, `spec/`, `python/`, `sdk/`, `web/`, `connectors/`, `deploy/`, `docs/`, `tests/`) and verify `find` shows every directory
- [ ] 1.2 Initialize the Go module `github.com/anorebel/anorebelcoin` with Go 1.27, Echo v5, CometBFT, Pebble, and OpenTelemetry; verify `go build ./...` succeeds
- [ ] 1.3 Initialize the Python project with `uv`, Python 3.14, FastAPI, Pydantic 2, coincurve, cbor2, and pytest; verify `uv sync && uv run pytest` succeeds
- [x] 1.4 Add `air` and `uv` development workflows plus `Makefile` targets (`dev`, `test`, `lint`, `generate`, `devnet`); verify each target runs from a clean checkout
- [x] 1.5 Remove `console-logging`, `pkg-resources`, Flask 1.x, and other unmaintained pins; verify no legacy import remains with `rg`
- [x] 1.6 Add Apache-2.0 `LICENSE`, `NOTICE`, `CONTRIBUTING.md`, `SECURITY.md`, and `CODE_OF_CONDUCT.md`; verify links resolve
- [ ] 1.7 Add CI for Go and Python with lint, unit tests, race tests, coverage, SBOM, and dependency scanning; verify CI passes on a clean branch
- [x] 1.8 Add pre-commit hooks and editor configuration; verify `pre-commit run --all-files` passes

## 2. Shared protocol schemas and conformance vectors

- [ ] 2.1 Define canonical deterministic CBOR schemas for transaction envelopes, signatures, assets, accounts, fees, escrow, allowances, proposals, and messages in `spec/cbor/`; verify Go and Python round-trip fixtures byte-identically
- [ ] 2.2 Define protobuf schemas for ABCI, internal services, and gRPC in `spec/proto/`; verify `buf lint` and generated code compile
- [ ] 2.3 Define OpenAPI 3.1 for REST `/v1` and JSON-RPC method schemas in `spec/openapi/`; verify `openapi-generator validate` passes
- [ ] 2.4 Create language-neutral vectors for encoding, hashing, key derivation, signatures, and domain separation in `spec/vectors/`; verify both implementations consume them
- [ ] 2.5 Create state-transition vectors for transfers, fees, escrow, allowances, governance, and shielded notes; verify both implementations produce the expected state roots
- [x] 2.6 Add a vector runner to Go and Python test suites; verify a deliberately corrupted vector fails both runners

## 3. Cryptography core

- [x] 3.1 Implement the shared Rust crypto core with Ed25519, BLS12-381, ML-DSA, and SHA-256/BLAKE3 bindings and a C ABI; verify `cargo test` and a C smoke test pass
- [x] 3.2 Add Go cgo bindings and Python cffi bindings to the crypto core; verify identical outputs for every primitive vector
- [x] 3.3 Implement canonical CBOR signing, domain separation, network/chain binding, and replay rejection; verify cross-network and cross-purpose vectors fail
- [x] 3.4 Implement BIP-39 and SLIP-0010 Ed25519 derivation with the ARNE path; verify the same mnemonic yields identical keys and addresses in Go and Python
- [x] 3.5 Implement BLS proof-of-possession registration and aggregate verification; verify rogue-key and missing-PoP vectors are rejected
- [x] 3.6 Implement hybrid Ed25519+ML-DSA registration and verification; verify downgrade and partial-signature vectors fail
- [x] 3.7 Implement the signature-suite registry with active/deprecated/disabled states; verify deprecated suites verify history and reject new accounts
- [x] 3.8 Add constant-time and secret-redaction tests; verify no key material appears in logs or errors

## 4. Asset ledger and account state

- [x] 4.1 Implement asset registration with canonical IDs, classes, decimals, ISO 4217/24165/LEI metadata, and immutable identity; verify valid and invalid registration vectors
- [x] 4.2 Implement supply policies (`fixed`, `capped`, `mintable`, `burnable`) and conservation checks; verify cap, fixed-supply, and burn vectors
- [x] 4.3 Implement account state with per-asset balances, nonce, authorization policy, and account creation; verify independent balances and overspend rejection
- [x] 4.4 Implement atomic transfers with fee charging and rollback; verify success and every failure path leaves state unchanged
- [x] 4.5 Implement fee payment in ARCE and in registered assets with auto-conversion and slippage limits; verify fee-cap, no-route, and slippage vectors
- [x] 4.6 Implement nonce ordering, expiration, replay rejection, and mempool replacement; verify duplicate and expired transactions fail
- [x] 4.7 Implement escrow with release, refund, arbiter, and timeout; verify double-release and unauthorized release fail
- [x] 4.8 Implement bounded, revocable allowances; verify over-allowance and post-revocation transfers fail
- [x] 4.9 Implement atomic batch transfers; verify one failing item rolls back the whole batch
- [x] 4.10 Implement balance, supply, escrow, and burned queries with state proofs; verify inclusion and exclusion proofs

## 5. State tree, storage, snapshots, and indexing

- [x] 5.1 Implement the canonical sparse Merkle tree with namespaced keys and deterministic roots; verify Go and Python roots match every vector
- [x] 5.2 Implement Pebble storage with atomic block batches, checksums, and crash recovery; verify a simulated crash recovers the last committed height
- [x] 5.3 Implement RocksDB storage for the Python node with the same key schema; verify Python state roots match Go
- [x] 5.4 Implement full and incremental snapshots with manifests, checksums, and restore; verify tampered snapshots are rejected
- [x] 5.5 Implement configurable pruning and archive mode; verify recent proofs work and pruned history returns a clear error
- [x] 5.6 Implement the optional Postgres indexer with idempotent, reorg-safe writes; verify catch-up, orphan removal, and node operation with the indexer down
- [x] 5.7 Implement versioned, idempotent schema migrations with rollback documentation; verify forward, failed, and rollback paths
- [x] 5.8 Implement encrypted backup and restore with integrity verification; verify wrong-key and corrupted-backup failures

## 6. Transactions, blocks, consensus, and ABCI

- [x] 6.1 Define the versioned transaction envelope and block structure with canonical encoding; verify encode/decode vectors and unknown-critical-field rejection
- [x] 6.2 Implement the ABCI 2.0 application (`CheckTx`, `PrepareProposal`, `ProcessProposal`, `FinalizeBlock`, `Commit`, `Query`); verify a single-node CometBFT devnet finalizes blocks
- [x] 6.3 Implement deterministic transaction ordering by fee and nonce; verify identical order for identical mempools
- [x] 6.4 Implement validator lifecycle (`pending`, `active`, `jailed`, `unbonding`, `unbonded`), staking, delegation, and epoch changes; verify transition vectors
- [x] 6.5 Implement slashing for double-sign and downtime with evidence windows and reporter rewards; verify valid, stale, and forged evidence
- [x] 6.6 Implement commit certificates, validator-set proofs, and light-client verification; verify insufficient-quorum rejection
- [x] 6.7 Implement public and consortium validator admission; verify public join, consortium approval, and unapproved rejection
- [x] 6.8 Implement node halt on state-root divergence and consensus-critical invariant violation; verify the halt path with an injected divergence
- [x] 6.9 Add a two-node and a Go+Python mixed devnet; verify both finalize the same chain

## 7. Governance and economics

- [x] 7.1 Implement proposal types, deposit, voting, quorum, threshold, timelock, execution, and cancellation; verify each lifecycle path
- [x] 7.2 Implement stake-weighted and consortium role-weighted voting with delegation and double-vote rejection; verify weights and rejection
- [x] 7.3 Implement constitutional invariants with supermajority and extended timelock; verify a simple majority cannot change the supply cap
- [x] 7.4 Implement timelocked code upgrades with binary-hash verification and rollback metadata; verify hash mismatch blocks activation
- [ ] 7.5 Implement treasury inflows, budgeted outflows, and reconciliation; verify overspend rejection and period reports
- [ ] 7.6 Implement the emergency committee pause/veto with multi-party threshold, auto-expiry, and on-chain records; verify expiry and audit
- [ ] 7.7 Implement ARCE supply, genesis allocation, vesting, tail emission, and constitutional bounds; verify cap and early-unlock rejection
- [ ] 7.8 Implement the base-fee market, burn/validator/treasury split, priority fee, and fee sponsorship; verify congestion, split, and budget vectors
- [ ] 7.9 Implement auto-conversion reserves and liability commitments; verify reserve proof and unbacked-route disablement
- [ ] 7.10 Implement staking reward distribution and slashing splits; verify epoch reconciliation and jailed-validator exclusion

## 8. Programmability

- [ ] 8.1 Integrate Wasmtime with deterministic configuration and fuel metering in Go and Python; verify identical gas for the same module and input
- [ ] 8.2 Implement deploy, instantiate, execute, query, migrate, and freeze with code hashes and versioned ABIs; verify lifecycle vectors
- [ ] 8.3 Implement contract state isolation, nested-call rollback, and call-depth/memory limits; verify reentrancy and exhaustion failures
- [ ] 8.4 Implement declarative templates for escrow, subscription, payroll, multisig, invoice, and conditional payment; verify template/reference-contract equivalence
- [ ] 8.5 Implement contract events, receipts, and indexed queries; verify event schema and query correctness
- [ ] 8.6 Implement reproducible-build and source-attestation verification; verify match and mismatch reports
- [ ] 8.7 Add contract fuzzing and differential execution tests; verify no panic or state divergence

## 9. Shielded pool

- [ ] 9.1 Implement note commitments, asset tags, range proofs, and the commitment tree; verify hidden amount/asset vectors
- [ ] 9.2 Implement nullifier derivation, recording, and double-spend rejection; verify first-spend, replay, and forged-nullifier vectors
- [ ] 9.3 Implement incoming/full/auditor viewing keys and note detection; verify scoped decryption and no-spend authority
- [ ] 9.4 Implement shield, transfer, and unshield flows with public reserve proofs; verify pool solvency and ineligible-asset rejection
- [ ] 9.5 Implement selective disclosure proofs for a payment, threshold, and period; verify context binding
- [ ] 9.6 Implement versioned circuits and verification-key commitments; verify historical and mismatch cases
- [ ] 9.7 Implement per-asset/per-jurisdiction confidential policy predicates; verify required-proof and policy-version behavior
- [ ] 9.8 Add confidential metadata-minimization and log-redaction tests; verify no plaintext or key leakage

## 10. Payments and messaging

- [x] 10.1 Implement payment intents with idempotency, expiry, receipt, and canonical links/QRs; verify duplicate and cross-network cases
- [x] 10.2 Implement batch and payroll runs with atomic and best-effort policies and per-item results; verify reconciliation
- [x] 10.3 Implement subscriptions/recurring authorizations with limits, expiry, and cancellation; verify over-limit and cancellation
- [x] 10.4 Implement refunds as linked new transfers with immutable history; verify reversal rejection
- [x] 10.5 Implement encrypted memos with size limits and public-memo warnings; verify decryption and oversize rejection
- [ ] 10.6 Integrate MLS (RFC 9420) group channels with add/remove/update/commit and epoch rotation; verify removed-member decryption failure
- [ ] 10.7 Implement channel messages, payment references, notices, file pointers, at-least-once delivery, deduplication, and offline catch-up; verify retry and retention
- [x] 10.8 Implement workspace roles, permissions, and audit logging; verify member/admin/auditor enforcement
- [x] 10.9 Implement signed webhooks with retries and replay protection; verify valid, replayed, and retryable deliveries

## 11. Compliance

- [x] 11.1 Implement the deterministic, versioned jurisdiction policy engine with allow/deny/hold decisions and rule IDs; verify policy-version history
- [x] 11.2 Implement Travel Rule/IVMS101 collection and authenticated exchange above threshold; verify qualifying, below-threshold, and unavailable-counterparty cases
- [x] 11.3 Implement provider-agnostic sanctions screening with list versions, potential-match holds, and fail-closed behavior; verify clear, match, and outage
- [x] 11.4 Implement W3C VC 2.0 KYC/accreditation verification with selective disclosure and no raw PII on-chain; verify jurisdiction, expiry, and revocation
- [x] 11.5 Implement scoped, time-bounded auditor access with automatic expiry and access logging; verify scope and expiry
- [x] 11.6 Implement data retention, legal hold, deletion/anonymization, and ledger-integrity preservation; verify retention and hold
- [x] 11.7 Implement FATF, MiCA, and ISO 20022 reference profiles; verify required-field validation and disclaimers
- [x] 11.8 Implement transaction-monitoring alerts and case dispositions without unauthorized auto-freeze; verify alert, disposition, and freeze policy
- [x] 11.9 Add tests proving no universal decryption path exists; verify ungranted access fails and exceptional access is logged

## 12. Interoperability connectors

- [x] 12.1 Implement the connector interface, capability handshake, asset mapping, health, and version negotiation; verify unsupported-asset and unhealthy cases
- [x] 12.2 Implement ISO 20022 `pacs.008`, `camt.053`, and `pain.001` mapping with field-level validation; verify outbound, inbound, statement, and malformed cases
- [x] 12.3 Implement ILPv4/Open Payments quotes, grants, STREAM, and HTLC settlement; verify quote expiry, fulfillment, and refund
- [x] 12.4 Implement the EVM lock/mint and burn/release bridge with finality, replay protection, rate limits, and emergency pause; verify replay and pause
- [x] 12.5 Implement external-to-canonical asset mapping with versioning and ambiguity rejection; verify EVM, fiat, and duplicate mappings
- [x] 12.6 Implement idempotent cross-rail reconciliation and exception reporting; verify duplicate confirmation, missing confirmation, and report totals
- [x] 12.7 Implement mTLS, credential rotation, allowlists, and secret redaction for connectors; verify rejection and rotation
- [x] 12.8 Implement exposure caps, rate limits, and governed limit changes; verify cap and rate-limit rejection

## 13. Peer network and sync

- [x] 13.1 Configure CometBFT authenticated/encrypted P2P with network identity and version negotiation; verify handshake and mismatch rejection
- [x] 13.2 Configure public discovery and consortium allowlists; verify public join and unapproved consortium rejection
- [x] 13.3 Tune block/tx gossip, deduplication, and block-priority propagation; verify duplicate suppression and priority
- [x] 13.4 Implement snapshot/state sync with state-root and commit verification; verify valid and tampered snapshots
- [x] 13.5 Implement peer scoring, rate limits, size limits, and bans; verify flood, invalid-message, and ban-expiry cases
- [x] 13.6 Configure NAT traversal, relay, advertised addresses, and anchor peers; verify NAT and mismatch behavior
- [x] 13.7 Implement eclipse/partition detection and no-finality-without-quorum behavior; verify partition and anchor recovery
- [x] 13.8 Verify Go and Python nodes interoperate on the same wire protocol; verify mixed-node finalization

## 14. Node API, CLI, and SDKs

- [x] 14.1 Implement REST `/v1` with OpenAPI, stable errors, cursor pagination, filters, and deprecation metadata; verify contract tests
- [x] 14.2 Implement JSON-RPC 2.0 with batch calls and stable method errors; verify valid, batch, and unknown-method cases
- [ ] 14.3 Implement WebSocket/SSE subscriptions with resumable cursors and `cursor_expired`; verify block, transaction, and message streams
- [x] 14.4 Implement API keys, OIDC/JWT, mTLS admin access, scopes, and rate limits; verify missing-scope, admin, and rate-limit cases
- [x] 14.5 Implement idempotency keys for every write with conflict detection and expiry; verify retry, conflict, and expiry
- [x] 14.6 Implement transaction submission, status polling/subscription, and dry-run simulation; verify valid, invalid, and simulated-failure cases
- [x] 14.7 Implement health, readiness, and version endpoints; verify healthy, syncing, and version discovery
- [ ] 14.8 Implement the Go SDK covering wallets, transactions, assets, payments, escrow, messaging, governance, and subscriptions; verify quickstart and typed errors (wallets, transactions, assets, payments, escrow, allowances, and subscriptions are in `sdk/go/arne`; messaging and governance wait on 10.7 and 7.1)
- [ ] 14.9 Implement the Python SDK with the same surface; verify quickstart and typed errors (matching `sdk/go/arne` in `arne.sdk`; messaging and governance wait on 10.7 and 7.1)
- [ ] 14.10 Implement the CLI for node, wallet, asset, pay, escrow, governance, connector, and devnet commands with JSON output; verify each command (every command exists in `arned` and `arne` except governance, which waits on 7.1)
- [x] 14.11 Add code generation and CI drift checks; verify stale generated code fails

## 15. Wallet and web console

- [x] 15.1 Implement Bech32m network-scoped addresses with checksums; verify valid, mutated, and wrong-network cases
- [x] 15.2 Implement HD accounts, multiple accounts, and mnemonic restore; verify cross-implementation derivation
- [ ] 15.3 Implement passkey/WebAuthn scoped authorization; verify enrollment, signing, and wrong-origin rejection
- [ ] 15.4 Implement social recovery with guardian threshold, timelock, and cancellation; verify threshold and cancellation
- [ ] 15.5 Implement hardware/external signer interface and human-readable confirmation; verify supported and unsupported signers
- [ ] 15.6 Implement scoped, expiring session keys and watch-only accounts; verify scope escalation and signing rejection
- [x] 15.7 Implement `arce:`/`arne:` payment URIs with strict validation and inert memo handling; verify valid, mismatch, and malicious memo
- [x] 15.8 Implement transaction previews and address-poisoning/unknown-asset warnings; verify preview and warning cases
- [ ] 15.9 Build the FastAPI console with explorer views for blocks, transactions, accounts, assets, validators, and governance; verify each view
- [ ] 15.10 Build the wallet UI with local signing, balances, send/receive, history, and staking; verify no secret leaves the client
- [ ] 15.11 Build the team workspace with channels, invoices, payroll, and roles; verify role enforcement and channel decryption
- [ ] 15.12 Add WebSocket/SSE live updates with reconnect and stale-data indicators; verify reconnect and stale states
- [ ] 15.13 Meet WCAG 2.2 AA, responsive layouts, locale formatting, and translation fallback; verify keyboard, screen-reader, mobile, and locale cases
- [ ] 15.14 Enforce CSP, CSRF, secure cookies, origin checks, and analytics-off default; verify CSRF and secret-leak tests
- [ ] 15.15 Meet console performance budgets; verify initial load, large-table, and bundle-budget checks

## 16. Observability, security, deployment, and operations

- [ ] 16.1 Implement metrics for consensus, mempool, peers, storage, API, connectors, and resources; verify scrape output and stable labels
- [ ] 16.2 Implement W3C Trace Context propagation and privacy-safe spans; verify end-to-end trace and secret redaction
- [ ] 16.3 Implement structured correlated logs with runtime level changes and redaction; verify correlation and redaction
- [ ] 16.4 Implement append-only hash-chained audit logs with export; verify tamper detection
- [ ] 16.5 Ship dashboards, alert rules, runbook links, and SLO reports; verify finality, liveness, and expiry alerts
- [ ] 16.6 Protect debug/profiling endpoints; verify production default and authorized access
- [ ] 16.7 Implement telemetry minimization, consortium opt-out, and aggregation thresholds; verify no memo or re-identification leakage
- [ ] 16.8 Maintain the threat model with mitigations and release reviews; verify a new component requires an update
- [ ] 16.9 Enforce secure defaults and reject default credentials or wildcard credentialed CORS; verify startup failures
- [ ] 16.10 Implement signed reproducible releases, SBOM, checksums, and verification docs; verify signature, rebuild, and SBOM completeness
- [ ] 16.11 Add continuous fuzzing, property tests, and differential fuzzing; verify no panic and no divergence
- [ ] 16.12 Complete independent audits, bug bounty, disclosure policy, and incident-response runbooks; verify critical findings block launch
- [x] 16.13 Implement the single-binary devnet with deterministic genesis and faucet; verify clean-machine start
- [ ] 16.14 Implement Docker Compose with node, CometBFT, indexer, console, and connectors; verify healthy startup, persistence, and health gating
- [ ] 16.15 Implement Helm with TLS, limits, network policies, PDBs, and separated workloads; verify production install and rolling restart
- [ ] 16.16 Implement Terraform modules with least privilege and no embedded secrets; verify plan, apply, and destroy
- [ ] 16.17 Implement network profiles and upgrade activation at a coordinated height; verify profile mismatch and insufficient adoption
- [ ] 16.18 Implement rehearsed rollback and state-safe restore; verify production-like rollback and irreversible-migration disclosure
- [ ] 16.19 Implement validator key separation, encrypted backup, rotation, and clock-skew monitoring; verify separation, restore, rotation, and drift alerts

## 17. Cross-language parity and release

- [ ] 17.1 Implement the full Python node on the `python` branch, including consensus wiring, ledger, API, CLI, and connectors; verify it joins a mixed testnet
- [x] 17.2 Run conformance vectors in both implementations on every change; verify a corrupted vector blocks both
- [ ] 17.3 Run differential fuzzing across encoding, transactions, state transitions, fees, governance, and shielded proofs; verify divergence is release-blocking
- [ ] 17.4 Automate branch synchronization of shared schemas and vectors; verify drift detection and same-tag release
- [ ] 17.5 Define and benchmark Python performance floors; verify block, API, and sync floors
- [ ] 17.6 Publish the compatibility matrix per release with smoke tests; verify every listed combination
- [ ] 17.7 Tag synchronized Go and Python releases with matching protocol versions; verify artifacts and SBOMs

## 18. Documentation

- [x] 18.1 Write `README.md` with the project overview, naming, quickstart, architecture diagram, and status; verify every command runs
- [ ] 18.2 Write user guides for wallets, payments, invoices, payroll, escrow, subscriptions, staking, governance, messaging, and recovery; verify examples against devnet
- [ ] 18.3 Write developer guides for local setup, architecture, transaction lifecycle, state tree, API, SDKs, contracts, templates, and testing; verify code samples compile
- [ ] 18.4 Write integration guides for ISO 20022, ILP/Open Payments, EVM bridging, webhooks, OIDC, and compliance providers; verify sample payloads
- [x] 18.5 Write embedding guides for running a node, embedding the ledger in another service, and using the SDKs as libraries; verify embed examples
- [x] 18.6 Write operator guides for deployment, validator setup, monitoring, backup/restore, upgrades, incident response, and security hardening; verify runbooks
- [x] 18.7 Write the feature reference and API reference, including every capability and error code; verify OpenAPI and JSON-RPC coverage
- [x] 18.8 Write security, privacy, compliance, and tokenomics documentation with disclaimers; verify links and versioning
- [ ] 18.9 Add documentation link checking, version selection, and CI tests; verify broken links and stale commands fail
- [x] 18.10 Publish the documentation site at `coin.anorebel.net` from the `AnoRebel/coin` repository on the maintainer's Dokploy instance (create the repository with `gh` if needed); verify HTTPS, automatic redeploy on the default branch, and no secrets in the build

## 19. Integration and launch readiness

- [ ] 19.1 Run the full test suite (unit, integration, conformance, differential, load, chaos) and publish results; verify all pass
- [ ] 19.2 Run a multi-node public testnet and a consortium reference network; verify finality, sync, upgrades, and recovery
- [ ] 19.3 Complete external security audits and remediate critical/high findings; verify reports and retests
- [ ] 19.4 Complete a disaster-recovery exercise and a key-compromise tabletop; verify RPO/RTO and runbook updates
- [ ] 19.5 Publish genesis, allocations, vesting, and network parameters for review; verify supply and conservation invariants
- [ ] 19.6 Publish the launch checklist, monitoring, support, and incident channels; verify ownership and escalation

## Workflow follow-up

- Archive the change only after the project's review and launch gates are satisfied.
- Verify the archived specs and the released compatibility matrix match the shipped behavior.
