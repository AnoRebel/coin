package p2p

import (
	"errors"
	"fmt"
	"math/bits"
)

var (
	ErrDiversity = errors.New("p2p: peer diversity invalid")
	ErrEclipse   = errors.New("p2p: peer set is concentrated")
	ErrNoQuorum  = errors.New("p2p: not enough voting power")
)

// PeerGroup is a peer and the network range or operator it belongs to.
// Grouping ignores addresses on purpose: what matters is whether one operator
// controls too much of the peer set, not how the address is written.
type PeerGroup struct {
	NodeID string `json:"node_id"`
	Group  string `json:"group"`
}

// Diversity rejects a peer set where one group holds more than MaxShareBPS of
// the connections, which is how an eclipse starts.
type Diversity struct {
	MaxShareBPS int
}

// NewDiversity validates a share between one and 10000 basis points.
func NewDiversity(maxShareBPS int) (*Diversity, error) {
	if maxShareBPS <= 0 || maxShareBPS > 10000 {
		return nil, fmt.Errorf("%w: max share must be 1..10000 basis points", ErrDiversity)
	}
	return &Diversity{MaxShareBPS: maxShareBPS}, nil
}

// Check verifies that no group exceeds the allowed share. Empty node IDs or
// groups, duplicate peers, and an empty set are rejected: a caller that forgot
// to group peers should not silently pass.
func (d *Diversity) Check(peers []PeerGroup) error {
	if len(peers) == 0 {
		return fmt.Errorf("%w: no peers", ErrDiversity)
	}
	seen := make(map[string]bool, len(peers))
	counts := make(map[string]int, len(peers))
	for _, peer := range peers {
		if peer.NodeID == "" || peer.Group == "" {
			return fmt.Errorf("%w: peer %q has no id or group", ErrDiversity, peer.NodeID)
		}
		id, err := normalizeID(peer.NodeID)
		if err != nil {
			return err
		}
		if seen[id] {
			return fmt.Errorf("%w: peer %s repeats", ErrDiversity, id)
		}
		seen[id] = true
		counts[peer.Group]++
	}
	for group, count := range counts {
		if count*10000 > len(peers)*d.MaxShareBPS {
			return fmt.Errorf("%w: group %q holds %d of %d peers", ErrEclipse, group, count, len(peers))
		}
	}
	return nil
}

// Liveness is the current partition view.
type Liveness struct {
	Partitioned bool  `json:"partitioned"`
	PowerBPS    int   `json:"power_bps"`
	Since       int64 `json:"since,omitempty"`
	CanFinalize bool  `json:"can_finalize"`
}

// Partition tracks whether the node can still see a quorum. Finality is
// refused immediately when quorum is lost; a partition is reported only after
// the configured threshold, so a brief blip does not page an operator.
type Partition struct {
	MinPowerBPS int
	Threshold   int64
	since       int64
	partitioned bool
	quorum      bool
	powerBPS    int
}

// NewPartition validates the quorum share and the detection threshold.
func NewPartition(minPowerBPS int, threshold int64) (*Partition, error) {
	if minPowerBPS <= 0 || minPowerBPS > 10000 {
		return nil, fmt.Errorf("%w: quorum must be 1..10000 basis points", ErrDiversity)
	}
	if threshold <= 0 {
		return nil, fmt.Errorf("%w: threshold must be positive", ErrDiversity)
	}
	return &Partition{MinPowerBPS: minPowerBPS, Threshold: threshold}, nil
}

// Observe records the voting power the node can currently reach.
func (p *Partition) Observe(now int64, signedPower, totalPower uint64) (Liveness, error) {
	if now < 0 {
		return Liveness{}, fmt.Errorf("%w: now must not be negative", ErrDiversity)
	}
	if totalPower == 0 || signedPower > totalPower {
		return Liveness{}, fmt.Errorf("%w: signed %d of %d", ErrDiversity, signedPower, totalPower)
	}
	p.powerBPS = int(signedPower * 10000 / totalPower)
	p.quorum = hasQuorum(signedPower, totalPower, p.MinPowerBPS)
	if p.quorum {
		p.since = 0
		p.partitioned = false
		return p.status(), nil
	}
	if p.since == 0 {
		p.since = now
	}
	if now-p.since >= p.Threshold {
		p.partitioned = true
	}
	return p.status(), nil
}

// CheckFinality refuses a block when the observed power is below quorum,
// regardless of what the local peers claim.
func (p *Partition) CheckFinality(signedPower, totalPower uint64) error {
	if totalPower == 0 || signedPower > totalPower {
		return fmt.Errorf("%w: signed %d of %d", ErrDiversity, signedPower, totalPower)
	}
	if !hasQuorum(signedPower, totalPower, p.MinPowerBPS) {
		return fmt.Errorf("%w: %.2f%% of power", ErrNoQuorum, float64(signedPower)*100/float64(totalPower))
	}
	return nil
}

// Liveness returns the last observation without changing it.
func (p *Partition) Liveness() Liveness { return p.status() }

func hasQuorum(signedPower, totalPower uint64, minPowerBPS int) bool {
	signedHi, signedLo := bits.Mul64(signedPower, 10000)
	needHi, needLo := bits.Mul64(totalPower, uint64(minPowerBPS))
	if signedHi != needHi {
		return signedHi > needHi
	}
	return signedLo >= needLo
}

func (p *Partition) status() Liveness {
	return Liveness{
		Partitioned: p.partitioned,
		PowerBPS:    p.powerBPS,
		Since:       p.since,
		CanFinalize: p.quorum,
	}
}
