# Spec Delta

## Purpose

Defines how ARNE executes programmable value: a deterministic sandboxed WASM environment for general contracts and a set of audited declarative templates for common team and payment workflows, with metered gas, versioned ABIs, and safe upgrade rules.

## ADDED Requirements

### Requirement: Contracts execute deterministically in a sandbox

The system SHALL execute contracts in an isolated WASM runtime with no access to wall-clock time, network, filesystem, environment variables, or non-deterministic randomness. The same contract code, input, and state MUST produce the same result and state root on every conforming node.

#### Scenario: Cross-node determinism
- **WHEN** the same contract call is executed by the Go and Python nodes at the same state
- **THEN** both produce identical output, events, gas usage, and state root

#### Scenario: Forbidden host access
- **WHEN** a contract attempts to read the clock or open a network connection
- **THEN** the call fails with a deterministic error and does not affect state

### Requirement: Gas is metered and bounded

Every contract deployment and call SHALL consume gas for instructions, memory, storage, and host calls. Execution MUST stop when the gas limit is exhausted, and all state changes from that call MUST be reverted.

#### Scenario: Out of gas reverts
- **WHEN** a contract call exhausts its gas limit
- **THEN** the call reverts, the fee is charged, and no contract state changes persist

#### Scenario: Gas limit respected
- **WHEN** a caller sets a gas limit below the cost of the call
- **THEN** execution does not exceed the limit

#### Scenario: Deterministic gas accounting
- **WHEN** the same call is executed on two conforming nodes
- **THEN** both report the same gas consumed

### Requirement: Declarative templates cover common workflows without code

The system SHALL provide audited declarative templates for escrow, subscription, payroll, multisig, invoice, and conditional payment. A template MUST be configured with typed parameters and MUST produce the same ledger effects as an equivalent audited contract.

#### Scenario: Create an escrow template
- **WHEN** a user configures an escrow template with payer, payee, arbiter, amount, asset, and timeout
- **THEN** the escrow is created and the configured funds are locked

#### Scenario: Template update is bounded
- **WHEN** a user attempts to change a template parameter that is immutable after creation, such as the asset or principal
- **THEN** the change is rejected

#### Scenario: Template and contract equivalence
- **WHEN** a template and its reference contract are executed with the same parameters
- **THEN** they produce the same transfers, events, and final balances

### Requirement: Contract lifecycle is explicit

Contracts SHALL support deploy, instantiate, execute, query, migrate, and freeze operations. A contract MUST have an immutable code hash, a versioned ABI, and an owner or governance authority for migration. A frozen contract MUST reject execution but remain queryable.

#### Scenario: Deploy and instantiate
- **WHEN** a user deploys valid contract code and instantiates it with initial state
- **THEN** the contract receives a unique address and its code hash is recorded

#### Scenario: Frozen contract rejects execution
- **WHEN** a caller invokes a frozen contract
- **THEN** the call is rejected and no state changes

#### Scenario: Unauthorized migration rejected
- **WHEN** an account without migration authority attempts to migrate a contract
- **THEN** the migration is rejected

### Requirement: Contract state is isolated and addressable

Each contract SHALL own a namespaced state area. A contract MUST NOT read or write another contract's state except through an explicit message call, and a failed call MUST NOT leave partial writes.

#### Scenario: Direct state access denied
- **WHEN** a contract attempts to write another contract's state key directly
- **THEN** the write is rejected

#### Scenario: Nested call failure rolls back
- **WHEN** a contract calls another contract and the inner call fails
- **THEN** the inner state changes are reverted while the outer call may handle the error

### Requirement: ABI and error codes are versioned

Every contract SHALL expose a versioned ABI describing entrypoints, argument types, return types, events, and error codes. A caller MUST be able to detect ABI incompatibility before execution.

#### Scenario: ABI compatibility check
- **WHEN** a client calls a contract using an ABI version the contract does not support
- **THEN** the call is rejected with an `abi_incompatible` error before execution

#### Scenario: Stable error codes
- **WHEN** a contract returns a business error
- **THEN** the error includes a stable machine-readable code and a human-readable message

### Requirement: Contracts are immutable by default and upgradeable only by authority

A deployed contract's code MUST be immutable unless it explicitly declares an upgrade authority at deployment. Upgrades MUST be authorized by that authority and MUST preserve storage compatibility or declare a migration.

#### Scenario: Immutable contract cannot change code
- **WHEN** anyone attempts to replace an immutable contract's code
- **THEN** the operation is rejected

#### Scenario: Authorized upgrade
- **WHEN** the declared upgrade authority performs a migration with a compatible storage layout
- **THEN** the contract code is replaced and existing state remains readable

#### Scenario: Incompatible migration rejected
- **WHEN** an upgrade would make existing state unreadable
- **THEN** the upgrade is rejected before code replacement

### Requirement: Reentrancy and resource exhaustion are bounded

The runtime SHALL enforce call-depth, memory, stack, and execution-step limits. A contract MUST NOT be able to reenter a state-changing entrypoint in a way that bypasses its own guards, and MUST NOT exhaust node memory.

#### Scenario: Call depth exceeded
- **WHEN** nested calls exceed the maximum call depth
- **THEN** the call fails deterministically and state is reverted

#### Scenario: Memory limit exceeded
- **WHEN** a contract allocates beyond the memory limit
- **THEN** execution halts and the fee is charged

### Requirement: Contract events are indexed and queryable

Contract events SHALL be emitted with a stable schema, included in the transaction receipt, and indexed for querying by contract, event type, and height.

#### Scenario: Event query
- **WHEN** a client queries events for a contract and event type
- **THEN** the indexed results match the events in finalized receipts

#### Scenario: Event schema mismatch
- **WHEN** a caller subscribes with a schema version the contract does not emit
- **THEN** the subscription is rejected or returns no events with a clear schema warning

### Requirement: Contract code is verifiable

The system SHALL support reproducible builds and source attestation for deployed contracts. A verifier MUST be able to compare a deployed code hash with a rebuilt artifact from published source and toolchain metadata.

#### Scenario: Reproducible build matches
- **WHEN** a verifier rebuilds published source with the declared toolchain
- **THEN** the resulting code hash matches the deployed code hash

#### Scenario: Mismatch reported
- **WHEN** the rebuilt hash differs from the deployed hash
- **THEN** the verifier reports a mismatch and does not mark the contract as verified
