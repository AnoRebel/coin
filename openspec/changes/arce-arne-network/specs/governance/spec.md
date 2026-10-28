# Spec Delta

## Purpose

Defines how ARNE changes safely: on-chain parameter governance, timelocked code upgrades, treasury spending, emergency intervention, and the immutable invariants that no ordinary vote may break.

## ADDED Requirements

### Requirement: Governance proposals have explicit types and lifecycle

The system SHALL support proposals for parameter changes, code upgrades, treasury spending, validator admission in consortium networks, asset policy, compliance policy, and cryptographic suite changes. Every proposal MUST move through `deposit`, `voting`, `passed` or `rejected`, `timelock`, and `executed` or `cancelled`.

#### Scenario: Parameter proposal executes
- **WHEN** a parameter proposal reaches quorum and the required threshold and the timelock expires
- **THEN** the parameter change is applied at the declared activation height

#### Scenario: Rejected proposal cannot execute
- **WHEN** a proposal fails quorum or threshold
- **THEN** it is marked rejected and cannot be executed

#### Scenario: Cancelled proposal
- **WHEN** the proposer cancels before execution
- **THEN** the proposal is cancelled and no change is applied

### Requirement: Voting power is stake- and role-aware

Voting power SHALL be derived from bonded stake and, in consortium networks, from member roles defined by the consortium charter. A voter MUST NOT vote twice, and delegated voting power MUST be attributable to the delegator.

#### Scenario: Stake-weighted vote
- **WHEN** a token holder votes with bonded ARCE
- **THEN** the vote weight equals the bonded amount subject to the governance cap

#### Scenario: Double vote rejected
- **WHEN** the same voting power is used in two conflicting votes on one proposal
- **THEN** the later vote is rejected

#### Scenario: Consortium role vote
- **WHEN** a consortium member votes under its charter role
- **THEN** the vote weight follows the charter and is recorded with the role

### Requirement: Code upgrades are timelocked and verifiable

A code upgrade proposal MUST include the target binary or WASM hash, the migration plan, the activation height, and a rollback plan. The upgrade MUST be executable only after the governance timelock and MUST verify that the running binary matches the approved hash.

#### Scenario: Approved upgrade activates
- **WHEN** the timelock expires and the node binary hash matches the approved hash
- **THEN** the upgrade activates at the declared height

#### Scenario: Hash mismatch blocks upgrade
- **WHEN** a node's binary hash does not match the approved hash
- **THEN** the node refuses to apply the upgrade and halts with a clear error

#### Scenario: Rollback path documented
- **WHEN** an upgrade fails its post-upgrade health checks
- **THEN** operators can follow the approved rollback plan without violating state invariants

### Requirement: Immutable invariants require a supermajority and a longer timelock

The system SHALL classify supply cap, value-conservation rules, cryptographic suite floor, and shielded-pool solvency rules as constitutional invariants. Changing an invariant MUST require a supermajority and a longer timelock than an ordinary parameter change.

#### Scenario: Ordinary vote cannot change supply cap
- **WHEN** a simple-majority proposal attempts to raise the ARCE supply cap
- **THEN** the proposal is rejected as constitutional

#### Scenario: Supermajority changes an invariant
- **WHEN** a constitutional proposal meets the supermajority threshold and the extended timelock
- **THEN** the invariant change is applied at the declared height

### Requirement: Treasury spending is transparent and budgeted

Treasury inflows and outflows SHALL be recorded on-chain with a purpose, recipient, amount, asset, and proposal reference. A treasury proposal MUST NOT spend more than the available balance and MUST declare a budget category.

#### Scenario: Approved treasury spend
- **WHEN** a treasury proposal passes and the treasury has sufficient balance
- **THEN** the amount is transferred to the declared recipient and recorded with the proposal reference

#### Scenario: Overspend rejected
- **WHEN** a treasury proposal exceeds the available balance
- **THEN** it is rejected before voting or execution

#### Scenario: Treasury report
- **WHEN** a client queries treasury flows for a period
- **THEN** the response reconciles inflows, outflows, and closing balance

### Requirement: Emergency intervention is bounded and accountable

The system SHALL allow a multi-party emergency committee to pause specified operations or veto a malicious proposal within a short window. An emergency action MUST expire automatically, MUST be publicly justified, and MUST be ratifiable or reversible by governance.

#### Scenario: Emergency pause
- **WHEN** the emergency committee reaches its multi-party threshold
- **THEN** the specified operations pause and an incident record is published

#### Scenario: Pause expires
- **WHEN** the emergency pause period ends without ratification
- **THEN** normal operations resume automatically

#### Scenario: Emergency action audited
- **WHEN** any emergency action is taken
- **THEN** the action, signers, reason, and duration are recorded on-chain and in the audit log

### Requirement: Proposal execution is atomic

If any step of an approved proposal fails during execution, none of the proposal's state changes MUST be applied. The proposal MUST be marked failed with a machine-readable reason.

#### Scenario: Multi-action proposal fails
- **WHEN** one action in a multi-action proposal fails
- **THEN** all actions are reverted and the proposal is marked failed

#### Scenario: Execution retry
- **WHEN** a failed proposal is corrected and resubmitted
- **THEN** it follows the full governance lifecycle again
