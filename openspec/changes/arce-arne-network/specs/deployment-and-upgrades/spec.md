# Spec Delta

## Purpose

Defines how ARNE is deployed and upgraded across laptops, small teams, consortiums, and public networks without losing state, finality, or the ability to roll back safely.

## ADDED Requirements

### Requirement: A single binary runs a complete devnet node

The project SHALL ship a single Go binary that can initialize, run, and expose a complete single-node devnet without external services. The binary MUST support a documented `devnet` profile with deterministic genesis and a faucet.

#### Scenario: Start a devnet
- **WHEN** a developer runs the documented devnet command on a clean machine
- **THEN** a node starts, produces blocks, and exposes the API and console

#### Scenario: Deterministic genesis
- **WHEN** two developers start the devnet profile with the same version
- **THEN** both use the same genesis hash

#### Scenario: No external dependency
- **WHEN** no Postgres, Redis, or external consensus service is running
- **THEN** the devnet profile still starts and finalizes blocks

### Requirement: Docker Compose supports small teams

The project SHALL provide a Docker Compose profile with a node, validator set, indexer, console, and optional connectors. The profile MUST use secure defaults, persistent volumes, health checks, and documented environment variables.

#### Scenario: Compose stack starts
- **WHEN** an operator runs the documented Compose command
- **THEN** all services become healthy and the network finalizes blocks

#### Scenario: Persistent restart
- **WHEN** the stack is restarted
- **THEN** chain state and configuration persist

#### Scenario: Health-gated startup
- **WHEN** the indexer starts before the node is ready
- **THEN** it retries and becomes healthy without manual intervention

### Requirement: Helm supports consortium and production deployments

The project SHALL provide a Helm chart with configurable replicas, resource limits, persistent storage, TLS, network policies, pod disruption budgets, and separate validator, sentry, indexer, console, and connector workloads.

#### Scenario: Production install
- **WHEN** an operator installs the chart with production values
- **THEN** the node, API, and indexer run with TLS, resource limits, and network policies

#### Scenario: Validator isolation
- **WHEN** a validator is deployed
- **THEN** its key material is isolated from public API workloads

#### Scenario: Rolling restart
- **WHEN** a non-consensus workload is upgraded
- **THEN** it restarts without interrupting finality

### Requirement: Terraform provisions cloud infrastructure reproducibly

The project SHALL provide Terraform modules for network, compute, storage, database, load balancing, secrets, and observability across at least one major cloud. Modules MUST be versioned and MUST NOT embed secrets.

#### Scenario: Provision an environment
- **WHEN** an operator applies the documented Terraform module
- **THEN** the network, nodes, database, and monitoring are created with least-privilege access

#### Scenario: No embedded secrets
- **WHEN** the Terraform plan is reviewed
- **THEN** no plaintext secret appears in the plan or state

#### Scenario: Destroy
- **WHEN** the environment is destroyed
- **THEN** persistent data is retained or explicitly deleted according to the configured policy

### Requirement: Network profiles isolate devnet, testnet, consortium, and mainnet

Each network profile SHALL have its own network ID, genesis, parameters, seed peers, and release channel. A node MUST NOT join a network with a mismatched profile, and test assets MUST NOT be usable on mainnet.

#### Scenario: Profile mismatch rejected
- **WHEN** a testnet node attempts to join mainnet
- **THEN** the connection is rejected with a network mismatch

#### Scenario: Test asset isolation
- **WHEN** a testnet asset identifier is used on mainnet
- **THEN** the transfer is rejected because the asset is not registered there

#### Scenario: Separate seeds
- **WHEN** a node starts with the testnet profile
- **THEN** it uses only testnet seed peers

### Requirement: Upgrades activate at a coordinated height

Consensus-affecting upgrades SHALL activate at a declared height after a governance decision and a supermajority of validators have installed the approved binary. Non-consensus services MAY use rolling or canary deployment.

#### Scenario: Coordinated activation
- **WHEN** the activation height is reached and the supermajority runs the approved binary
- **THEN** the new rules apply from that block

#### Scenario: Insufficient upgrade adoption
- **WHEN** the supermajority has not installed the approved binary by the activation height
- **THEN** activation is postponed and operators are alerted

#### Scenario: Canary API
- **WHEN** a non-consensus API change is deployed
- **THEN** a canary receives traffic first and is rolled back automatically on error-rate regression

### Requirement: Rollback is rehearsed and state-safe

Every upgrade SHALL have a documented rollback or restore procedure, and the project MUST rehearse it on a production-like network before mainnet activation. A rollback MUST NOT corrupt state or violate finality.

#### Scenario: Rollback rehearsal
- **WHEN** a release candidate is tested
- **THEN** the documented rollback is executed on a production-like network and verified

#### Scenario: Rollback after activation
- **WHEN** an operator rolls back after an activation height
- **THEN** the node restores a compatible state and rejoins without forking

#### Scenario: Irreversible migration disclosed
- **WHEN** an upgrade includes an irreversible state migration
- **THEN** the rollback plan is a verified restore, and this is stated before activation

### Requirement: Validator keys are separated and backed up

Validator consensus keys and account keys MUST be separated, stored in an HSM or protected store, backed up with encryption, and restored through a documented ceremony. A validator MUST NOT expose its consensus key to the public API or console.

#### Scenario: Key separation
- **WHEN** a validator is configured
- **THEN** its consensus key is not available to the API or console processes

#### Scenario: Encrypted backup
- **WHEN** a validator key is backed up
- **THEN** the backup is encrypted and verified by restore in a safe environment

#### Scenario: Key rotation
- **WHEN** a validator rotates its consensus key
- **THEN** the new key is registered and the old key is retired without downtime

### Requirement: Time synchronization is monitored

Nodes SHALL monitor clock skew and warn or halt when skew exceeds the BFT-safe threshold. Operators MUST be able to configure NTP or chrony and MUST receive an alert on drift.

#### Scenario: Clock drift alert
- **WHEN** clock skew exceeds the warning threshold
- **THEN** the node alerts and logs the measured skew

#### Scenario: Unsafe skew
- **WHEN** clock skew exceeds the safe threshold
- **THEN** the validator stops signing until time is corrected

#### Scenario: Time source unavailable
- **WHEN** the configured time source becomes unreachable
- **THEN** the node reports degraded time sync
