# Spec Delta

## Purpose

Defines the peer-to-peer layer that lets ARNE nodes discover each other, exchange transactions and blocks, synchronize state, resist abuse, and operate across public and consortium network topologies.

## ADDED Requirements

### Requirement: Peers authenticate and encrypt all transport

Every peer connection SHALL be authenticated and encrypted. A node MUST verify the peer's network identity and protocol version before exchanging consensus or transaction messages. Unauthenticated or unencrypted sessions MUST NOT carry application traffic.

#### Scenario: Authenticated session established
- **WHEN** two compatible nodes connect
- **THEN** they complete an authenticated, encrypted handshake and negotiate a protocol version

#### Scenario: Unauthenticated peer rejected
- **WHEN** a peer cannot prove possession of its advertised identity key
- **THEN** the connection is closed before any application message is processed

#### Scenario: Wire encryption
- **WHEN** traffic is captured on the network
- **THEN** message contents are not readable without the session keys

### Requirement: Peer discovery supports public and consortium topologies

Public networks SHALL support bootstrap peers, peer exchange, and a distributed hash table. Consortium networks SHALL support an explicit allowlist of member peer identities and MAY disable open discovery. A node MUST NOT connect to a peer outside its network's allowed topology.

#### Scenario: Public peer discovery
- **WHEN** a new public node starts with bootstrap peers
- **THEN** it discovers additional compatible peers and joins gossip

#### Scenario: Consortium allowlist enforced
- **WHEN** an unknown peer attempts to join a consortium network
- **THEN** the connection is rejected even if the peer speaks the protocol

#### Scenario: Bootstrap unavailable
- **WHEN** all bootstrap peers are unreachable
- **THEN** the node retries with backoff and reports degraded discovery without crashing

### Requirement: Transaction and block gossip is efficient and deduplicated

Nodes SHALL propagate transactions and blocks using a gossip protocol with message identifiers, deduplication, and size limits. A node MUST NOT relay a message it has already seen, and MUST prioritize block propagation over transaction propagation.

#### Scenario: Transaction reaches peers
- **WHEN** a valid transaction is submitted to one node
- **THEN** it propagates to connected peers and is deduplicated at each hop

#### Scenario: Block prioritized
- **WHEN** a new block and a new transaction are ready to gossip
- **THEN** the block is propagated first

#### Scenario: Duplicate suppressed
- **WHEN** a node receives a message identifier it has already processed
- **THEN** it drops the duplicate without re-relaying it

### Requirement: State sync verifies everything it imports

A node SHALL support snapshot-based state sync and headers-first block sync. Imported state MUST be verified against a trusted state root and validator commit before it is used, and a failed verification MUST discard the snapshot.

#### Scenario: Snapshot sync
- **WHEN** a new node syncs from a snapshot at a finalized height
- **THEN** it verifies the state root and commit certificate before accepting the snapshot

#### Scenario: Invalid snapshot discarded
- **WHEN** a snapshot's state root does not match the committed root
- **THEN** the node discards it and falls back to block sync

#### Scenario: Headers-first catch-up
- **WHEN** a node is far behind
- **THEN** it downloads and verifies headers before requesting block bodies

### Requirement: Abuse is rate-limited, scored, and ban-worthy

The network layer SHALL enforce per-peer connection limits, message-rate limits, message-size limits, and a peer score. A peer that repeatedly sends invalid, oversized, or abusive messages MUST be throttled and eventually banned.

#### Scenario: Message flood throttled
- **WHEN** a peer exceeds its message-rate limit
- **THEN** the node throttles or disconnects it

#### Scenario: Invalid message penalized
- **WHEN** a peer sends a malformed or invalid consensus message
- **THEN** its score decreases and repeated violations lead to a ban

#### Scenario: Ban expiry
- **WHEN** a ban period ends
- **THEN** the peer may reconnect subject to normal admission checks

### Requirement: NAT traversal and connectivity are configurable

The node SHALL support outbound connections, inbound listeners, hole punching, and relay circuits. Operators MUST be able to configure listen addresses, advertised addresses, maximum peers, and relay usage.

#### Scenario: Node behind NAT
- **WHEN** a node cannot accept inbound connections
- **THEN** it can still connect outbound and participate through relay or hole punching

#### Scenario: Advertised address mismatch
- **WHEN** the advertised address is not reachable
- **THEN** the node reports the mismatch and continues with outbound connectivity

### Requirement: Eclipse and partition attacks are mitigated

Nodes SHALL maintain a diverse peer set across outbound, inbound, and anchor connections, and MUST detect when they are isolated from the expected network. A node MUST NOT finalize blocks without a quorum even if its local peers claim a chain.

#### Scenario: Peer diversity
- **WHEN** a node selects peers
- **THEN** it avoids concentrating all connections in one network range or operator

#### Scenario: Partition detected
- **WHEN** a node cannot reach a quorum of voting power for longer than the configured threshold
- **THEN** it reports a partition and stops finalizing new blocks

#### Scenario: Anchor peer recovery
- **WHEN** a node loses most peers
- **THEN** it reconnects through configured anchor peers and re-evaluates the chain

### Requirement: Wire protocol is versioned and cross-implementation compatible

The peer wire protocol SHALL use versioned, canonical message encoding. The Go and Python nodes MUST interoperate on the same network and MUST reject messages from unsupported protocol versions with a clear reason.

#### Scenario: Cross-implementation handshake
- **WHEN** a Go node and a Python node connect
- **THEN** they complete the handshake and exchange valid messages

#### Scenario: Unsupported version rejected
- **WHEN** a peer advertises an unsupported wire version
- **THEN** the connection is rejected with the supported range
