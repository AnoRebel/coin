package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrThreshold means too few distinct committee members signed.
	ErrThreshold = errors.New("threshold")
	// ErrSigner means a signer is unknown, blank, or repeated.
	ErrSigner = errors.New("signer")
)

// EmergencyAction is one recorded pause, veto, revocation, or ratification.
type EmergencyAction struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Operations []string `json:"operations,omitempty"`
	Proposal   string   `json:"proposal,omitempty"`
	Reason     string   `json:"reason"`
	Signers    []string `json:"signers,omitempty"`
	At         uint64   `json:"at"`
	ExpiresAt  uint64   `json:"expires_at,omitempty"`
	Status     string   `json:"status"`
	RatifiedBy string   `json:"ratified_by,omitempty"`
}

// EmergencyEvent is one audit-log line. Pause and veto carry the signers and
// reason; expiry, revocation, and ratification carry the original record.
type EmergencyEvent struct {
	Action   string   `json:"action"`
	ID       string   `json:"id"`
	At       uint64   `json:"at"`
	Reason   string   `json:"reason,omitempty"`
	Signers  []string `json:"signers,omitempty"`
	Proposal string   `json:"proposal,omitempty"`
}

// Emergency is a multi-party committee that can pause operations or veto a
// proposal for a bounded period. Every action expires on its own.
type Emergency struct {
	Members   []string             `json:"members"`
	Threshold int                  `json:"threshold"`
	Period    uint64               `json:"period"`
	Actions   []EmergencyAction    `json:"actions,omitempty"`
	Audit     func(EmergencyEvent) `json:"-"`
}

const (
	EmergencyActive   = "active"
	EmergencyExpired  = "expired"
	EmergencyRatified = "ratified"
	EmergencyRevoked  = "revoked"
)

