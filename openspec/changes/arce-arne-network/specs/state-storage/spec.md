# Spec Delta

## Purpose

Defines how ARNE persists consensus state, proves it, snapshots it, prunes it, indexes it for queries, and migrates it safely across versions and implementations.

## ADDED Requirements

### Requirement: Consensus state writes are atomic and crash-consistent

The node SHALL persist each block's state transition as an atomic batch. After a crash, the node MUST recover to either the last committed height or a state from which it can safely replay, and MUST NOT expose a partially applied block.

#### Scenario: Crash during commit
- **WHEN** the node crashes while writing a block batch
- **THEN** on restart it recovers the last fully committed height and replays any incomplete block

#### Scenario: Atomic batch
- **WHEN** a block contains multiple state writes
- **THEN** all writes become visible together or none do

#### Scenario: Corruption detected
- **WHEN** a state record fails its checksum
- **THEN** the node reports corruption and refuses to serve proofs from the affected state

### Requirement: State is committed in a verifiable sparse Merkle tree

The node SHALL commit account, asset, escrow, contract, and governance state in a sparse Merkle tree with canonical key ordering. Every committed height MUST expose a state root and inclusion or exclusion proofs for any key.

#### Scenario: Inclusion proof
- **WHEN** a client requests a proof for an existing account balance
- **THEN** the proof verifies against the height's state root

#### Scenario: Exclusion proof
- **WHEN** a client requests a proof that an account does not exist
- **THEN** the exclusion proof verifies against the state root

#### Scenario: Deterministic root
- **WHEN** two conforming nodes apply the same block to the same state
- **THEN** they compute the same state root

### Requirement: Snapshots are verifiable and restorable

The node SHALL produce full and incremental snapshots tagged with height, state root, protocol version, and checksum. A restored node MUST verify the snapshot before serving traffic and MUST be able to continue syncing from it.

#### Scenario: Snapshot restore
- **WHEN** a node restores a verified snapshot
- **THEN** it starts at the snapshot height with the expected state root

#### Scenario: Tampered snapshot rejected
- **WHEN** a snapshot checksum or state root does not match
- **THEN** the restore is aborted and the existing state is untouched

#### Scenario: Incremental snapshot
- **WHEN** an incremental snapshot is applied to its declared base
- **THEN** the resulting state matches a full snapshot at the same height

### Requirement: Pruning preserves proofs and consensus safety

The node SHALL support configurable pruning of historical block and state data while retaining the latest finalized state, recent proofs, and all data required for consensus and slashing evidence. Archive mode MUST retain everything.

#### Scenario: Pruned node serves recent proofs
- **WHEN** a pruned node receives a proof request within its retention window
- **THEN** it returns a valid proof

#### Scenario: Pruned history unavailable
- **WHEN** a client requests a block older than the retention window
- **THEN** the node returns a clear `pruned` error with the available range

#### Scenario: Archive mode
- **WHEN** a node runs in archive mode
- **THEN** it retains all blocks and state and can serve any historical proof

### Requirement: Optional Postgres indexer is reorg-safe and idempotent

The node SHALL support an optional Postgres indexer for explorer, analytics, and API queries. Indexing MUST be idempotent, MUST handle finalized-chain reorgs that occur before finality, and MUST NOT become a consensus dependency.

#### Scenario: Indexer catches up
- **WHEN** the indexer starts behind the node
- **THEN** it catches up to the finalized height without duplicating records

#### Scenario: Pre-finality reorg
- **WHEN** a block is replaced before finality
- **THEN** the indexer removes the orphaned records and indexes the replacement

#### Scenario: Indexer unavailable
- **WHEN** the indexer is down
- **THEN** the node continues producing and finalizing blocks

### Requirement: Schema migrations are versioned and reversible

Every storage schema SHALL have a version and an ordered migration path. A migration MUST be tested on a production-sized dataset, MUST be idempotent, and MUST have a documented rollback or restore procedure.

#### Scenario: Forward migration
- **WHEN** a node starts with an older schema version
- **THEN** it applies the required migrations and records the new version

#### Scenario: Failed migration
- **WHEN** a migration fails partway
- **THEN** the node aborts, leaves the prior state intact where possible, and reports the failed step

#### Scenario: Rollback
- **WHEN** an operator rolls back to the prior binary
- **THEN** the documented restore procedure returns the node to a compatible state

### Requirement: Backup and restore are encrypted and verified

The system SHALL support encrypted backups of state, genesis, configuration, and keys with integrity checks. A restore MUST verify the backup before use and MUST report the restored height and state root.

#### Scenario: Encrypted backup
- **WHEN** an operator creates a backup
- **THEN** the backup is encrypted and includes a checksum and metadata

#### Scenario: Restore verification
- **WHEN** an operator restores a backup
- **THEN** the system verifies integrity and reports the restored height and state root

#### Scenario: Wrong key
- **WHEN** a restore is attempted with the wrong decryption key
- **THEN** it fails without modifying existing state

### Requirement: Genesis can be exported, imported, and audited

The system SHALL export a canonical genesis file containing network identity, initial accounts, asset registrations, validators, parameters, and allocation proofs. Import MUST validate every entry and MUST reject a genesis that violates supply or identity invariants.

#### Scenario: Genesis round-trip
- **WHEN** a genesis file is exported and re-imported on a fresh node
- **THEN** the resulting genesis hash matches the original

#### Scenario: Invalid allocation
- **WHEN** a genesis allocation exceeds the declared supply cap
- **THEN** import is rejected

#### Scenario: Genesis audit
- **WHEN** an auditor inspects the genesis file
- **THEN** every allocation, validator, and parameter is visible and verifiable
