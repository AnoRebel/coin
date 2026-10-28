# Spec Delta

## Purpose

Defines how operators observe ARNE health and behavior: metrics, tracing, structured logs, tamper-evident audit logs, dashboards, SLOs, and privacy-safe telemetry.

## ADDED Requirements

### Requirement: Metrics cover every critical subsystem

The node SHALL expose metrics for consensus height and finality latency, validator participation, mempool size and rejection reasons, peer count and gossip, storage latency, API latency and errors, connector health, and resource usage. Metrics MUST use stable names and documented labels.

#### Scenario: Consensus metrics available
- **WHEN** an operator scrapes the metrics endpoint
- **THEN** consensus height, finality latency, and validator participation are present

#### Scenario: Mempool metrics
- **WHEN** transactions are rejected
- **THEN** counters increment by documented rejection reason

#### Scenario: Stable labels
- **WHEN** a metric label is renamed or removed
- **THEN** the change follows the documented deprecation policy

### Requirement: Distributed tracing follows W3C Trace Context

The node SHALL propagate W3C Trace Context across API, consensus, storage, peer, and connector boundaries. Traces MUST include span status and MUST NOT include secrets, private keys, note plaintext, or personal data.

#### Scenario: End-to-end trace
- **WHEN** a client submits a transaction with tracing enabled
- **THEN** the trace shows API admission, mempool, consensus inclusion, execution, and finality spans

#### Scenario: Trace privacy
- **WHEN** a trace is exported
- **THEN** it contains no private keys, note plaintext, or unredacted personal data

#### Scenario: Sampling
- **WHEN** trace volume exceeds the configured rate
- **THEN** the node samples according to policy without losing error traces

### Requirement: Logs are structured, correlated, and redacted

The node SHALL emit structured JSON logs with timestamp, level, component, correlation ID, and stable event names. Logs MUST redact secrets and personal data and MUST support configurable retention.

#### Scenario: Correlated request log
- **WHEN** an API request is handled
- **THEN** its logs share a correlation ID across components

#### Scenario: Secret redaction
- **WHEN** an error includes a credential or key
- **THEN** the log contains a redacted placeholder, not the secret

#### Scenario: Log level control
- **WHEN** an operator changes the log level at runtime
- **THEN** the new level takes effect without a restart

### Requirement: Audit logs are append-only and tamper-evident

Privileged actions, governance changes, compliance decisions, key operations, and administrative API calls SHALL be written to an append-only audit log with hash chaining. Tampering MUST be detectable, and audit logs MUST be exportable for external retention.

#### Scenario: Privileged action recorded
- **WHEN** an admin changes a role or parameter
- **THEN** the audit log records actor, action, target, time, result, and prior value

#### Scenario: Tamper detected
- **WHEN** a record in the audit chain is altered or removed
- **THEN** verification reports the first broken link

#### Scenario: Export
- **WHEN** an auditor exports the audit log for a period
- **THEN** the export includes verification metadata

### Requirement: Dashboards and alerts cover the golden signals

The project SHALL ship dashboards and alert rules for latency, traffic, errors, saturation, finality, validator liveness, connector health, and certificate or key expiry. Alerts MUST have a runbook link and a severity.

#### Scenario: Finality alert
- **WHEN** finality latency exceeds its SLO for the configured window
- **THEN** a high-severity alert fires with a runbook link

#### Scenario: Validator liveness alert
- **WHEN** a validator misses more than the allowed blocks
- **THEN** an alert fires before the downtime slash threshold

#### Scenario: Certificate expiry alert
- **WHEN** a TLS or connector certificate is near expiry
- **THEN** an alert fires with the expiry date

### Requirement: SLOs are published and measured

Each service SHALL publish availability, latency, and correctness SLOs, and MUST report error budgets against them. A breach MUST trigger a review and a recorded remediation.

#### Scenario: API availability SLO
- **WHEN** a monthly availability report is generated
- **THEN** it shows the target, actual value, and remaining error budget

#### Scenario: Error budget exhausted
- **WHEN** an error budget is exhausted
- **THEN** the release process requires a reliability review before further feature work

### Requirement: Debug endpoints are protected

Profiling, tracing, and debug endpoints MUST be disabled by default in production, bound to localhost or an administrative interface, and protected by authentication and authorization.

#### Scenario: Debug disabled by default
- **WHEN** a node starts with production configuration
- **THEN** profiling and debug endpoints are not publicly reachable

#### Scenario: Authorized debug access
- **WHEN** an operator enables debug access on the admin interface
- **THEN** only authenticated admin clients can use it

### Requirement: Telemetry is privacy-preserving

Telemetry SHALL minimize personal data, MUST NOT include transaction memos or note plaintext, and MUST support opt-out or redaction for consortium deployments. Aggregated metrics MUST NOT allow re-identification of individual participants.

#### Scenario: No memo in telemetry
- **WHEN** a transaction with an encrypted memo is processed
- **THEN** no telemetry contains the memo plaintext or ciphertext

#### Scenario: Consortium opt-out
- **WHEN** a consortium disables external telemetry
- **THEN** only local metrics and logs are retained

#### Scenario: Aggregation threshold
- **WHEN** telemetry is aggregated for reporting
- **THEN** groups below the minimum size are suppressed