// NewEmergency validates the committee. A multi-party committee needs at
// least two members and a positive bounded period.
func NewEmergency(members []string, threshold int, period uint64) (*Emergency, error) {
	if len(members) < 2 {
		return nil, fmt.Errorf("%w: at least two members are required", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, member := range members {
		if strings.TrimSpace(member) == "" {
			return nil, fmt.Errorf("%w: member is blank", ErrInvalid)
		}
		if seen[member] {
			return nil, fmt.Errorf("%w: duplicate member %s", ErrInvalid, member)
		}
		seen[member] = true
	}
	if threshold < 2 || threshold > len(members) {
		return nil, fmt.Errorf("%w: threshold must be 2..%d", ErrInvalid, len(members))
	}
	if period == 0 {
		return nil, fmt.Errorf("%w: period must be positive", ErrInvalid)
	}
	return &Emergency{Members: members, Threshold: threshold, Period: period}, nil
}

// Pause stops the named operations until the period ends or governance acts.
func (e *Emergency) Pause(signers, operations []string, reason string, now uint64) (EmergencyAction, error) {
	if len(operations) == 0 {
		return EmergencyAction{}, fmt.Errorf("%w: at least one operation is required", ErrInvalid)
	}
	canonical := append([]string(nil), operations...)
	for _, op := range canonical {
		if strings.TrimSpace(op) == "" {
			return EmergencyAction{}, fmt.Errorf("%w: operation is blank", ErrInvalid)
		}
	}
	sort.Strings(canonical)
	for i := 1; i < len(canonical); i++ {
		if canonical[i] == canonical[i-1] {
			return EmergencyAction{}, fmt.Errorf("%w: duplicate operation %s", ErrInvalid, canonical[i])
		}
	}
	return e.record("pause", "", canonical, signers, reason, now)
}

// Veto blocks one proposal until the period ends or governance acts.
func (e *Emergency) Veto(signers []string, proposal, reason string, now uint64) (EmergencyAction, error) {
	if strings.TrimSpace(proposal) == "" {
		return EmergencyAction{}, fmt.Errorf("%w: proposal is required", ErrInvalid)
	}
	return e.record("veto", proposal, nil, signers, reason, now)
}

func (e *Emergency) record(kind, proposal string, operations, signers []string, reason string, now uint64) (EmergencyAction, error) {
	if strings.TrimSpace(reason) == "" {
		return EmergencyAction{}, fmt.Errorf("%w: a public reason is required", ErrInvalid)
	}
	who, err := e.checkSigners(signers)
	if err != nil {
		return EmergencyAction{}, err
	}
	action := EmergencyAction{
		ID:   EmergencyID(kind, proposal, operations, who, reason, now),
		Kind: kind, Operations: operations, Proposal: proposal, Reason: reason,
		Signers: who, At: now, ExpiresAt: now + e.Period, Status: EmergencyActive,
	}
	e.Actions = append(e.Actions, action)
	e.audit(EmergencyEvent{Action: kind, ID: action.ID, At: now, Reason: reason, Signers: who, Proposal: proposal})
	return action, nil
}

func (e *Emergency) checkSigners(signers []string) ([]string, error) {
	if len(signers) < e.Threshold {
		return nil, fmt.Errorf("%w: %d of %d required", ErrThreshold, len(signers), e.Threshold)
	}
	members := map[string]bool{}
	for _, member := range e.Members {
		members[member] = true
	}
	who := append([]string(nil), signers...)
	sort.Strings(who)
	for i, signer := range who {
		if !members[signer] {
			return nil, fmt.Errorf("%w: %s is not on the committee", ErrSigner, signer)
		}
		if i > 0 && who[i-1] == signer {
			return nil, fmt.Errorf("%w: duplicate signer %s", ErrSigner, signer)
		}
	}
	return who, nil
}

// Advance expires actions whose period ended and returns their IDs.
func (e *Emergency) Advance(now uint64) []string {
	expired := []string{}
	for i := range e.Actions {
		action := &e.Actions[i]
		if action.Status == EmergencyActive && now >= action.ExpiresAt {
			action.Status = EmergencyExpired
			expired = append(expired, action.ID)
			e.audit(EmergencyEvent{Action: "expire", ID: action.ID, At: now, Reason: action.Reason, Proposal: action.Proposal})
		}
	}
	sort.Strings(expired)
	return expired
}

// Paused reports whether an operation is paused at now.
func (e *Emergency) Paused(operation string, now uint64) bool {
	for _, action := range e.Actions {
		if action.Kind != "pause" || !e.isActive(action, now) {
			continue
		}
		for _, op := range action.Operations {
			if op == operation || op == "all" {
				return true
			}
		}
	}
	return false
}

// Vetoed reports whether a proposal is vetoed at now.
func (e *Emergency) Vetoed(proposal string, now uint64) bool {
	for _, action := range e.Actions {
		if action.Kind == "veto" && action.Proposal == proposal && e.isActive(action, now) {
			return true
		}
	}
	return false
}

func (e *Emergency) isActive(action EmergencyAction, now uint64) bool {
	return action.Status == EmergencyActive && now < action.ExpiresAt
}

// Revoke ends an active action early; the committee threshold is required.
func (e *Emergency) Revoke(id string, signers []string, reason string, now uint64) error {
	action := e.find(id)
	if action == nil {
		return fmt.Errorf("%w: action %s", ErrUnknown, id)
	}
	if action.Status != EmergencyActive {
		return fmt.Errorf("%w: action is %s", ErrStatus, action.Status)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: a public reason is required", ErrInvalid)
	}
	who, err := e.checkSigners(signers)
	if err != nil {
		return err
	}
	action.Status = EmergencyRevoked
	e.audit(EmergencyEvent{Action: "revoke", ID: id, At: now, Reason: reason, Signers: who, Proposal: action.Proposal})
	return nil
}

// Ratify confirms an action by a governance proposal. A ratified action stops
// being active; its record keeps the proposal that confirmed it.
func (e *Emergency) Ratify(id, proposal string, now uint64) error {
	if strings.TrimSpace(proposal) == "" {
		return fmt.Errorf("%w: a governance proposal is required", ErrInvalid)
	}
	action := e.find(id)
	if action == nil {
		return fmt.Errorf("%w: action %s", ErrUnknown, id)
	}
	if action.Status != EmergencyActive && action.Status != EmergencyExpired {
		return fmt.Errorf("%w: action is %s", ErrStatus, action.Status)
	}
	action.Status = EmergencyRatified
	action.RatifiedBy = proposal
	e.audit(EmergencyEvent{Action: "ratify", ID: id, At: now, Reason: proposal, Proposal: action.Proposal})
	return nil
}

func (e *Emergency) find(id string) *EmergencyAction {
	for i := range e.Actions {
		if e.Actions[i].ID == id {
			return &e.Actions[i]
		}
	}
	return nil
}

func (e *Emergency) audit(event EmergencyEvent) {
	if e.Audit != nil {
		e.Audit(event)
	}
}

// EmergencyID is the first 16 bytes of
// SHA-256("arne-emergency-v1" || 0 || kind || 0 || proposal || 0 ||
// operations || 0 || signers || 0 || reason || 0 || at), hex encoded. The
// operation and signer lists are canonical (sorted) before hashing.
func EmergencyID(kind, proposal string, operations, signers []string, reason string, at uint64) string {
	ops := append([]string(nil), operations...)
	who := append([]string(nil), signers...)
	sort.Strings(ops)
	sort.Strings(who)
	h := sha256.New()
	for _, part := range []string{
		"arne-emergency-v1", kind, proposal,
		strings.Join(ops, ","), strings.Join(who, ","),
		reason, strconv.FormatUint(at, 10),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
