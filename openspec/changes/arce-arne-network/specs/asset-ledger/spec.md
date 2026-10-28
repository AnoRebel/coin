# Spec Delta

## Purpose

Defines the multi-asset account ledger: asset registration, balances, transfers, fees, supply invariants, escrow, allowances, and the rules that keep any registered currency or token conserved and spendable exactly once.

## ADDED Requirements

### Requirement: Any asset can be registered with canonical metadata

The system SHALL allow any participant to register an asset with a canonical identifier, ticker, class, decimal precision, issuer, and supply policy. The asset class MUST be one of `fiat`, `crypto`, `stablecoin`, `commodity`, `security`, `point`, or `other`. Optional standards metadata MUST support ISO 4217 currency codes, ISO 24165 digital token identifiers, and ISO 17442 legal entity identifiers.

#### Scenario: Register a fiat asset
- **WHEN** an issuer registers an asset with class `fiat`, ISO 4217 code `TZS`, and 2 decimals
- **THEN** the asset is available for transfers under its canonical identifier with exactly 2 decimal places

#### Scenario: Register a commodity asset
- **WHEN** an issuer registers an asset with class `commodity` and a UCUM unit code
- **THEN** the asset metadata includes the unit and the asset can be transferred in integer base units

#### Scenario: Invalid metadata rejected
- **WHEN** an issuer supplies an unknown class, negative decimals, a duplicate canonical identifier, or a malformed ISO code
- **THEN** registration is rejected and no asset is created

### Requirement: Asset identity and precision are immutable

An asset's canonical identifier, class, decimal precision, and original issuer MUST be immutable after registration. Display names, descriptions, logos, and external references MAY change through a versioned metadata update that preserves prior versions.

#### Scenario: Decimals cannot change
- **WHEN** an issuer attempts to change an existing asset's decimals
- **THEN** the update is rejected and balances remain interpreted at the original precision

#### Scenario: Display metadata update is versioned
- **WHEN** an issuer updates an asset's display name
- **THEN** the new name is visible while the prior metadata version remains retrievable

### Requirement: Supply policies are explicit and enforced

Every asset SHALL declare a supply policy of `fixed`, `capped`, or `mintable`, and MAY declare `burnable`. Mint operations MUST NOT exceed a declared cap. Fixed-supply assets MUST NOT be minted after genesis issuance.

#### Scenario: Capped supply respected
- **WHEN** a mint would raise total supply above the declared cap
- **THEN** the mint is rejected and supply is unchanged

#### Scenario: Fixed supply cannot mint
- **WHEN** an issuer attempts to mint a fixed-supply asset after its initial issuance
- **THEN** the operation is rejected

#### Scenario: Burn reduces supply
- **WHEN** a holder burns units of a burnable asset
- **THEN** total supply decreases by the burned amount and the burned units cannot be spent

### Requirement: Balances are per-asset integer quantities

Account state SHALL store one balance per canonical asset identifier in integer base units. The system MUST NOT use floating-point arithmetic for balances, fees, or supply. A balance MUST never become negative.

#### Scenario: Independent balances
- **WHEN** an account holds 100 units of `arce:mainnet` and 50 units of a registered fiat asset
- **THEN** transferring 10 fiat units leaves the ARCE balance unchanged

#### Scenario: Overspend rejected
- **WHEN** an account attempts to transfer more than its available balance of an asset
- **THEN** the transfer is rejected and no balance changes

#### Scenario: Fractional base unit rejected
- **WHEN** a transfer specifies a non-integer base-unit amount
- **THEN** the transfer is rejected before execution

### Requirement: Transfers are atomic and conserve value

A transfer SHALL debit the sender, credit the recipient, and charge the fee in a single atomic state transition. If any step fails, no part of the transfer MUST be applied. The sum of all balances, escrowed amounts, and burned amounts for an asset MUST equal its minted supply after every block.

#### Scenario: Successful transfer
- **WHEN** a funded account transfers a valid amount to another account with sufficient fee
- **THEN** the sender is debited, the recipient is credited, the fee is charged, and total supply is unchanged

#### Scenario: Failure rolls back
- **WHEN** a transfer fails during fee charging or recipient validation
- **THEN** sender, recipient, fee pool, and supply remain exactly as before

