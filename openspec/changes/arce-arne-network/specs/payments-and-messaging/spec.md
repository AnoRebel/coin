# Spec Delta

## Purpose

Defines the payment and team-communication layer: invoices and payment intents, payroll and subscriptions, encrypted memos, MLS group channels, delivery semantics, and workspace permissions that make ARNE usable by interconnected teams.

## ADDED Requirements

### Requirement: Payment intents are idempotent and payable once

A payee SHALL be able to create a payment intent with an amount, asset, expiry, reference, and optional memo. A payment intent MUST be payable at most once, and duplicate submissions MUST return the original result rather than moving value twice.

#### Scenario: Pay an intent
- **WHEN** a payer submits a valid payment for an open intent
- **THEN** the intent is marked paid, the transfer executes, and a receipt is issued

#### Scenario: Duplicate payment
- **WHEN** the same idempotency key is submitted twice for one intent
- **THEN** the second submission returns the original receipt and does not move additional value

#### Scenario: Expired intent
- **WHEN** a payer attempts to pay an expired intent
- **THEN** the payment is rejected and the intent remains unpaid

### Requirement: Payment links and URIs resolve to canonical intents

The system SHALL support human-readable payment links and QR codes that resolve to a canonical payment intent on a specific network. A link MUST include the network ID and MUST NOT be payable on a different network.

#### Scenario: Pay by link
- **WHEN** a payer scans a valid payment link
- **THEN** the wallet resolves the intent on the declared network and displays the correct amount and asset

#### Scenario: Cross-network link rejected
- **WHEN** a link for `arne:testnet` is opened on `arne:mainnet`
- **THEN** the wallet refuses to pay and explains the network mismatch

### Requirement: Batch and payroll transfers report per-item outcomes

The system SHALL support batch payments and payroll runs with a declared failure policy of `atomic` or `best_effort`. The result MUST report each item's status and MUST reconcile the total debited amount to the successful items plus fees.

#### Scenario: Atomic batch
- **WHEN** a batch is declared `atomic` and one item fails
- **THEN** no item executes and the batch reports the failing item

#### Scenario: Best-effort batch
- **WHEN** a batch is declared `best_effort` and one item fails
- **THEN** the valid items execute and the result lists each item's status

#### Scenario: Batch reconciliation
- **WHEN** a batch completes
- **THEN** the total debit equals the sum of successful transfers plus the total fee

### Requirement: Recurring payments require explicit authorization

A subscription or recurring payment SHALL require a scoped authorization with a maximum amount per period, a total cap, an expiry, and a cancellation path. The payee MUST NOT charge outside the authorization.

#### Scenario: Scheduled charge within authorization
- **WHEN** a payee charges within the per-period and total limits
- **THEN** the payment executes and the remaining allowance decreases

#### Scenario: Charge above limit rejected
- **WHEN** a payee attempts to charge above the per-period limit
- **THEN** the charge is rejected

#### Scenario: Cancellation
- **WHEN** the payer cancels the authorization
- **THEN** subsequent charges are rejected immediately

### Requirement: Refunds are new transfers and history is immutable

A refund SHALL be executed as a new transfer linked to the original payment reference. The system MUST NOT mutate, delete, or reverse a finalized transaction.

#### Scenario: Refund linked to original
- **WHEN** a payee refunds a payment
- **THEN** a new transfer references the original transaction and both remain queryable

#### Scenario: No history mutation
- **WHEN** any client requests a reversal operation on a finalized transfer
- **THEN** the operation is rejected and the original receipt is unchanged

### Requirement: Memos are encrypted and bounded

Transactions MAY carry a memo. A memo MUST be encrypted to the recipient by default, MUST have a maximum size, and MUST NOT be readable by nodes, indexers, or logs. A sender MAY choose a public memo with an explicit warning.

#### Scenario: Encrypted memo
- **WHEN** a sender attaches an encrypted memo
- **THEN** only the recipient's viewing key can decrypt it

#### Scenario: Oversized memo rejected
- **WHEN** a memo exceeds the maximum size
- **THEN** the transaction is rejected before broadcast

#### Scenario: Public memo warning
- **WHEN** a sender selects a public memo
- **THEN** the wallet warns that the memo is visible to third parties

### Requirement: MLS group channels provide end-to-end encrypted team messaging

The system SHALL support MLS (RFC 9420) group channels with add, remove, update, and commit operations. Group keys MUST rotate on membership change, and removed members MUST NOT decrypt messages sent after their removal.

#### Scenario: Create a channel
- **WHEN** a team creates a channel and adds members
- **THEN** each member receives a valid key package and can decrypt new messages

#### Scenario: Remove a member
- **WHEN** an admin removes a member
- **THEN** the group commits a new epoch and the removed member cannot decrypt later messages

#### Scenario: Forward secrecy
- **WHEN** a member's device is compromised after an epoch change
- **THEN** previously delivered messages remain protected by forward secrecy

### Requirement: Messages support payment references and notices

A channel message SHALL support text, a payment reference, a system notice, and a file pointer. File content MUST be stored off-chain; the channel carries only a content hash and retrieval pointer.

#### Scenario: Attach a payment reference
- **WHEN** a user sends a message referencing a payment
- **THEN** channel members can open the referenced receipt subject to their permissions

#### Scenario: File pointer only
- **WHEN** a user shares a file
- **THEN** the channel carries a hash and pointer, not the file bytes

#### Scenario: Notice delivery
- **WHEN** a system notice is published to a channel
- **THEN** it is ordered in the channel and marked as a system message

### Requirement: Message delivery is at-least-once and deduplicated

The messaging layer SHALL deliver messages at least once, assign each message a stable identifier, and deduplicate retries. Offline members MUST be able to retrieve missed messages within the retention window.

#### Scenario: Retry is deduplicated
- **WHEN** a sender retries a message after a timeout
- **THEN** recipients receive it once

#### Scenario: Offline catch-up
- **WHEN** a member reconnects after being offline
- **THEN** they can retrieve missed messages within the retention window

#### Scenario: Retention expiry
- **WHEN** a message is older than the channel retention policy
- **THEN** it is no longer retrievable through the API

### Requirement: Workspace roles and permissions are enforced

A workspace SHALL support owner, admin, member, and auditor roles. Permissions MUST be checked server-side for every channel, payment, and audit operation, and every privileged action MUST be recorded in an audit log.

#### Scenario: Member cannot remove admin
- **WHEN** a member attempts to remove an admin
- **THEN** the action is rejected

#### Scenario: Auditor read-only access
- **WHEN** an auditor accesses a channel or payment record
- **THEN** the auditor can read but cannot send, pay, or change settings

#### Scenario: Privileged action audited
- **WHEN** an admin changes a role or channel membership
- **THEN** the action is recorded with actor, target, time, and result

### Requirement: Webhooks and notifications are signed and retried

The system SHALL deliver payment and message notifications through signed webhooks with idempotency keys, bounded retries, and replay protection. A receiver MUST be able to verify the signature against a published key.

#### Scenario: Valid webhook
- **WHEN** a payment is finalized and a webhook is delivered
- **THEN** the receiver verifies the signature and processes the event once

#### Scenario: Replay rejected
- **WHEN** a webhook is replayed with an old signature or timestamp
- **THEN** the receiver rejects it

#### Scenario: Retry after failure
- **WHEN** a webhook receiver returns a retryable error
- **THEN** the system retries with backoff up to the configured limit
