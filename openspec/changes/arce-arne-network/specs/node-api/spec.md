# Spec Delta

## Purpose

Defines the stable, versioned programmatic surface of an ARNE node: REST, JSON-RPC, WebSocket subscriptions, authentication, idempotency, error semantics, pagination, and rate limiting.

## ADDED Requirements

### Requirement: REST API is versioned and documented

The system SHALL expose a `/v1` REST API with an OpenAPI description covering every public endpoint, request schema, response schema, and error code. A breaking change MUST require a new major version, and the prior version MUST remain available for a declared deprecation period.

#### Scenario: OpenAPI describes live API
- **WHEN** a client fetches the OpenAPI document
- **THEN** every documented endpoint exists and matches the live request and response schemas

#### Scenario: Breaking change versioned
- **WHEN** an endpoint's response shape changes incompatibly
- **THEN** the change is released under a new major version and the old version remains available during deprecation

#### Scenario: Deprecated endpoint
- **WHEN** a client calls a deprecated endpoint
- **THEN** the response includes a deprecation notice with the sunset date

### Requirement: JSON-RPC 2.0 exposes node operations

The system SHALL expose a JSON-RPC 2.0 endpoint with standard request, response, batch, and error objects. Every method MUST be documented with its parameters, result schema, and stable error codes.

#### Scenario: Valid JSON-RPC call
- **WHEN** a client sends a valid method call
- **THEN** the response contains the result and the matching request ID

#### Scenario: Batch call
- **WHEN** a client sends a batch of independent calls
- **THEN** each call receives its own result or error and the batch preserves request IDs

#### Scenario: Unknown method
- **WHEN** a client calls an unknown method
- **THEN** the response is a JSON-RPC method-not-found error

### Requirement: WebSocket subscriptions are ordered and resumable

The system SHALL provide WebSocket subscriptions for new blocks, transaction status, contract events, governance proposals, and workspace messages. Events MUST carry a monotonic sequence or height, and a client MUST be able to resume from the last seen cursor within retention.

#### Scenario: New block subscription
- **WHEN** a client subscribes to new blocks
- **THEN** it receives finalized blocks in height order

#### Scenario: Resume after disconnect
- **WHEN** a client reconnects with its last cursor
- **THEN** it receives events after that cursor without duplicates

#### Scenario: Cursor too old
- **WHEN** a client's cursor is older than retention
- **THEN** the server returns a clear `cursor_expired` error with the earliest available cursor

### Requirement: Authentication and authorization are scoped

The API SHALL support API keys for service access, OIDC/JWT for user access, and mutual TLS for administrative access. Every credential MUST carry scopes, and a request MUST be rejected if it lacks the required scope.

#### Scenario: Valid scoped key
- **WHEN** a request presents a key with the required scope
- **THEN** the operation is authorized

#### Scenario: Missing scope
- **WHEN** a key lacks the scope required by the endpoint
- **THEN** the request is rejected with a forbidden error

#### Scenario: Admin endpoint
- **WHEN** a client calls an administrative endpoint without mutual TLS or an admin scope
- **THEN** the request is rejected

### Requirement: Write operations support idempotency keys

Every state-changing REST or JSON-RPC operation SHALL accept an idempotency key. Repeating the same key with the same payload MUST return the original result; repeating it with a different payload MUST return a conflict.

#### Scenario: Retry returns original result
- **WHEN** a client retries a write with the same idempotency key and payload
- **THEN** the server returns the original result without repeating the effect

#### Scenario: Key reuse with different payload
- **WHEN** a client reuses an idempotency key with a different payload
- **THEN** the server returns a conflict error

#### Scenario: Key expiry
- **WHEN** an idempotency key is older than its retention period
- **THEN** the server treats it as new and documents the retention window

### Requirement: Errors are stable, typed, and leak no internals

The API SHALL return a stable machine-readable error code, a human-readable message, and optional field-level details. Errors MUST NOT expose stack traces, internal paths, secrets, or raw database errors.

#### Scenario: Validation error
- **WHEN** a request contains an invalid field
- **THEN** the response includes the field path and a stable validation code

#### Scenario: Internal failure
- **WHEN** an unexpected internal error occurs
- **THEN** the response contains a generic error code and a correlation ID, not internal details

#### Scenario: Rate limited
- **WHEN** a client exceeds its rate limit
- **THEN** the response contains a rate-limit error and a retry-after value

### Requirement: List endpoints support pagination and filtering

List endpoints SHALL support cursor-based pagination, explicit sort order, and documented filters. Responses MUST include a next cursor when more results exist and MUST NOT silently truncate results.

#### Scenario: First page
- **WHEN** a client requests a list without a cursor
- **THEN** the response returns the first page and a next cursor if more results exist

#### Scenario: Last page
- **WHEN** a client requests the final page
- **THEN** the next cursor is absent and the result count is accurate

#### Scenario: Invalid filter
- **WHEN** a client supplies an unsupported filter or sort field
- **THEN** the request is rejected with the allowed values

### Requirement: Transaction submission is validated and observable

The API SHALL accept a signed transaction envelope, validate its signature, nonce, fee, and format, and return a transaction identifier and initial status. A client MUST be able to poll or subscribe to the transaction's status through finality.

#### Scenario: Valid transaction accepted
- **WHEN** a client submits a valid signed transaction
- **THEN** the API returns the transaction identifier and `accepted` status

#### Scenario: Invalid signature rejected
- **WHEN** a client submits a transaction with an invalid signature
- **THEN** the API rejects it with a signature error and does not admit it to the mempool

#### Scenario: Status to finality
- **WHEN** a client subscribes to a submitted transaction
- **THEN** it observes status transitions to `finalized` or `failed` with a receipt

### Requirement: Dry-run simulation is available before submission

The API SHALL provide a simulation endpoint that executes a transaction against current state without committing it, returning the expected result, fee, events, and any failure reason.

#### Scenario: Successful simulation
- **WHEN** a client simulates a valid transaction
- **THEN** the response contains the expected state changes, fee, and events without committing them

#### Scenario: Simulation failure
- **WHEN** a simulated transaction would fail
- **THEN** the response contains the failure reason and the simulation leaves state unchanged

### Requirement: Health, readiness, and version are exposed

The node SHALL expose liveness, readiness, and version endpoints. Readiness MUST fail when the node cannot serve its configured role, and the version response MUST include the protocol version, build commit, and supported API versions.

#### Scenario: Healthy node
- **WHEN** an orchestrator probes liveness and readiness on a healthy node
- **THEN** both return success

#### Scenario: Not ready
- **WHEN** the node is syncing or cannot reach its storage
- **THEN** readiness fails while liveness remains successful

#### Scenario: Version discovery
- **WHEN** a client requests the version
- **THEN** the response includes the protocol version, build commit, and supported API versions
