# Spec Delta

## Purpose

Defines how ARNE connects to existing money and chain infrastructure: a connector framework, ISO 20022 bank messaging, ILPv4/Open Payments, EVM bridging, attestations, reconciliation, and safe failure semantics.

## ADDED Requirements

### Requirement: Connectors are pluggable and capability-negotiated

The system SHALL define a connector interface with declared capabilities, asset mappings, health status, and version. A connector MUST advertise the assets, directions, limits, and message types it supports before processing traffic.

#### Scenario: Capability handshake
- **WHEN** a connector registers with the node
- **THEN** the node records its supported assets, directions, limits, and version

#### Scenario: Unsupported asset rejected
- **WHEN** a transfer requests an asset the connector does not support
- **THEN** the request is rejected before reaching the external rail

#### Scenario: Connector unhealthy
- **WHEN** a connector fails its health check
- **THEN** new transfers through it are paused while in-flight operations are reconciled

### Requirement: ISO 20022 messages map to and from ARNE transfers

The system SHALL support ISO 20022 `pacs.008` customer credit transfers and `camt.053` bank statements. Mapping MUST preserve amount, currency, debtor, creditor, remittance information, end-to-end identifier, and status. Invalid messages MUST be rejected with field-level errors.

#### Scenario: Outbound credit transfer
- **WHEN** an ARNE transfer is sent to a bank-connected asset
- **THEN** a valid `pacs.008` message is produced with the mapped parties and remittance data

#### Scenario: Inbound credit transfer
- **WHEN** a valid `pacs.008` message is received
- **THEN** the corresponding ARNE transfer is created or matched idempotently

#### Scenario: Statement reconciliation
- **WHEN** a `camt.053` statement is imported
- **THEN** entries are matched to ARNE transfers and unmatched entries are reported as exceptions

#### Scenario: Malformed message rejected
- **WHEN** an ISO 20022 message is missing a mandatory field or has an invalid currency
- **THEN** it is rejected with the field path and reason

### Requirement: ILPv4 and Open Payments connectors route cross-network value

The system SHALL support ILPv4 packet routing and Open Payments quote, grant, and payment flows. A connector MUST enforce quote expiry, maximum packet amount, and Hashed Timelock Agreement settlement, and MUST NOT release value before the HTLC condition is satisfied.

#### Scenario: Open Payments quote
- **WHEN** a payer requests a quote for a cross-currency payment
- **THEN** the connector returns a signed quote with rate, fees, and expiry

#### Scenario: HTLC settlement
- **WHEN** the receiver fulfills the HTLC condition before expiry
- **THEN** the sender's value is settled and the receiver is credited

#### Scenario: Expired HTLC
- **WHEN** the HTLC expires without fulfillment
- **THEN** the sender's value is refunded and the receiver is not credited

#### Scenario: Quote replayed
- **WHEN** an expired or already-used quote is submitted
- **THEN** the payment is rejected

### Requirement: EVM bridge locks, mints, burns, and releases exactly once

The EVM connector SHALL support lock-and-mint and burn-and-release flows with replay protection, finality checks, rate limits, and an emergency pause. A bridged asset MUST NOT be minted without a corresponding locked collateral, and MUST NOT be released twice.

#### Scenario: Lock and mint
- **WHEN** an asset is locked on the EVM chain and the required finality is reached
- **THEN** the corresponding wrapped asset is minted on ARNE exactly once

#### Scenario: Burn and release
- **WHEN** a wrapped asset is burned on ARNE
- **THEN** the corresponding collateral is released on the EVM chain exactly once

#### Scenario: Replay rejected
- **WHEN** the same source transaction is submitted twice
- **THEN** the second submission is rejected and no additional value is minted

#### Scenario: Emergency pause
- **WHEN** the bridge is paused
- **THEN** new mints and releases stop while redemption of already-issued wrapped assets remains possible through the approved path

### Requirement: External asset identifiers map to canonical ARNE assets

Every bridged or bank-connected asset SHALL have a canonical ARNE asset identifier and an explicit mapping to its external identifier, including chain ID, contract address, ISO 4217 code, ISO 24165 DTI, or bank account scheme. The mapping MUST be versioned and reversible for audit.

#### Scenario: Map an EVM token
- **WHEN** a connector registers an EVM token
- **THEN** the mapping records chain ID, contract address, decimals, and the canonical ARNE asset identifier

#### Scenario: Map a fiat currency
- **WHEN** a connector registers a bank currency
- **THEN** the mapping records the ISO 4217 code and the settlement account reference

#### Scenario: Ambiguous mapping rejected
- **WHEN** two external identifiers map to one canonical asset without an explicit merge policy
- **THEN** the registration is rejected

### Requirement: Cross-rail operations are idempotent and reconciled

Every connector operation SHALL carry an idempotency key and a correlation identifier. The system MUST reconcile external confirmations with on-chain state and MUST report unmatched or conflicting operations as exceptions.

#### Scenario: Duplicate external confirmation
- **WHEN** the external rail sends the same confirmation twice
- **THEN** the second confirmation is ignored and the operation remains settled once

#### Scenario: Missing confirmation
- **WHEN** an external operation is not confirmed within its timeout
- **THEN** it is marked pending or failed according to policy and reported as an exception

#### Scenario: Reconciliation report
- **WHEN** a reconciliation run completes
- **THEN** it reports matched, unmatched, and conflicting operations with totals

### Requirement: Connector credentials and endpoints are protected

Connectors SHALL authenticate with mutually authenticated TLS or signed requests, use rotatable credentials, restrict destinations by allowlist, and MUST NOT expose secrets in logs or API responses.

#### Scenario: mTLS required
- **WHEN** a connector connects without a valid client certificate
- **THEN** the connection is rejected

#### Scenario: Credential rotation
- **WHEN** a connector credential is rotated
- **THEN** the old credential stops working after the declared overlap period

#### Scenario: Secret redaction
- **WHEN** a connector error is logged
- **THEN** credentials, tokens, and account numbers are redacted

### Requirement: Exposure and rate limits are enforced per connector

Each connector SHALL enforce per-asset and per-counterparty exposure caps, transaction rate limits, and maximum transfer sizes. A limit breach MUST reject the operation before external value moves.

#### Scenario: Exposure cap reached
- **WHEN** a new transfer would exceed the connector's exposure cap
- **THEN** the transfer is rejected

#### Scenario: Rate limit exceeded
- **WHEN** a counterparty exceeds its transaction rate limit
- **THEN** further requests are throttled or rejected

#### Scenario: Limit change is governed
- **WHEN** an operator changes a connector limit
- **THEN** the change is recorded with actor, reason, and prior value
