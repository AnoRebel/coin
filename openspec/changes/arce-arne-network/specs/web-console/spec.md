# Spec Delta

## Purpose

Defines the human-facing console: a FastAPI-based explorer, wallet, and team workspace that is accessible, responsive, privacy-respecting, and safe with keys.

## ADDED Requirements

### Requirement: Console is a FastAPI application with server-rendered pages

The console SHALL be built on FastAPI and SHALL serve accessible server-rendered pages, progressively enhanced with real-time updates. It MUST expose no private keys to the server for normal wallet use.

#### Scenario: Load the explorer
- **WHEN** a user opens the console
- **THEN** the explorer page renders blocks, transactions, and network status

#### Scenario: Wallet signing is client-side
- **WHEN** a user sends a payment
- **THEN** signing happens in the browser or a connected signer, and the server receives only the signed envelope

#### Scenario: Progressive enhancement
- **WHEN** JavaScript is unavailable
- **THEN** core read-only pages still render and navigate

### Requirement: Explorer covers blocks, transactions, accounts, assets, validators, and governance

The console SHALL provide searchable views for blocks, transactions, accounts, assets, validators, proposals, and connectors, with filters, pagination, and links between related objects. Every view MUST show the canonical network and height.

#### Scenario: Search a transaction
- **WHEN** a user searches a transaction hash
- **THEN** the console shows its status, block, transfers, fee, memos (if authorized), and events

#### Scenario: Asset view
- **WHEN** a user opens an asset
- **THEN** the console shows metadata, supply, holders, transfers, and contract or issuer details

#### Scenario: Validator view
- **WHEN** a user opens a validator
- **THEN** the console shows stake, voting power, uptime, commission, and slashing history

### Requirement: Wallet supports create, import, send, receive, history, and staking

The console wallet SHALL support creating and importing HD wallets, displaying balances per asset, sending and requesting payments, viewing history, and delegating stake. It MUST require explicit confirmation for every state-changing action.

#### Scenario: Create a wallet
- **WHEN** a user creates a wallet
- **THEN** the console generates a mnemonic locally, requires backup confirmation, and shows the first address

#### Scenario: Send a payment
- **WHEN** a user confirms a payment
- **THEN** the console signs locally and displays the resulting transaction status

#### Scenario: Delegate stake
- **WHEN** a user delegates to a validator
- **THEN** the console shows the validator, amount, fee, and expected reward before confirmation

### Requirement: Team workspace supports channels, payments, invoices, and roles

The console SHALL provide a workspace with MLS-encrypted channels, payment requests, invoices, payroll runs, and role-based access. A user MUST see only the workspaces and channels they are authorized to access.

#### Scenario: Team channel
- **WHEN** a member opens a channel
- **THEN** they see decrypted messages for which they hold keys

#### Scenario: Invoice flow
- **WHEN** a member creates an invoice
- **THEN** the payer can open, review, and pay it from the workspace

#### Scenario: Role enforcement
- **WHEN** a member without admin rights opens workspace settings
- **THEN** privileged controls are hidden and server-side checks reject unauthorized actions

### Requirement: Real-time updates are resilient

The console SHALL use WebSocket or Server-Sent Events for new blocks, transaction status, and messages, with automatic reconnect, cursor resume, and a visible connection state.

#### Scenario: New block appears
- **WHEN** a new block is finalized
- **THEN** the open explorer view updates without a full page reload

#### Scenario: Reconnect
- **WHEN** the connection drops and returns
- **THEN** the console resumes from the last cursor and shows a brief reconnecting indicator

#### Scenario: Stale connection
- **WHEN** updates stop arriving
- **THEN** the console shows a stale-data warning and offers a refresh

### Requirement: Accessibility meets WCAG 2.2 AA

The console SHALL meet WCAG 2.2 AA for keyboard navigation, focus visibility, semantic landmarks, form labels, error identification, color contrast, reduced motion, and screen-reader announcements for live updates.

#### Scenario: Keyboard-only flow
- **WHEN** a user navigates and sends a payment using only the keyboard
- **THEN** every control is reachable and operable with visible focus

#### Scenario: Screen-reader update
- **WHEN** a new block or message arrives
- **THEN** the update is announced politely without stealing focus

#### Scenario: Reduced motion
- **WHEN** the user prefers reduced motion
- **THEN** non-essential animations are disabled

### Requirement: Console is responsive and internationalized

The console SHALL support mobile, tablet, and desktop layouts and SHALL format amounts, dates, and numbers according to the user's locale while preserving exact base-unit values. User-facing strings MUST be externalized for translation.

#### Scenario: Mobile layout
- **WHEN** the console is opened on a small screen
- **THEN** navigation, tables, and payment forms remain usable without horizontal scrolling

#### Scenario: Locale formatting
- **WHEN** a user selects a locale
- **THEN** dates and display amounts are formatted for that locale and the underlying exact value is available

#### Scenario: Translation missing
- **WHEN** a string has no translation
- **THEN** the console falls back to the default language without layout breakage

### Requirement: Console security and privacy are enforced

The console SHALL use secure session cookies, CSRF protection, strict Content Security Policy, and origin checks. It MUST NOT send mnemonic phrases, private keys, or decrypted memos to the server, and analytics MUST be off by default.

#### Scenario: CSRF attempt
- **WHEN** a cross-origin request attempts a state-changing action
- **THEN** the console rejects it

#### Scenario: Secret never leaves the client
- **WHEN** a wallet signs a transaction
- **THEN** the network request contains the signed envelope, not the private key or mnemonic

#### Scenario: Analytics default
- **WHEN** a deployment does not explicitly enable analytics
- **THEN** no third-party analytics requests are made

### Requirement: Console performance is budgeted

The console SHALL meet documented performance budgets for initial load, interaction latency, and bundle size on a reference connection and device. A regression MUST be reported in CI.

#### Scenario: Initial load
- **WHEN** a user opens the explorer on the reference connection
- **THEN** the main content is interactive within the documented budget

#### Scenario: Large table
- **WHEN** a table contains thousands of rows
- **THEN** it virtualizes or paginates and remains responsive

#### Scenario: Budget regression
- **WHEN** bundle size exceeds its budget
- **THEN** CI fails or requires an explicit waiver