#### Scenario: Conservation violation halts the block
- **WHEN** applying a block would break the supply invariant for any asset
- **THEN** the block is rejected and the node reports a consensus-critical error

### Requirement: Fees can be paid in the transferred asset or ARCE

Every transaction SHALL declare a maximum fee and a fee asset. The network MUST charge the actual fee in the declared asset, and MAY auto-convert another registered asset to ARCE through an authorized liquidity route. A transaction whose maximum fee is below the current minimum MUST be rejected before execution.

#### Scenario: Fee paid in ARCE
- **WHEN** a transaction declares ARCE as the fee asset with a sufficient maximum fee
- **THEN** the fee is charged in ARCE and the transfer executes

#### Scenario: Fee paid in a registered asset
- **WHEN** a transaction declares a registered fiat asset as the fee asset and a valid conversion route exists
- **THEN** the fee is converted and settled without requiring the sender to hold ARCE

#### Scenario: Fee cap exceeded
- **WHEN** the required fee exceeds the transaction's declared maximum fee
- **THEN** the transaction is rejected and not included in a block

### Requirement: Nonces and expiration prevent replay and stale execution

Every account SHALL have a monotonically increasing nonce. A transaction MUST carry the next expected nonce and MAY carry an expiration height or timestamp. The system MUST reject duplicate nonces, expired transactions, and transactions with a nonce gap that exceeds the account's allowed window.

#### Scenario: Sequential nonce accepted
- **WHEN** an account at nonce 7 submits a transaction with nonce 8
- **THEN** the transaction is eligible for execution and the account nonce becomes 8

#### Scenario: Replay rejected
- **WHEN** a previously executed transaction is resubmitted with the same nonce and signature
- **THEN** it is rejected and no value moves

#### Scenario: Expired transaction rejected
- **WHEN** a transaction's expiration height has passed
- **THEN** it is rejected even if its signature and nonce are valid

### Requirement: Escrow and conditional holds are first-class

The system SHALL support escrowed transfers that lock funds until a release condition, timeout, or authorized arbiter acts. Escrowed funds MUST NOT be spendable by the sender and MUST be returned by a deterministic refund path if the condition expires.

#### Scenario: Escrow release
- **WHEN** the release condition is satisfied before expiry
- **THEN** the escrowed amount is credited to the designated recipient and removed from escrow

#### Scenario: Escrow refund
- **WHEN** the escrow expires without release
- **THEN** the escrowed amount is returned to the sender and the escrow is closed

#### Scenario: Double release rejected
- **WHEN** a release is attempted after the escrow has already been released or refunded
- **THEN** the operation is rejected

### Requirement: Allowances are bounded and revocable

Accounts SHALL be able to grant another account an allowance to spend up to a limit of a specific asset until an expiry. Allowances MUST be revocable at any time and MUST NOT exceed the owner's available balance at execution time.

#### Scenario: Delegated transfer within allowance
- **WHEN** a spender transfers an amount within the owner's unexpired allowance
- **THEN** the transfer executes and the allowance decreases by the amount spent

#### Scenario: Allowance exceeded
- **WHEN** a spender attempts to transfer more than the remaining allowance
- **THEN** the transfer is rejected

#### Scenario: Revocation is immediate
- **WHEN** an owner revokes an allowance
- **THEN** subsequent delegated transfers are rejected

### Requirement: Batch transfers are all-or-nothing

The system SHALL support a batch of transfers that executes atomically. If any transfer in the batch fails validation, none of the transfers MUST be applied.

#### Scenario: Batch succeeds
- **WHEN** every transfer in a batch is valid and the total fee is paid
- **THEN** all transfers execute in the declared order

#### Scenario: Batch fails atomically
- **WHEN** one transfer in a batch has insufficient balance
- **THEN** the entire batch is rejected and no balances change

### Requirement: Balances and supply are queryable with proofs

The system SHALL expose current balances, total supply, escrow totals, and burned totals per asset. A client MUST be able to request a cryptographic proof that a balance or supply value is included in the committed state.

#### Scenario: Balance proof verifies
- **WHEN** a client requests a balance proof at a finalized height
- **THEN** the proof verifies against the state root for that height

#### Scenario: Stale proof rejected
- **WHEN** a proof is presented against a state root that does not match the finalized block
- **THEN** verification fails
