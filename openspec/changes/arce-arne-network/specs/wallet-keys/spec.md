# Spec Delta

## Purpose

Defines how users and teams hold keys, sign safely, recover access, and interact with addresses and payment requests without exposing secrets or falling for common wallet attacks.

## ADDED Requirements

### Requirement: Addresses are canonical, network-scoped, and checksummed

Every account SHALL have a canonical address derived from its public key with a network-specific human-readable prefix and a checksum. An address MUST be rejected if its checksum, prefix, or network does not match the active network.

#### Scenario: Valid address accepted
- **WHEN** a wallet parses an address with the correct prefix and checksum for the active network
- **THEN** it displays the address and allows a transfer

#### Scenario: Checksum failure rejected
- **WHEN** a single character in an address is changed
- **THEN** the checksum fails and the wallet refuses to use the address

#### Scenario: Wrong network rejected
- **WHEN** a mainnet address is pasted into a testnet wallet
- **THEN** the wallet refuses to send and explains the network mismatch

### Requirement: HD wallets derive accounts deterministically

A wallet SHALL derive accounts from a BIP-39 mnemonic using the ARNE derivation path and MUST support multiple accounts and change addresses. The same mnemonic MUST restore the same accounts on any conforming wallet.

#### Scenario: Restore from mnemonic
- **WHEN** a user restores a wallet from a valid mnemonic
- **THEN** the wallet recreates the same accounts and addresses

#### Scenario: Multiple accounts
- **WHEN** a user creates a new account index
- **THEN** the new account has distinct keys and balances

#### Scenario: Invalid mnemonic rejected
- **WHEN** a user enters an invalid checksum or word
- **THEN** the wallet rejects the mnemonic and does not derive keys

### Requirement: Passkeys provide phishing-resistant signing

The wallet SHALL support passkey/WebAuthn authentication bound to the wallet's origin. A passkey MUST authorize a scoped session or a specific transaction and MUST NOT expose the account's root private key to the relying party.

#### Scenario: Passkey enrollment
- **WHEN** a user enrolls a passkey
- **THEN** the wallet records the credential public key and associates it with the account

#### Scenario: Passkey signing
- **WHEN** a user approves a transaction with a passkey
- **THEN** the wallet produces a valid scoped authorization without revealing the root key

#### Scenario: Wrong origin rejected
- **WHEN** a passkey assertion is presented from a different origin
- **THEN** the wallet rejects it

### Requirement: Social recovery requires a threshold and a timelock

An account SHALL be able to designate guardians for recovery. Recovery MUST require a threshold of guardians, a delay period, and a cancellation window for the legitimate owner. A single guardian MUST NOT be able to recover an account alone.

#### Scenario: Threshold recovery
- **WHEN** the required guardian threshold approves a recovery
- **THEN** the recovery enters a timelock and completes if not cancelled

#### Scenario: Owner cancels
- **WHEN** the legitimate owner cancels during the timelock
- **THEN** the recovery is aborted and the guardianship remains unchanged

#### Scenario: Single guardian rejected
- **WHEN** one guardian attempts recovery alone
- **THEN** the request is rejected

### Requirement: Hardware and external signers use a common interface

The wallet SHALL support hardware and external signers through a common signing interface that exposes public keys, signs canonical payloads, and confirms human-readable transaction details. A signer MUST NOT receive secrets it does not need.

#### Scenario: Hardware signing
- **WHEN** a user signs with a supported hardware wallet
- **THEN** the device displays the canonical transaction details and returns a valid signature

#### Scenario: Unsupported signer
- **WHEN** a signer does not support the required suite or payload
- **THEN** the wallet reports the incompatibility before requesting a signature

### Requirement: Session keys are scoped and expiring

Applications SHALL be able to request session keys with explicit permissions, spending limits, asset scope, and expiry. A session key MUST NOT exceed its permissions and MUST stop working at expiry.

#### Scenario: Scoped session
- **WHEN** a user grants a session key for one asset and a spending limit
- **THEN** the application can transfer only within that scope

#### Scenario: Session expiry
- **WHEN** the session key expires
- **THEN** further requests are rejected

#### Scenario: Scope escalation rejected
- **WHEN** an application attempts an operation outside its session scope
- **THEN** the operation is rejected

### Requirement: Payment URIs are unambiguous and safe to parse

The wallet SHALL support `arce:` and `arne:` payment URIs carrying a network, address or intent, asset, amount, memo, and optional expiry. The wallet MUST validate every field, display a human-readable summary, and require explicit confirmation.

#### Scenario: Parse a payment URI
- **WHEN** a user scans a valid payment URI
- **THEN** the wallet displays the network, recipient, asset, amount, and memo before confirmation

#### Scenario: Amount mismatch
- **WHEN** the URI amount conflicts with the payment intent amount
- **THEN** the wallet refuses to pay and explains the conflict

#### Scenario: Malicious memo
- **WHEN** a URI contains a memo with executable content
- **THEN** the wallet renders it as inert text and warns the user

### Requirement: Transaction previews are human-readable and warning-aware

Before signing, the wallet SHALL display the actual asset, amount, fee, recipient, network, and any risk warnings in human-readable form. The preview MUST NOT rely on untrusted metadata supplied only by the recipient.

#### Scenario: Clear preview
- **WHEN** a user initiates a transfer
- **THEN** the wallet shows the asset, amount, fee, recipient, and network before signing

#### Scenario: Address poisoning warning
- **WHEN** a recipient address closely resembles a saved contact but is not an exact match
- **THEN** the wallet warns the user and does not auto-fill the contact

#### Scenario: Unknown asset warning
- **WHEN** a transaction involves an asset not in the wallet's trusted list
- **THEN** the wallet warns the user before signing

### Requirement: Watch-only and export are supported safely

The wallet SHALL support watch-only accounts using public keys or viewing keys and MUST allow encrypted export of wallet metadata. A watch-only account MUST NOT be able to sign, and an export MUST NOT contain plaintext secrets unless the user explicitly requests a plaintext backup after a warning.

#### Scenario: Watch-only balance
- **WHEN** a user adds a watch-only account
- **THEN** the wallet shows balances and history but cannot sign

#### Scenario: Encrypted export
- **WHEN** a user exports wallet metadata
- **THEN** the export is encrypted and requires a passphrase to restore

#### Scenario: Plaintext export warning
- **WHEN** a user requests a plaintext secret export
- **THEN** the wallet warns about the risk and requires explicit confirmation
