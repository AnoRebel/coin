// Package disclosure makes the "no universal backdoor" rule enforceable and
// auditable for sealed memos.
//
// A memo can only be opened through a grant. A user grant covers a named set of
// transfers for one viewer and is created by the data owner; an exceptional
// grant requires at least two distinct approvers and a legal-process reference.
// There is no manager-wide or operator-wide key: Open always needs a grant, and
// a grant only reaches the transfers in its scope. Every grant and every open
// or denied open is logged.
package disclosure

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anorebel/anorebelcoin/internal/memo"
)

// Kind distinguishes a user disclosure from a governance-approved one.
type Kind string

const (
	User        Kind = "user"
	Exceptional Kind = "exceptional"
)

var (
	ErrInvalid    = errors.New("disclosure: invalid grant")
	ErrNoGrant    = errors.New("disclosure: no matching grant")
	ErrForbidden  = errors.New("disclosure: actor is not the grant subject")
	ErrNotActive  = errors.New("disclosure: grant is not active yet")
	ErrExpired    = errors.New("disclosure: grant has expired")
	ErrOutOfScope = errors.New("disclosure: transfer is outside the grant scope")
	ErrNoMemo     = errors.New("disclosure: unknown transfer")
	ErrDecrypt    = errors.New("disclosure: cannot decrypt")
)

// Grant authorizes one subject to open a fixed set of sealed memos in a
// half-open time window [From, Until).
type Grant struct {
	ID           string   `json:"id"`
	Kind         Kind     `json:"kind"`
	Subject      string   `json:"subject"`
	Scope        []string `json:"scope"`
	From         int64    `json:"from"`
	Until        int64    `json:"until"`
	Purpose      string   `json:"purpose"`
	Approvers    []string `json:"approvers,omitempty"`
	LegalProcess string   `json:"legal_process,omitempty"`
	GrantedBy    string   `json:"granted_by"`
	GrantedAt    int64    `json:"granted_at"`
}

// Event is one grant, open, or denied-open log record.
type Event struct {
	Seq       uint64   `json:"seq"`
	Time      int64    `json:"time"`
	Actor     string   `json:"actor,omitempty"`
	Grant     string   `json:"grant,omitempty"`
	Kind      Kind     `json:"kind,omitempty"`
	Transfer  string   `json:"transfer,omitempty"`
	Result    string   `json:"result"`
	Reason    string   `json:"reason,omitempty"`
	Scope     []string `json:"scope,omitempty"`
	Purpose   string   `json:"purpose,omitempty"`
	Approvers []string `json:"approvers,omitempty"`
}

// Manager holds sealed memos and grants.
type Manager struct {
	mu     sync.Mutex
	memos  map[string][]byte
	grants map[string]Grant
	events []Event
	seq    uint64
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{memos: map[string][]byte{}, grants: map[string]Grant{}}
}

// AddMemo stores a sealed memo under its transfer hash. It never decrypts.
func (m *Manager) AddMemo(transfer string, sealed []byte) error {
	if strings.TrimSpace(transfer) == "" || len(sealed) == 0 {
		return fmt.Errorf("%w: transfer and sealed memo are required", ErrInvalid)
	}
	if err := memo.Validate(sealed); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.memos[transfer]; dup {
		return fmt.Errorf("%w: transfer %q already registered", ErrInvalid, transfer)
	}
	m.memos[transfer] = append([]byte(nil), sealed...)
	return nil
}

