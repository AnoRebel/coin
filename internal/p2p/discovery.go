package p2p

import (
	"errors"
	"fmt"
	"sort"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// ErrFull is returned when a peer would exceed the connection cap.
var ErrFull = errors.New("p2p: peer limit reached")

// Discovery states.
const (
	StateJoined   = "joined"
	StateDegraded = "degraded"
)

// Status is the current view of discovery.
type Status struct {
	State   string `json:"state"`
	Peers   int    `json:"peers"`
	RetryAt int64  `json:"retry_at,omitempty"`
}

// Discovery tracks bootstrap connectivity and backoff. It performs no I/O: the
// caller reports which bootstrap peers were reachable, and Discovery decides
// whether the node may join gossip, which peers it may hold, and when to retry.
type Discovery struct {
	policy     Config
	minPeers   int
	backoff    int64
	maxBackoff int64
	peers      map[string]bool
	delay      int64
	nextRetry  int64
}

// NewDiscovery validates the policy and sets the first retry window.
func NewDiscovery(policy Config, minPeers int, backoff, maxBackoff int64) (*Discovery, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if minPeers < 1 {
		return nil, fmt.Errorf("%w: min peers must be positive", ErrInvalid)
	}
	if minPeers > policy.MaxInbound+policy.MaxOutbound {
		return nil, fmt.Errorf("%w: min peers exceeds the connection limits", ErrInvalid)
	}
	if backoff <= 0 || maxBackoff < backoff {
		return nil, fmt.Errorf("%w: backoff must be positive and not exceed max_backoff", ErrInvalid)
	}
	return &Discovery{
		policy:     policy,
		minPeers:   minPeers,
		backoff:    backoff,
		maxBackoff: maxBackoff,
		peers:      map[string]bool{},
		delay:      backoff,
	}, nil
}

// Observe records which bootstrap peers answered at `now`. A consortium node
// ignores a reachable peer that is not allowlisted, even when it speaks the
// protocol. Below the minimum peer count the node is degraded and a retry is
// scheduled with capped exponential backoff; reaching the minimum resets it.
func (d *Discovery) Observe(now int64, reachable []string) (Status, error) {
	if now < 0 {
		return Status{}, fmt.Errorf("%w: now must not be negative", ErrInvalid)
	}
	if now < d.nextRetry {
		return d.status(), nil
	}
	for _, raw := range reachable {
		id, err := normalizeID(raw)
		if err != nil {
			return Status{}, err
		}
		if len(d.peers) >= d.capacity() && !d.peers[id] {
			continue
		}
		if !d.allowed(id) {
			continue
		}
		d.peers[id] = true
	}
	if len(d.peers) >= d.minPeers {
		d.delay = d.backoff
		d.nextRetry = 0
		return d.status(), nil
	}
	d.nextRetry = now + d.delay
	d.delay = min(d.delay*2, d.maxBackoff)
	return d.status(), nil
}

// Admit applies the peer policy and connection cap, then reports the session.
func (d *Discovery) Admit(offer Offer) (Session, error) {
	session, err := d.policy.Handshake(offer)
	if err != nil {
		return Session{}, err
	}
	if !d.peers[session.NodeID] && len(d.peers) >= d.capacity() {
		return Session{}, fmt.Errorf("%w: %d connections", ErrFull, d.capacity())
	}
	d.peers[session.NodeID] = true
	if len(d.peers) >= d.minPeers {
		d.delay = d.backoff
		d.nextRetry = 0
	}
	return session, nil
}

// Disconnect removes a peer; the next Observe reports degraded again.
func (d *Discovery) Disconnect(nodeID string) {
	id, err := normalizeID(nodeID)
	if err != nil {
		return
	}
	delete(d.peers, id)
}

// Peers returns the connected peer IDs, sorted for stable output.
func (d *Discovery) Peers() []string {
	ids := make([]string, 0, len(d.peers))
	for id := range d.peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// RetryAt is the earliest time Observe will attempt again, or 0 when joined.
func (d *Discovery) RetryAt() int64 { return d.nextRetry }

func (d *Discovery) status() Status {
	state := StateDegraded
	if len(d.peers) >= d.minPeers {
		state = StateJoined
	}
	return Status{State: state, Peers: len(d.peers), RetryAt: d.nextRetry}
}

func (d *Discovery) capacity() int { return d.policy.MaxInbound + d.policy.MaxOutbound }

func (d *Discovery) allowed(id string) bool {
	if d.policy.Class == protocol.ClassConsortium {
		return contains(d.policy.Allowlist, id)
	}
	return true
}
