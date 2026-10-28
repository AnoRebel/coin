# Spec Delta

## Purpose

Defines ARCE's supply, the fee market, staking rewards, tail emission, auto-conversion, and treasury flows so that validator security is funded without making the network dependent on any single external currency.

## ADDED Requirements

### Requirement: ARCE has a capped supply with bounded tail emission

ARCE SHALL have a hard supply cap and MUST NOT exceed it. After the cap is reached, a tail emission MAY continue at a governance-set annual rate within a hard bound, and MUST be used only for validator rewards and protocol security. Every mint and burn MUST be recorded on-chain.

#### Scenario: Cap never exceeded
- **WHEN** any mint would raise ARCE supply above the hard cap
- **THEN** the mint is rejected or reduced so that supply equals the cap

#### Scenario: Tail emission within bounds
- **WHEN** the cap is reached and tail emission is enabled
- **THEN** the annual rate is within the governance-set hard bound and is recorded per epoch

#### Scenario: Tail emission misuse rejected
- **WHEN** a proposal attempts to direct tail emission to a non-security purpose
- **THEN** the proposal is rejected

### Requirement: Genesis allocation is transparent and vesting-bound

The genesis allocation SHALL publish the amounts assigned to community, contributors, treasury, validators, and liquidity, with vesting and cliff schedules. No insider allocation MUST become transferable before its declared cliff.

#### Scenario: Genesis allocation published
- **WHEN** the network launches
- **THEN** the genesis file lists every allocation, vesting schedule, and beneficiary

#### Scenario: Early unlock rejected
- **WHEN** a vesting account attempts to transfer before its cliff
- **THEN** the transfer of locked units is rejected

#### Scenario: Vesting completes
- **WHEN** the vesting schedule elapses
- **THEN** the remaining units become transferable and the event is recorded

### Requirement: Fees use a predictable base fee plus priority fee

Each transaction SHALL pay a base fee determined by a deterministic congestion rule and MAY pay a priority fee. A governance-set share of the base fee MUST be burned and the remainder distributed to validators and the treasury.

#### Scenario: Base fee rises with congestion
- **WHEN** block utilization exceeds the target
- **THEN** the base fee increases deterministically for subsequent blocks

#### Scenario: Base fee falls with spare capacity
- **WHEN** block utilization is below the target
- **THEN** the base fee decreases deterministically

#### Scenario: Fee split recorded
- **WHEN** a block is finalized
- **THEN** the burn, validator, and treasury portions are recorded and reconcile to the total fees paid

### Requirement: Fees may be paid in any registered asset through auto-conversion

The fee market SHALL accept any registered fee asset. If the fee asset is not ARCE, the protocol MUST obtain ARCE through an authorized conversion route with a user-declared slippage limit. If conversion cannot meet the limit, the transaction MUST be rejected before execution.

#### Scenario: Auto-conversion succeeds
- **WHEN** a user pays fees in a registered fiat asset and a route exists within the slippage limit
- **THEN** the fee is converted to ARCE and the transaction executes

#### Scenario: Slippage exceeded
- **WHEN** the conversion would exceed the user's slippage limit
- **THEN** the transaction is rejected and no fee is charged

#### Scenario: No route available
- **WHEN** no authorized conversion route exists for the fee asset
- **THEN** the transaction is rejected with a `no_fee_route` error

### Requirement: Staking rewards are proportional and epoch-settled

Validator and delegator rewards SHALL be distributed at epoch boundaries in proportion to bonded stake and the validator's commission. Jailed validators MUST NOT earn rewards for the period in which they are jailed.

#### Scenario: Delegator reward
- **WHEN** an epoch ends and a delegator's validator is active
- **THEN** the delegator receives rewards proportional to its bonded stake net of commission

#### Scenario: Jailed validator earns nothing
- **WHEN** a validator is jailed for part of an epoch
- **THEN** it and its delegators receive no rewards for the jailed period

#### Scenario: Reward reconciliation
- **WHEN** an epoch's rewards are distributed
- **THEN** the sum of validator, delegator, and treasury rewards equals the epoch's reward budget

### Requirement: Slashing proceeds are shared by rule

Slashed stake SHALL be split between the treasury, the reporter, and a burn according to governance parameters. The split MUST be recorded with the evidence and MUST NOT exceed the slashed amount.

#### Scenario: Slash distribution
- **WHEN** a validator is slashed
- **THEN** the treasury, reporter, and burn portions are recorded and sum to the slashed amount

#### Scenario: Reporter reward capped
- **WHEN** the reporter reward would exceed its configured cap
- **THEN** the excess is redirected to the treasury or burn according to the rule

### Requirement: Auto-conversion is over-collateralized and auditable

Any protocol-operated conversion route SHALL maintain reserves sufficient to honor outstanding obligations and MUST publish reserve and liability commitments. A route MUST NOT mint ARCE or create unbacked claims.

#### Scenario: Reserve proof verifies
- **WHEN** an auditor verifies a conversion route's reserves
- **THEN** the reserves cover the route's outstanding obligations

#### Scenario: Unbacked route disabled
- **WHEN** a route's reserves fall below its obligations
- **THEN** the route is disabled for new conversions until recapitalized

### Requirement: ARCE is not redeemable for any external currency

ARCE SHALL NOT carry a protocol-level redemption promise for any fiat, commodity, or other asset. Documentation and APIs MUST state that ARCE is a utility asset for fees and staking, not a claim on any reserve.

#### Scenario: No redemption endpoint
- **WHEN** a client searches the API for an ARCE redemption operation
- **THEN** no protocol redemption operation exists

#### Scenario: Clear disclosure
- **WHEN** a user views ARCE metadata
- **THEN** the metadata states that ARCE is not backed by or redeemable for any external currency

### Requirement: Fee sponsorship is bounded

An account SHALL be able to sponsor fees for another account's transactions up to a declared budget, asset, and expiry. A sponsor MUST NOT be able to authorize transfers from the sponsored account.

#### Scenario: Sponsored fee paid
- **WHEN** a sponsored transaction executes within the sponsor's budget
- **THEN** the sponsor pays the fee and the sender's transfer executes

#### Scenario: Budget exhausted
- **WHEN** the sponsor's budget is exhausted
- **THEN** further sponsored transactions are rejected unless another fee source is provided

#### Scenario: Sponsor cannot move funds
- **WHEN** a sponsor attempts to transfer value from the sponsored account
- **THEN** the attempt is rejected
