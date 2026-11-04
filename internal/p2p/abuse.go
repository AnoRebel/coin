// Package p2p peer scoring and bans.
//
// The relay queue decides what a message is; the guard decides whether a peer
// may keep sending at all. It rate-limits each peer, penalizes invalid,
// oversized, and throttled traffic, and bans a peer whose score reaches zero
// until the ban expires. Scores and rate windows are in memory: a restart
// forgets them, which is acceptable because a reconnect still has to pass the
// handshake and the connection caps.
package p2p

import (
	"errors"
	"fmt"
	"sort"
)

var (
	ErrAbuse     = errors.New("p2p: abuse policy invalid")
	ErrThrottled = errors.New("p2p: message rate exceeded")
	ErrBanned    = errors.New("p2p: peer is banned")
)

// Outcome is what the node decided about a peer's message.
type Outcome string

const (
	OutcomeValid     Outcome = "valid"
	OutcomeInvalid   Outcome = "invalid"
	OutcomeOversized Outcome = "oversized"
	OutcomeThrottled Outcome = "throttled"
)

// AbusePolicy sets the rate window and how fast a peer loses its score.
type AbusePolicy struct {
	RateCount        int   `json:"rate_count"`
	RateWindow       int64 `json:"rate_window"`
	InitialScore     int   `json:"initial_score"`
	InvalidPenalty   int   `json:"invalid_penalty"`
	OversizedPenalty int   `json:"oversized_penalty"`
	ThrottlePenalty  int   `json:"throttle_penalty"`
	BanDuration      int64 `json:"ban_duration"`
}

// Validate rejects a policy that could not actually limit abuse.
func (p AbusePolicy) Validate() error {
	if p.RateCount <= 0 || p.RateWindow <= 0 {
		return fmt.Errorf("%w: rate count and window must be positive", ErrAbuse)
	}
	if p.InitialScore <= 0 {
		return fmt.Errorf("%w: initial score must be positive", ErrAbuse)
	}
	if p.InvalidPenalty <= 0 || p.OversizedPenalty <= 0 || p.ThrottlePenalty <= 0 {
		return fmt.Errorf("%w: penalties must be positive", ErrAbuse)
	}
	if p.BanDuration <= 0 {
		return fmt.Errorf("%w: ban duration must be positive", ErrAbuse)
	}
	return nil
}

// View is a peer's current standing.
type View struct {
	NodeID      string `json:"node_id"`
	Score       int    `json:"score"`
	Messages    int    `json:"messages"`
	BannedUntil int64  `json:"banned_until,omitempty"`
	Banned      bool   `json:"banned"`
}

type peerState struct {
	score    int
	window   []int64
	bannedAt int64
}

// Guard tracks scores, rate windows, and bans. It is not safe for concurrent
// use; the caller serializes peer events.
type Guard struct {
	policy AbusePolicy
	peers  map[string]*peerState
}

// NewGuard validates the policy and starts an empty guard.
func NewGuard(policy AbusePolicy) (*Guard, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Guard{policy: policy, peers: map[string]*peerState{}}, nil
}

// Admit refuses a banned peer. An expired ban is cleared and the peer starts
// again with its initial score and an empty rate window.
func (g *Guard) Admit(nodeID string, now int64) error {
	state, err := g.state(nodeID, now)
	if err != nil {
		return err
	}
	if state.bannedAt != 0 {
		return fmt.Errorf("%w until %d", ErrBanned, state.bannedAt)
	}
	return nil
}

// Allow counts one message against the peer's rate window. A peer at its limit
// is throttled, loses score, and is refused; the refused message does not enter
// the window, so a peer that backs off is not punished twice for the same
// burst.
func (g *Guard) Allow(nodeID string, now int64) error {
	state, err := g.state(nodeID, now)
	if err != nil {
		return err
	}
	if state.bannedAt != 0 {
		return fmt.Errorf("%w until %d", ErrBanned, state.bannedAt)
	}
	state.window = pruneTimes(state.window, now-g.policy.RateWindow)
	if len(state.window) >= g.policy.RateCount {
		g.penalize(state, OutcomeThrottled, now)
		return fmt.Errorf("%w: %d per %ds", ErrThrottled, g.policy.RateCount, g.policy.RateWindow)
	}
	state.window = append(state.window, now)
	return nil
}

// Record applies a message outcome. Valid traffic slowly recovers score;
// invalid, oversized, and throttled traffic subtracts it. A score at or below
// zero bans the peer for the configured duration. Recording after a ban is a
// no-op until the ban expires.
func (g *Guard) Record(nodeID string, outcome Outcome, now int64) (View, error) {
	state, err := g.state(nodeID, now)
	if err != nil {
		return View{}, err
	}
	if state.bannedAt != 0 {
		return g.view(nodeID, state, now), nil
	}
	switch outcome {
	case OutcomeValid:
		if state.score < g.policy.InitialScore {
			state.score++
		}
	case OutcomeInvalid, OutcomeOversized, OutcomeThrottled:
		g.penalize(state, outcome, now)
	default:
		return View{}, fmt.Errorf("%w: unknown outcome %q", ErrAbuse, outcome)
	}
	return g.view(nodeID, state, now), nil
}

// View reports a peer's score and ban without changing either.
func (g *Guard) View(nodeID string, now int64) (View, error) {
	state, err := g.state(nodeID, now)
	if err != nil {
		return View{}, err
	}
	return g.view(nodeID, state, now), nil
}

// Peers lists the known peers in stable order.
func (g *Guard) Peers() []string {
	ids := make([]string, 0, len(g.peers))
	for id := range g.peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (g *Guard) state(nodeID string, now int64) (*peerState, error) {
	if now < 0 {
		return nil, fmt.Errorf("%w: now must not be negative", ErrAbuse)
	}
	id, err := normalizeID(nodeID)
	if err != nil {
		return nil, err
	}
	state, ok := g.peers[id]
	if !ok {
		state = &peerState{score: g.policy.InitialScore}
		g.peers[id] = state
	}
	if state.bannedAt != 0 && now >= state.bannedAt {
		state.bannedAt = 0
		state.score = g.policy.InitialScore
		state.window = nil
	}
	return state, nil
}

func (g *Guard) penalize(state *peerState, outcome Outcome, now int64) {
	penalty := g.policy.InvalidPenalty
	switch outcome {
	case OutcomeOversized:
		penalty = g.policy.OversizedPenalty
	case OutcomeThrottled:
		penalty = g.policy.ThrottlePenalty
	}
	state.score -= penalty
	if state.score <= 0 {
		state.score = 0
		state.bannedAt = now + g.policy.BanDuration
	}
}

func (g *Guard) view(nodeID string, state *peerState, now int64) View {
	banned := state.bannedAt != 0 && now < state.bannedAt
	view := View{
		NodeID:   nodeID,
		Score:    state.score,
		Messages: len(state.window),
		Banned:   banned,
	}
	if banned {
		view.BannedUntil = state.bannedAt
	}
	return view
}

func pruneTimes(values []int64, after int64) []int64 {
	kept := values[:0]
	for _, at := range values {
		if at > after {
			kept = append(kept, at)
		}
	}
	return kept
}
