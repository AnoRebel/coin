# Spec Delta

## Purpose

Defines the canonical names, tickers, namespaced identifiers, network identity, and protocol versioning that every ARNE component and client must use to avoid ambiguity and cross-network replay.

## ADDED Requirements

### Requirement: Canonical project and ticker names

The system SHALL use `AnoRebelCoin` with ticker `ARCE` for the native asset and `AnoRebelNetwork` with ticker `ARNE` for the network. The legacy names `RebelCoin`, `ARC`, and `ARN` alone MUST NOT be used as canonical identifiers in new APIs, wire messages, or documentation.

#### Scenario: Canonical names in API responses
- **WHEN** a client requests network or native-asset metadata
- **THEN** the response identifies the native asset as `ARCE` and the network as `ARNE`

#### Scenario: Legacy ticker rejected as canonical
- **WHEN** a client submits `ARC` or `ARN` as the native asset ticker in a canonical identifier
- **THEN** the system rejects the request with an error naming `ARCE` or `ARNE` as the canonical ticker

### Requirement: Namespaced asset and network identifiers

Every asset and network SHALL have an immutable namespaced canonical identifier. The native asset identifier MUST be `arce:<network-id>`. Network identifiers MUST be `arne:<class>/<network-id>` where class is `mainnet`, `testnet`, `devnet`, or `consortium`.

#### Scenario: Valid native asset identifier
- **WHEN** the network ID is `mainnet`
- **THEN** the native asset identifier is `arce:mainnet`

#### Scenario: Valid consortium network identifier
- **WHEN** a consortium network is registered with ID `acme-pay`
- **THEN** its network identifier is `arne:consortium/acme-pay`

#### Scenario: Invalid identifier rejected
- **WHEN** a client submits an identifier with an unknown class, empty segment, uppercase characters, or more than two path segments
- **THEN** the system rejects it with a validation error and does not create or resolve the asset or network

### Requirement: Network identity binds genesis and chain state

Each network SHALL have a unique network ID, chain ID, genesis hash, and genesis timestamp. A node MUST refuse to peer, sync, or accept transactions from a peer whose network ID or genesis hash differs.

#### Scenario: Mismatched genesis rejected
- **WHEN** a node receives a handshake from a peer advertising the same network ID but a different genesis hash
- **THEN** the node rejects the connection with a `genesis_mismatch` reason and records no state from that peer

#### Scenario: Mismatched network rejected
- **WHEN** a peer advertises a different network ID
- **THEN** the node rejects the connection with a `network_mismatch` reason

### Requirement: Protocol version negotiation

Nodes SHALL advertise a semantic protocol version and a supported version range. A node MUST reject peers whose version is outside its supported range and MUST return the supported range in the rejection reason.

#### Scenario: Compatible peer accepted
- **WHEN** two nodes advertise overlapping supported protocol ranges
- **THEN** they establish a session using the highest mutually supported version

#### Scenario: Incompatible peer rejected
- **WHEN** a peer advertises a version below the node's minimum supported version
- **THEN** the node rejects the session and reports its minimum and maximum supported versions

### Requirement: Chain registry is discoverable and verifiable

The system SHALL expose a chain registry listing each known ARNE network with its network ID, chain ID, genesis hash, protocol version, status, and public endpoints. Registry entries MUST be verifiable against the genesis hash returned by the network itself.

#### Scenario: Registry entry matches live network
- **WHEN** a client resolves a registry entry and queries the live network
- **THEN** the network returns the same network ID, chain ID, and genesis hash as the registry entry

#### Scenario: Tampered registry entry detected
- **WHEN** a registry entry's genesis hash differs from the live network's genesis hash
- **THEN** the client treats the entry as untrusted and refuses to transact with it

### Requirement: Human-readable display names never replace canonical identifiers

The system SHALL allow human-readable display names and aliases but MUST resolve all storage, signing, and settlement operations through canonical identifiers.

#### Scenario: Alias resolves to canonical identifier
- **WHEN** a user selects a display alias for an asset
- **THEN** the signed transaction contains the asset's canonical identifier, not the alias

#### Scenario: Duplicate display alias
- **WHEN** two assets share a display alias
- **THEN** the system disambiguates them by canonical identifier and does not merge their balances or metadata
