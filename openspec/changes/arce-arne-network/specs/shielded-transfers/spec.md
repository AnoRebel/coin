# Spec Delta

## Purpose

Defines the opt-in confidential transfer pool: how amounts and asset types are hidden, how double-spends are prevented, how users and auditors can disclose selectively, and where confidentiality must yield to network policy and solvency.

## ADDED Requirements

### Requirement: Confidentiality is opt-in and asset-scoped

Transparent transfers SHALL remain the default. A user MAY shield a supported asset into the confidential pool and later unshield it. An asset's issuer or the network policy MUST be able to declare the asset ineligible for shielded transfers.

#### Scenario: Shield a supported asset
- **WHEN** a user shields a supported asset into the pool
- **THEN** the transparent balance decreases and a confidential note is created

#### Scenario: Ineligible asset rejected
- **WHEN** a user attempts to shield an asset marked ineligible by its issuer or network policy
- **THEN** the operation is rejected before any state change

#### Scenario: Unshield to transparent balance
- **WHEN** a user unshields a note to a transparent address
- **THEN** the note is spent, the pool liability decreases, and the transparent balance increases

### Requirement: Amounts and asset types are hidden in the pool

Confidential notes SHALL commit to amount and asset type using cryptographically binding commitments. Transfers within the pool MUST NOT reveal the amount or asset type in the transaction, mempool, block, or API responses. Range proofs MUST demonstrate that every output amount is non-negative and within the asset's valid range.

#### Scenario: Hidden amount in block
- **WHEN** a confidential transfer is included in a finalized block
- **THEN** an observer without viewing keys cannot determine the transferred amount

#### Scenario: Hidden asset type in block
- **WHEN** a confidential transfer moves a registered asset
- **THEN** an observer without viewing keys cannot determine which eligible asset was moved

#### Scenario: Negative amount proof fails
- **WHEN** a malicious user constructs a note with a negative or out-of-range amount
- **THEN** the range proof fails and the transaction is rejected

### Requirement: Nullifiers prevent double-spending

Every confidential note SHALL have a unique nullifier derived from its spending key and note data. The system MUST reject any transaction that reveals a previously spent nullifier or a nullifier not derived from a valid note.

#### Scenario: First spend accepted
- **WHEN** a valid note is spent for the first time
- **THEN** its nullifier is recorded and the spend is accepted

#### Scenario: Double-spend rejected
- **WHEN** a transaction reveals a nullifier already recorded on-chain
- **THEN** the transaction is rejected and no value moves

#### Scenario: Forged nullifier rejected
- **WHEN** a nullifier is not correctly derived from a note the spender is authorized to spend
- **THEN** the proof fails and the transaction is rejected

### Requirement: Viewing keys support user recovery and audit disclosure

The system SHALL support incoming viewing keys, full viewing keys, and scoped auditor view keys. A viewing key MUST allow decryption only of the notes and fields within its scope, and MUST NOT allow spending.

#### Scenario: Recipient detects incoming note
- **WHEN** a sender creates a note for a recipient's incoming viewing key
- **THEN** the recipient can detect and decrypt the note without any spending authority

#### Scenario: Auditor sees scoped history
- **WHEN** a user grants an auditor a time-bounded view key for a specific asset
- **THEN** the auditor can decrypt only the matching notes within the granted period and asset

#### Scenario: View key cannot spend
- **WHEN** an auditor attempts to spend a note using only a view key
- **THEN** the transaction is rejected

### Requirement: Selective disclosure proves specific facts

A user SHALL be able to produce a disclosure proof for a specific payment, balance threshold, or period without revealing unrelated notes. The proof MUST be verifiable against finalized state and MUST NOT grant ongoing visibility.

#### Scenario: Prove a single payment
- **WHEN** a user generates a disclosure proof for one transaction
- **THEN** a verifier can confirm the amount, asset, sender, and recipient for that transaction without learning other balances

#### Scenario: Prove a balance threshold
- **WHEN** a user proves that a balance exceeds a threshold
- **THEN** the verifier learns only that the threshold is met, not the exact balance

#### Scenario: Disclosure proof cannot be reused out of context
- **WHEN** a disclosure proof is presented for a different verifier challenge or network
- **THEN** verification fails

### Requirement: Pool solvency is publicly verifiable

The system SHALL publish a commitment to total shielded liabilities per asset and MUST provide a proof that the pool's transparent reserves cover those liabilities. The proof MUST be verifiable by any observer without viewing keys.

#### Scenario: Solvent pool proof verifies
- **WHEN** an observer verifies the pool's reserve proof at a finalized height
- **THEN** the proof confirms that reserves are at least equal to committed liabilities for each asset

#### Scenario: Insolvent state blocks unshielding
- **WHEN** a reserve proof fails or reserves are insufficient
- **THEN** unshielding is halted and the incident is reported

### Requirement: Confidential transfers respect network policy

Network policy SHALL be able to require, per asset or jurisdiction, a compliance predicate before a shielded transfer is accepted. The predicate MAY require a zero-knowledge proof of allowlist membership, a Travel Rule attestation, or a disclosure key. Policy MUST be publicly documented and versioned.

#### Scenario: Policy-compliant shielded transfer
- **WHEN** a shielded transfer includes the compliance proof required by the active policy
- **THEN** the transfer is accepted

#### Scenario: Missing compliance proof rejected
- **WHEN** a shielded transfer omits a required compliance proof
- **THEN** the transfer is rejected with the policy identifier and requirement

#### Scenario: Policy change is versioned
- **WHEN** a network updates its shielded-transfer policy
- **THEN** the new policy takes effect at a declared height and prior transfers remain valid under the policy in force at their height

### Requirement: Proof system is versioned and verifiable

Confidential transfers SHALL use a versioned proof system with transparent setup or a publicly auditable ceremony. Verification keys MUST be committed on-chain and MUST be retrievable by height. A proof produced for one circuit version MUST NOT verify against another.

#### Scenario: Current circuit verifies
- **WHEN** a transaction uses the active circuit version
- **THEN** the proof verifies against the committed verification key

#### Scenario: Deprecated circuit still validates history
- **WHEN** a historical transaction used a prior circuit version
- **THEN** chain validation continues to accept it

#### Scenario: Circuit version mismatch rejected
- **WHEN** a proof claims one circuit version but is verified against another
- **THEN** verification fails

### Requirement: Confidential metadata is minimized

Transactions, mempool entries, and API responses MUST NOT include amounts, asset identifiers, addresses, or timing correlations beyond what the protocol requires. Nodes MUST NOT log confidential note contents or viewing keys.

#### Scenario: Mempool metadata minimized
- **WHEN** a confidential transaction enters the mempool
- **THEN** the mempool entry exposes only the fields needed for admission and ordering, not the hidden amount or asset

#### Scenario: No secrets in logs
- **WHEN** a node processes a confidential transaction at debug level
- **THEN** no note plaintext, viewing key, or spending key appears in logs or traces
