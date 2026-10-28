# Spec Delta

## Purpose

Defines the two-implementation contract: Go as the canonical full node on `main`, Python as a full parity node on the `python` branch, both driven by shared schemas and conformance vectors so the network cannot fork by implementation.

## ADDED Requirements

### Requirement: Go is the canonical full node and Python is a full parity node

The `main` branch SHALL contain the Go full node, including consensus, P2P, storage, API, wallet, CLI, and connectors. The `python` branch SHALL contain a full Python node with the same externally observable behavior. Both MUST join the same network and interoperate.

#### Scenario: Same-network interoperability
- **WHEN** a Go node and a Python node join the same testnet
- **THEN** they exchange transactions and blocks and agree on the finalized chain

#### Scenario: Python node produces a block
- **WHEN** a Python validator is selected as proposer
- **THEN** Go validators accept and finalize its block

#### Scenario: Feature parity check
- **WHEN** a protocol feature is released
- **THEN** both implementations expose the same protocol behavior and API contract, or the release notes declare an explicit temporary gap

### Requirement: Shared schemas are the single source of truth

All wire messages, transaction envelopes, state keys, API objects, and error codes SHALL be defined in shared schemas under a language-neutral `spec/` directory. Generated code MUST NOT be hand-edited, and a schema change MUST regenerate both implementations in CI.

#### Scenario: Schema drives both implementations
- **WHEN** a field is added to a shared message schema
- **THEN** both Go and Python generated types include the field after regeneration

#### Scenario: Hand-edited generated code
- **WHEN** generated code differs from a fresh generation
- **THEN** CI fails

#### Scenario: Breaking schema change
- **WHEN** a schema change is incompatible
- **THEN** the protocol version is incremented and a migration path is documented

### Requirement: Conformance vectors cover all consensus-critical behavior

The repository SHALL include language-neutral test vectors for canonical encoding, hashing, signatures, key derivation, transaction validation, state transitions, fee accounting, governance, shielded proofs, and contract execution. Both implementations MUST pass every vector.

#### Scenario: Encoding vectors
- **WHEN** both implementations encode the same logical message
- **THEN** each produces the exact bytes in the vector

#### Scenario: State transition vectors
- **WHEN** both implementations apply the same block fixture
- **THEN** each produces the expected state root and receipts

#### Scenario: Vector failure blocks release
- **WHEN** either implementation fails a conformance vector
- **THEN** the release is blocked

### Requirement: Differential testing runs on every change

CI SHALL run differential tests that feed identical random and fixture inputs to both implementations and compare outputs, errors, state roots, gas usage, and API responses. A divergence MUST be treated as a release-blocking bug.

#### Scenario: Differential transaction fuzzing
- **WHEN** a random transaction is submitted to both implementations
- **THEN** both return the same acceptance decision, error code, and state effect

#### Scenario: Differential state root
- **WHEN** a block fixture is applied to both
- **THEN** both compute the same state root

#### Scenario: Divergence reported
- **WHEN** outputs differ
- **THEN** CI reports the input, both outputs, and the first differing field

### Requirement: Branch synchronization is automated

The `python` branch SHALL consume shared schemas and vectors from `main` through a documented mechanism such as a subtree, submodule, or generated artifact. A synchronization job MUST detect drift and open a change or fail the build.

#### Scenario: Schema drift detected
- **WHEN** `main` changes a shared schema and `python` has not regenerated
- **THEN** the synchronization job fails with the missing version

#### Scenario: Python branch update
- **WHEN** `main` publishes a new conformance vector set
- **THEN** the `python` branch can consume it without manual copying

#### Scenario: Release alignment
- **WHEN** a release is tagged on `main`
- **THEN** the corresponding `python` release is tagged with the same protocol version

### Requirement: Performance floors are defined for the Python node

The Python node SHALL meet documented minimum performance floors for block execution, transaction validation, API latency, and sync throughput on reference hardware. Falling below a floor MUST be reported as a release risk, not silently accepted.

#### Scenario: Block execution floor
- **WHEN** the Python node executes the reference block benchmark
- **THEN** it completes within the documented time floor

#### Scenario: API latency floor
- **WHEN** the Python node serves the reference API benchmark
- **THEN** p95 latency is within the documented floor

#### Scenario: Floor regression
- **WHEN** a change causes a floor regression
- **THEN** the benchmark job reports it and the release requires an explicit waiver

### Requirement: Compatibility matrix is published per release

Each release SHALL publish a compatibility matrix covering Go and Python versions, protocol versions, API versions, storage schema versions, and supported network profiles. A node MUST refuse an unsupported combination with a clear error.

#### Scenario: Supported combination
- **WHEN** an operator runs a combination listed in the matrix
- **THEN** the node starts and interoperates

#### Scenario: Unsupported combination
- **WHEN** an operator runs a combination absent from the matrix
- **THEN** the node refuses to start or clearly warns and limits functionality

#### Scenario: Matrix accuracy
- **WHEN** the release is tested
- **THEN** every listed combination has an automated smoke test
