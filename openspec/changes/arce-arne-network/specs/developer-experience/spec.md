# Spec Delta

## Purpose

Defines the tools developers and operators use to build on ARNE: Go and Python SDKs, a unified CLI, code generation, local development workflows, examples, and documentation that stays accurate.

## ADDED Requirements

### Requirement: Go and Python SDKs expose the full public API

The project SHALL provide Go and Python SDKs covering wallets, transactions, assets, payments, escrow, messaging, governance, connectors, and subscriptions. Both SDKs MUST be generated from the shared schemas where possible and MUST expose typed errors.

#### Scenario: Send a payment with the Go SDK
- **WHEN** a developer follows the Go quickstart
- **THEN** they can create a wallet, fund it on devnet, and send a payment

#### Scenario: Send a payment with the Python SDK
- **WHEN** a developer follows the Python quickstart
- **THEN** they can perform the same flow with equivalent behavior

#### Scenario: Typed errors
- **WHEN** an SDK call fails
- **THEN** the caller receives a typed error with a stable code and retry guidance

### Requirement: CLI covers node, wallet, asset, governance, connector, and devnet operations

The project SHALL provide a single CLI with documented commands for running a node, managing wallets, registering assets, sending payments, creating escrows, participating in governance, managing connectors, and starting a devnet. Every command MUST support machine-readable output.

#### Scenario: Node operations
- **WHEN** an operator runs the documented node command
- **THEN** the node starts with the selected profile and logs structured output

#### Scenario: Wallet operations
- **WHEN** a user creates a wallet and sends a payment through the CLI
- **THEN** the operations complete and the receipt is printed

#### Scenario: Machine-readable output
- **WHEN** a command is run with JSON output enabled
- **THEN** it emits valid JSON suitable for automation

### Requirement: Code generation is reproducible and checked in CI

SDK types, API clients, protobuf bindings, and documentation references SHALL be generated from shared schemas. CI MUST verify that generated artifacts match a fresh generation and MUST fail on drift.

#### Scenario: Regeneration
- **WHEN** a developer runs the documented generation command
- **THEN** the repository contains updated generated artifacts

#### Scenario: Drift detection
- **WHEN** generated artifacts are stale
- **THEN** CI fails with the command needed to regenerate them

#### Scenario: Generated docs
- **WHEN** an API schema changes
- **THEN** the reference documentation is regenerated

### Requirement: Local development is one command

The project SHALL provide a documented one-command workflow that installs dependencies, starts a devnet, seeds fixtures, and runs the test suite. Python development MUST use `uv`; Go development MUST support `air` for live reload.

#### Scenario: Bootstrap
- **WHEN** a developer runs the documented bootstrap command on a clean checkout
- **THEN** dependencies are installed and the test suite runs

#### Scenario: Live reload
- **WHEN** a Go source file changes during development
- **THEN** the node rebuilds and restarts automatically

#### Scenario: Seeded devnet
- **WHEN** the devnet starts
- **THEN** it includes funded test accounts, registered assets, and sample payments

### Requirement: Examples cover real team and payment workflows

The project SHALL include runnable examples for accepting a payment, payroll, escrow, subscriptions, invoices, team messaging, asset issuance, and a bank or EVM connector. Each example MUST state its prerequisites and expected output.

#### Scenario: Example runs
- **WHEN** a developer runs an example on the devnet
- **THEN** it completes with the documented output

#### Scenario: Example failure
- **WHEN** a prerequisite is missing
- **THEN** the example fails with a clear instruction

#### Scenario: Example tested in CI
- **WHEN** the examples are changed
- **THEN** CI runs the runnable examples against a devnet

### Requirement: Documentation is versioned and tested

The project SHALL publish quickstarts, concepts, API reference, operator guides, security guidance, and troubleshooting for each supported release. Documented commands and code samples MUST be tested in CI or clearly marked as illustrative.

#### Scenario: Quickstart accuracy
- **WHEN** a new user follows the quickstart on a clean machine
- **THEN** every command succeeds as written

#### Scenario: Versioned docs
- **WHEN** a user selects a release version
- **THEN** the documentation matches that release's API and behavior

#### Scenario: Broken link
- **WHEN** documentation contains a broken internal link
- **THEN** CI fails

### Requirement: Error messages guide developers to a fix

SDK and CLI errors SHALL include a stable code, a human-readable explanation, the offending field or input, and a link or hint for resolution. Errors MUST NOT expose secrets or internal stack traces in production mode.

#### Scenario: Invalid input
- **WHEN** a developer supplies an invalid address
- **THEN** the error names the field, the expected format, and a documentation link

#### Scenario: Retryable failure
- **WHEN** a transient network error occurs
- **THEN** the error indicates whether retry is safe and the recommended backoff

#### Scenario: Production redaction
- **WHEN** an error occurs in production mode
- **THEN** the response contains no stack trace or secret
