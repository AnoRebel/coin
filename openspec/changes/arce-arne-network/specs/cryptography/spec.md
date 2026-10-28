# Spec Delta

## Purpose

Defines the cryptographic primitives, signature suites, key derivation, domain separation, and post-quantum migration rules that all ARNE nodes, wallets, and SDKs must implement identically.

## ADDED Requirements

### Requirement: Canonical user signature suite

User transactions and authorization messages SHALL be signed with Ed25519 by default. A signature MUST verify only against the exact canonical serialized message and the claimed public key, and verification MUST be deterministic across Go and Python implementations.

#### Scenario: Valid Ed25519 signature accepted
- **WHEN** a transaction is signed with a registered Ed25519 key and the canonical message is unchanged
- **THEN** every conforming node verifies the signature successfully

#### Scenario: Mutated message rejected
- **WHEN** any signed field, including amount, asset, recipient, fee, nonce, or chain ID, is changed after signing
- **THEN** verification fails on every conforming node

#### Scenario: Wrong key rejected
- **WHEN** a signature is presented with a public key that did not produce it
- **THEN** verification fails

### Requirement: Validator aggregate signatures are rogue-key resistant

Validator votes SHALL use BLS12-381 aggregate signatures with proof-of-possession. A node MUST reject an aggregate that includes a validator without a valid proof-of-possession or that fails aggregate verification.

#### Scenario: Valid aggregate accepted
- **WHEN** a quorum of validators with registered proofs-of-possession sign the same block commitment
- **THEN** the aggregate verifies and the block is considered committed

#### Scenario: Missing proof-of-possession rejected
- **WHEN** a validator contributes to an aggregate without a registered valid proof-of-possession
- **THEN** the aggregate is rejected and the validator is reported as misbehaving

#### Scenario: Rogue-key attempt rejected
- **WHEN** an attacker registers a public key derived from another validator's key to forge an aggregate
- **THEN** proof-of-possession verification prevents the key from being registered

### Requirement: Hybrid post-quantum signatures are available and explicit

The system SHALL support an optional hybrid signature suite combining Ed25519 with ML-DSA (FIPS 204). A hybrid signature MUST verify only when both component signatures verify. The suite identifier MUST be present in the signed envelope and in on-chain account metadata.

#### Scenario: Hybrid signature verifies
- **WHEN** an account is registered for the hybrid suite and both Ed25519 and ML-DSA signatures verify
- **THEN** the authorization is accepted

#### Scenario: One component fails
- **WHEN** either the Ed25519 or ML-DSA component fails verification
- **THEN** the authorization is rejected

#### Scenario: Suite downgrade rejected
- **WHEN** an account registered for the hybrid suite submits a classical-only signature
- **THEN** the authorization is rejected and the account's suite requirement is unchanged

### Requirement: Hashing, encoding, and signing canonicalization

The system SHALL use SHA-256 for consensus commitments and BLAKE3 for content-addressed payloads. All signed structures MUST use a canonical deterministic encoding with explicit field order and length delimiting. JSON MUST NOT be used as the signed representation.

#### Scenario: Cross-implementation canonical bytes match
- **WHEN** the same logical message is encoded by the Go and Python implementations
- **THEN** both produce byte-identical canonical encodings and identical SHA-256 commitments

#### Scenario: Non-canonical encoding rejected
- **WHEN** a message contains duplicate fields, unknown critical fields, or non-minimal integer encodings
- **THEN** the receiver rejects it before signature verification

### Requirement: Hierarchical deterministic key derivation

Wallets SHALL derive keys deterministically from a BIP-39 mnemonic using SLIP-0010 Ed25519 derivation with the ARNE purpose path `m/44'/9999'/<account>'/<change>'/<index>'`. The same mnemonic and path MUST produce the same public key in every implementation.

#### Scenario: Cross-implementation derivation matches
- **WHEN** the same mnemonic and derivation path are used in the Go and Python wallets
- **THEN** both produce the same private key, public key, and address

#### Scenario: Account isolation
- **WHEN** a user creates a second account index
- **THEN** keys, addresses, and balances are distinct from the first account

### Requirement: Domain separation prevents cross-context and cross-network replay

Every signature SHALL include a domain tag, the network ID, the chain ID, the signature suite, and the message purpose. A signature produced for one network, purpose, or suite MUST NOT verify in another.

#### Scenario: Cross-network replay rejected
- **WHEN** a transaction signed for `arne:testnet` is submitted to `arne:mainnet`
- **THEN** verification fails and the transaction is not executed

#### Scenario: Cross-purpose replay rejected
- **WHEN** a message signed for governance voting is submitted as a payment authorization
- **THEN** verification fails

### Requirement: Cryptographic agility and deprecation

The system SHALL maintain a versioned registry of signature suites and hash functions with status `active`, `deprecated`, or `disabled`. Deprecated suites MUST continue to verify existing signatures but MUST NOT be accepted for new accounts after their deprecation height.

#### Scenario: Deprecated suite still verifies history
- **WHEN** a historical transaction uses a suite marked deprecated
- **THEN** chain validation continues to accept it

#### Scenario: Deprecated suite rejected for new account
- **WHEN** a new account attempts to register a suite marked deprecated
- **THEN** registration is rejected with the active suite list

#### Scenario: Disabled suite rejected everywhere
- **WHEN** any transaction uses a suite marked disabled
- **THEN** the transaction is rejected regardless of signature validity

### Requirement: Key material is protected in memory and logs

Implementations MUST use a cryptographically secure random source, MUST NOT log or serialize private keys or seed phrases, and MUST zero sensitive buffers after use where the runtime permits.

#### Scenario: Private key absent from logs
- **WHEN** a wallet signs a transaction at debug log level
- **THEN** no private key, seed phrase, or derived secret appears in logs, traces, or error messages

#### Scenario: Weak randomness rejected
- **WHEN** an implementation cannot obtain a secure random source
- **THEN** key generation and signing fail closed rather than using a fallback source

### Requirement: Multi-signature and threshold authorization

The system SHALL support M-of-N authorization for accounts and transactions using either Ed25519 or BLS12-381. A transaction MUST NOT execute until the required threshold of distinct authorized keys has signed.

#### Scenario: Threshold met
- **WHEN** an M-of-N account submits M valid distinct signatures
- **THEN** the transaction executes

#### Scenario: Threshold not met
- **WHEN** fewer than M valid distinct signatures are present
- **THEN** the transaction is rejected and no state changes

#### Scenario: Duplicate signer
- **WHEN** the same key signs twice in an M-of-N authorization
- **THEN** it counts once and the threshold is not satisfied by the duplicate