// Grant validates and records a grant. The ID is derived from its fields.
func (m *Manager) Grant(g Grant) (Grant, error) {
	g.Subject = strings.TrimSpace(g.Subject)
	g.Purpose = strings.TrimSpace(g.Purpose)
	g.GrantedBy = strings.TrimSpace(g.GrantedBy)
	g.LegalProcess = strings.TrimSpace(g.LegalProcess)
	g.Scope = normalizeSet(g.Scope)
	g.Approvers = normalizeSet(g.Approvers)
	if err := g.validate(); err != nil {
		return Grant{}, err
	}
	if g.ID == "" {
		g.ID = grantID(g)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.grants[g.ID]; dup {
		return Grant{}, fmt.Errorf("%w: grant %q already exists", ErrInvalid, g.ID)
	}
	m.grants[g.ID] = g
	m.appendLocked(Event{
		Time: g.GrantedAt, Actor: g.GrantedBy, Grant: g.ID, Kind: g.Kind,
		Result: "granted", Scope: append([]string(nil), g.Scope...),
		Purpose: g.Purpose, Approvers: append([]string(nil), g.Approvers...),
	})
	return g, nil
}

// Grant returns a recorded grant.
func (m *Manager) GrantByID(id string) (Grant, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	return g, ok
}

// Open decrypts one memo for the grant subject. Without a grant, or outside its
// scope or window, it fails and records the attempt. The seed must belong to the
// memo recipient; a grant alone never decrypts.
func (m *Manager) Open(actor, grantID, transfer string, seed []byte, at int64) ([]byte, error) {
	m.mu.Lock()
	g, ok := m.grants[grantID]
	sealed, haveMemo := m.memos[transfer]
	m.mu.Unlock()

	if !ok {
		m.deny(actor, grantID, "", transfer, at, "no grant")
		return nil, ErrNoGrant
	}
	if g.Subject != strings.TrimSpace(actor) {
		m.deny(actor, grantID, g.Kind, transfer, at, "not grant subject")
		return nil, ErrForbidden
	}
	if at < g.From {
		m.deny(actor, grantID, g.Kind, transfer, at, "not active yet")
		return nil, ErrNotActive
	}
	if at >= g.Until {
		m.deny(actor, grantID, g.Kind, transfer, at, "expired")
		return nil, ErrExpired
	}
	if !contains(g.Scope, transfer) {
		m.deny(actor, grantID, g.Kind, transfer, at, "outside scope")
		return nil, ErrOutOfScope
	}
	if !haveMemo {
		m.deny(actor, grantID, g.Kind, transfer, at, "unknown transfer")
		return nil, ErrNoMemo
	}
	plaintext, err := memo.Open(seed, sealed)
	if err != nil {
		m.deny(actor, grantID, g.Kind, transfer, at, "decrypt failed")
		return nil, ErrDecrypt
	}
	m.mu.Lock()
	m.appendLocked(Event{
		Time: at, Actor: actor, Grant: g.ID, Kind: g.Kind, Transfer: transfer,
		Result: "opened", Scope: append([]string(nil), g.Scope...),
		Purpose: g.Purpose, Approvers: append([]string(nil), g.Approvers...),
	})
	m.mu.Unlock()
	return plaintext, nil
}

// Events returns log records with Seq > after, up to limit (default 100).
func (m *Manager) Events(after uint64, limit int) []Event {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Event{}
	for _, e := range m.events {
		if e.Seq <= after {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out
}

func (g Grant) validate() error {
	if g.Subject == "" {
		return fmt.Errorf("%w: subject is required", ErrInvalid)
	}
	if g.Purpose == "" {
		return fmt.Errorf("%w: purpose is required", ErrInvalid)
	}
	if len(g.Scope) == 0 || len(g.Scope) > 64 {
		return fmt.Errorf("%w: scope must list 1 to 64 transfers", ErrInvalid)
	}
	if g.From <= 0 || g.Until <= g.From {
		return fmt.Errorf("%w: until must be after a positive from", ErrInvalid)
	}
	if g.GrantedAt <= 0 {
		return fmt.Errorf("%w: granted_at is required", ErrInvalid)
	}
	switch g.Kind {
	case User:
		if len(g.Approvers) != 0 || g.LegalProcess != "" {
			return fmt.Errorf("%w: a user grant takes no approvers or legal process", ErrInvalid)
		}
	case Exceptional:
		if len(g.Approvers) < 2 {
			return fmt.Errorf("%w: exceptional access needs at least two approvers", ErrInvalid)
		}
		if g.LegalProcess == "" {
			return fmt.Errorf("%w: exceptional access needs a legal process reference", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: kind must be user or exceptional", ErrInvalid)
	}
	return nil
}

func (m *Manager) deny(actor, grantID string, kind Kind, transfer string, at int64, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at <= 0 {
		at = now()
	}
	m.appendLocked(Event{
		Time: at, Actor: actor, Grant: grantID, Kind: kind, Transfer: transfer,
		Result: "denied", Reason: reason,
	})
}

func (m *Manager) appendLocked(e Event) {
	m.seq++
	e.Seq = m.seq
	if e.Time == 0 {
		e.Time = now()
	}
	m.events = append(m.events, e)
}

func normalizeSet(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func now() int64 { return time.Now().Unix() }

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func grantID(g Grant) string {
	h := sha256.New()
	_, _ = h.Write([]byte("arne-disclosure-grant-v1"))
	parts := []string{string(g.Kind), strings.TrimSpace(g.Subject), strings.TrimSpace(g.Purpose),
		strconv.FormatInt(g.From, 10), strconv.FormatInt(g.Until, 10)}
	parts = append(parts, normalizeSet(g.Scope)...)
	parts = append(parts, "")
	parts = append(parts, normalizeSet(g.Approvers)...)
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
