# Spec Delta

## Purpose

Defines how ARNE reaches agreement: a CometBFT-based BFT proof-of-stake protocol with deterministic finality, validator lifecycle, staking, slashing, light-client verification, and support for both public and consortium validator sets.

## ADDED Requirements

### Requirement: Consensus provides deterministic finality

A block SHALL be final once a valid commit certificate from more than two-thirds of the voting power is included. A finalized block MUST NOT be reverted by any conforming node under fewer than one-third Byzantine voting power.

#### Scenario: Finalized block is irreversible
- **WHEN** a block receives a valid commit certificate
- **THEN** all conforming nodes treat it as final and refuse any conflicting block at that height

#### Scenario: Conflicting block rejected
- **WHEN** a node receives a block at a finalized height that conflicts with the committed block
- **THEN** the node rejects it and reports the sender as misbehaving

#### Scenario: Quorum unavailable
- **WHEN** less than two-thirds of voting power is available
- **THEN** the network does not finalize new blocks and reports a liveness degradation

### Requirement: Validators have an explicit lifecycle

Validators SHALL move through `pending`, `active`, `jailed`, `unbonding`, and `unbonded` states. A validator MUST have a registered consensus key, a proof-of-possession, a staking account, and a declared commission before activation.

#### Scenario: Validator activation
- **WHEN** a pending validator meets the minimum stake and registers a valid consensus key
- **THEN** it becomes eligible for the active set at the next epoch

#### Scenario: Jailed validator cannot propose
- **WHEN** a validator is jailed
- **THEN** it is removed from the active set and cannot propose or vote until unjailed

#### Scenario: Unbonding delay
- **WHEN** a validator begins unbonding
- **THEN** its stake remains slashable until the unbonding period elapses

### Requirement: Voting power is stake-weighted and capped

Validator voting power SHALL be proportional to bonded stake, subject to a governance-set per-validator cap and a minimum self-bond. The sum of all voting power MUST be normalized to the active set's total stake.

#### Scenario: Stake increases voting power
- **WHEN** a validator's bonded stake increases within the cap
- **THEN** its voting power increases proportionally at the next epoch

#### Scenario: Cap enforced
- **WHEN** a validator's stake would exceed the per-validator voting power cap
- **THEN** the excess does not increase its voting power

#### Scenario: Minimum self-bond enforced
- **WHEN** a validator's self-bond falls below the minimum
- **THEN** it is removed from the active set

### Requirement: Epochs bound validator set changes

Validator set changes, reward distribution, and parameter updates SHALL take effect at declared epoch boundaries. An epoch MUST have a fixed length in blocks and a deterministic schedule.

#### Scenario: Set change at epoch boundary
- **WHEN** a validator is scheduled to join or leave the active set
- **THEN** the change applies at the first block of the next epoch

#### Scenario: Mid-epoch stake change
- **WHEN** a delegation changes mid-epoch
- **THEN** voting power for the current epoch is unchanged and the change applies at the next epoch

### Requirement: Slashing deters equivocation and downtime

The system SHALL slash bonded stake for double-signing and persistent downtime. Evidence MUST be verifiable, attributable, and submitted within a bounded window. Slashed funds MUST be split between the treasury and the reporter according to governance parameters.

#### Scenario: Double-sign slashed
- **WHEN** verifiable evidence shows a validator signed two conflicting blocks at the same height
- **THEN** the validator is jailed, its stake is slashed, and the reporter receives the configured reward

#### Scenario: Downtime slashed
- **WHEN** a validator misses more than the allowed number of blocks in a window
- **THEN** it is jailed and receives the configured downtime penalty

#### Scenario: Stale evidence rejected
- **WHEN** evidence is submitted after the evidence window closes
- **THEN** it is rejected and no slash is applied

### Requirement: Light clients verify consensus without full state

The system SHALL produce commit certificates and validator-set proofs that allow a light client to verify finality and state inclusion without downloading full blocks. Light-client verification MUST reject certificates from an outdated or insufficient validator set.

#### Scenario: Light client verifies a block
- **WHEN** a light client has a trusted validator set and receives a valid commit certificate
- **THEN** it verifies the block is final and can verify state proofs against the block's state root

#### Scenario: Insufficient validator set rejected
- **WHEN** a commit certificate is signed by less than two-thirds of the trusted voting power
- **THEN** the light client rejects it

### Requirement: Public and consortium validator admission are both supported

The protocol SHALL support a public mode where any sufficiently staked validator can join and a consortium mode where validator admission requires approval by the consortium governance policy. Both modes MUST use the same consensus and state-transition rules.

#### Scenario: Public validator joins
- **WHEN** a validator meets the public minimum stake and registration requirements
- **THEN** it becomes eligible without a separate admission vote

#### Scenario: Consortium validator requires approval
- **WHEN** a validator attempts to join a consortium network without approval
- **THEN** it remains pending and cannot vote

#### Scenario: Consortium approval recorded
- **WHEN** the consortium governance policy approves a validator
- **THEN** the approval is recorded on-chain and the validator becomes eligible at the next epoch

### Requirement: Transaction ordering is deterministic and documented

Block proposers SHALL order transactions by a documented deterministic rule that includes fee priority and nonce ordering. The rule MUST be verifiable from block contents and MUST NOT allow a proposer to reorder a transaction after it is committed to a block proposal.

#### Scenario: Same inputs produce same order
- **WHEN** two proposers receive the same mempool and produce a block with the same transaction set
- **THEN** both produce the same deterministic order

#### Scenario: Nonce order preserved
- **WHEN** an account has multiple pending transactions
- **THEN** they are ordered by nonce and a later nonce is not executed before an earlier one

### Requirement: Node state transitions are reproducible across implementations

Given the same genesis, block, and application version, every conforming node SHALL produce the same state root, events, and receipts. A node that diverges MUST halt rather than commit a divergent state.

#### Scenario: Cross-implementation state root matches
- **WHEN** the Go and Python nodes apply the same block to the same prior state
- **THEN** both produce identical state roots and receipts

#### Scenario: Divergence halts the node
- **WHEN** a node computes a state root that differs from the committed root
- **THEN** it halts, emits a consensus-critical alert, and does not commit the block
