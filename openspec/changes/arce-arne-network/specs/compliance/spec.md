# Spec Delta

## Purpose

Defines compliance-ready behavior that deployers can activate without turning the protocol into a regulated intermediary: jurisdiction policy, Travel Rule data exchange, sanctions and KYC interfaces, audit access, retention, and reference profiles for FATF, MiCA, and ISO 20022.

## ADDED Requirements

### Requirement: Jurisdiction policy is explicit, versioned, and deterministic

Each network SHALL declare a jurisdiction policy set that maps assets, participants, and transfer types to requirements. Policy evaluation MUST be deterministic, MUST return a machine-readable decision, and MUST be versioned by activation height.

#### Scenario: Policy allows transfer
- **WHEN** a transfer satisfies every active rule for its asset and participants
- **THEN** the transfer proceeds and the decision is recorded

#### Scenario: Policy blocks transfer
- **WHEN** a transfer violates an active rule
- **THEN** the transfer is rejected with the rule identifier and a human-readable reason

#### Scenario: Historical policy preserved
- **WHEN** a client audits a transfer from a prior height
- **THEN** the decision is evaluated against the policy version active at that height

### Requirement: Travel Rule data is exchanged for qualifying transfers

For transfers above the configured threshold, the system SHALL collect originator and beneficiary information in IVMS101-compatible form, identify the counterparty virtual asset service provider, and exchange the data through an authenticated channel. The data MUST NOT be written to the public ledger.

#### Scenario: Qualifying transfer
- **WHEN** a transfer exceeds the Travel Rule threshold
- **THEN** the required originator and beneficiary data is collected and exchanged before settlement

#### Scenario: Counterparty unavailable
- **WHEN** the counterparty provider cannot be reached
- **THEN** the transfer is held or rejected according to policy and the exception is recorded

#### Scenario: Below threshold
- **WHEN** a transfer is below the threshold
- **THEN** Travel Rule exchange is not required but the decision is logged

### Requirement: Sanctions screening is provider-agnostic and auditable

The system SHALL define a sanctions-screening interface that can bind to one or more providers. Screening MUST occur before settlement for regulated assets and MUST record the list version, provider, result, and decision without storing unnecessary personal data.

#### Scenario: Clear screening
- **WHEN** a participant and transfer pass sanctions screening
- **THEN** the transfer proceeds and the screening record is stored

#### Scenario: Potential match
- **WHEN** screening returns a potential match
- **THEN** the transfer is held for review and the case is recorded

#### Scenario: Provider outage
- **WHEN** the screening provider is unavailable
- **THEN** regulated transfers fail closed and the outage is reported

### Requirement: KYC and identity credentials use selective disclosure

The system SHALL support W3C Verifiable Credentials 2.0 for identity and accreditation. A participant MUST be able to prove a required claim, such as jurisdiction or accredited status, without disclosing the underlying personal data. Raw identity documents MUST NOT be stored on-chain.

#### Scenario: Prove jurisdiction
- **WHEN** a participant presents a valid jurisdiction credential
- **THEN** the verifier learns the jurisdiction claim and nothing else

#### Scenario: Expired credential rejected
- **WHEN** a credential is expired or revoked
- **THEN** the verification fails

#### Scenario: No raw documents on-chain
- **WHEN** a participant completes KYC
- **THEN** only credential references and hashes appear on-chain

### Requirement: Audit access is scoped, time-bounded, and accountable

An auditor SHALL receive only the access granted by a scoped view key or a governance-approved legal request. Every access MUST be logged with the auditor identity, scope, purpose, and time. Access MUST expire automatically.

#### Scenario: Scoped audit
- **WHEN** an auditor is granted access to one asset for one quarter
- **THEN** the auditor can query only that asset and period

#### Scenario: Access expires
- **WHEN** the grant period ends
- **THEN** further queries are rejected

#### Scenario: Access logged
- **WHEN** an auditor queries records
- **THEN** the query is recorded in the audit log

### Requirement: Data retention minimizes personal data

The system SHALL minimize personal data on-chain and in logs, SHALL define retention periods per data class, and SHALL support deletion or anonymization of off-chain personal data where legally required without breaking the immutable ledger.

#### Scenario: Retention expiry
- **WHEN** off-chain personal data reaches its retention limit
- **THEN** it is deleted or anonymized according to policy

#### Scenario: Ledger integrity preserved
- **WHEN** off-chain data is deleted
- **THEN** on-chain hashes and proofs remain verifiable

#### Scenario: Legal hold
- **WHEN** a legal hold applies to a record
- **THEN** deletion is suspended for that record and the hold is logged

### Requirement: MiCA reference profile is available

The system SHALL provide a MiCA reference profile covering crypto-asset white-paper metadata, CASP order-book record fields, ISO 24165 digital token identifiers, and ISO 17442 legal entity identifiers. The profile MUST be selectable per network and MUST NOT be presented as legal advice.

#### Scenario: MiCA profile enabled
- **WHEN** a network enables the MiCA profile
- **THEN** regulated asset metadata includes the required DTI, LEI, and white-paper references

#### Scenario: Missing required field
- **WHEN** an asset is registered without a field required by the active profile
- **THEN** registration is rejected with the missing field

#### Scenario: Profile disclosure
- **WHEN** a deployer views the profile
- **THEN** the system states that the deployer remains responsible for legal compliance

### Requirement: FATF reference profile is available

The system SHALL provide a FATF reference profile implementing the Travel Rule threshold, originator and beneficiary data fields, and record-keeping expectations. The threshold and fields MUST be configurable per jurisdiction.

#### Scenario: FATF profile threshold
- **WHEN** a transfer exceeds the configured FATF threshold
- **THEN** the Travel Rule workflow is triggered

#### Scenario: Jurisdiction override
- **WHEN** a jurisdiction sets a lower threshold
- **THEN** the lower threshold applies to transfers in that jurisdiction

### Requirement: Risk monitoring produces reviewable cases

The system SHALL support transaction-monitoring rules that produce alerts and cases with evidence, status, and assignment. An alert MUST NOT automatically freeze funds unless the active policy explicitly authorizes it.

#### Scenario: Alert created
- **WHEN** a transfer matches a monitoring rule
- **THEN** an alert and case are created with the matching evidence

#### Scenario: Analyst disposition
- **WHEN** an analyst closes a case
- **THEN** the disposition, reason, and reviewer are recorded

#### Scenario: Automatic freeze requires policy
- **WHEN** a rule would freeze funds without an explicit policy authorization
- **THEN** the freeze is not applied and the case is escalated instead

### Requirement: Compliance data never becomes a universal backdoor

The system MUST NOT provide any party with unconditional ability to decrypt all shielded transfers. Disclosure MUST require a user-granted view key, a governance-approved legal process, or a policy-defined predicate. Every exceptional access MUST be logged and reviewable.

#### Scenario: No global decryption key
- **WHEN** an operator attempts to decrypt an arbitrary shielded transfer without a grant
- **THEN** the attempt fails

#### Scenario: Exceptional access recorded
- **WHEN** exceptional access is granted through governance
- **THEN** the scope, approvers, and use are recorded and auditable

#### Scenario: User disclosure
- **WHEN** a user grants a disclosure key
- **THEN** the recipient sees only the granted scope
