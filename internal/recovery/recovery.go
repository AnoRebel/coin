// Package recovery implements guardian-based social recovery with a threshold
// and a timelock.
//
// A policy names the account owner, two or more guardians, a threshold of at
// least two, and a delay. A recovery request collects signed guardian
// approvals. Once the threshold is met the request is ready, but it can only
// execute after the delay. The owner can cancel a pending or ready request
// before it executes. A single guardian can never execute alone.
//
// The package is a state machine over signed messages. It does not change an
// account key in the ledger; wallet and node code must apply the executed new
// key.
package recovery

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

const (
	StatePending   = "pending"
	StateReady     = "ready"
	StateExecuted  = "executed"
	StateCancelled = "cancelled"
)

var (
	ErrInvalid   = errors.New("recovery: invalid policy or request")
	ErrGuardian  = errors.New("recovery: not a guardian")
	ErrThreshold = errors.New("recovery: threshold not met")
	ErrTimelock  = errors.New("recovery: timelock not elapsed")
	ErrSignature = errors.New("recovery: bad signature")
	ErrDuplicate = errors.New("recovery: duplicate approval")
	ErrState     = errors.New("recovery: wrong state")
	ErrUnknown   = errors.New("recovery: unknown request")
)

// Guardian is a named Ed25519 public key.
type Guardian struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
}

// Policy is the immutable recovery configuration for one account.
type Policy struct {
	Account        string     `json:"account"`
	OwnerPublicKey string     `json:"owner_public_key"`
	Guardians      []Guardian `json:"guardians"`
	Threshold      int        `json:"threshold"`
	Delay          uint64     `json:"delay"`
}

// Validate checks that the policy cannot be captured by one guardian.
func (p Policy) Validate() error {
	if !validLabel(p.Account) || !validHash(p.OwnerPublicKey) {
		return fmt.Errorf("%w: account or owner key", ErrInvalid)
	}
	if p.Threshold < 2 {
		return fmt.Errorf("%w: threshold must be at least 2", ErrInvalid)
	}
	if p.Delay == 0 {
		return fmt.Errorf("%w: delay must be positive", ErrInvalid)
	}
	if len(p.Guardians) < p.Threshold {
		return fmt.Errorf("%w: fewer guardians than the threshold", ErrInvalid)
	}
	ids := map[string]bool{}
	keys := map[string]bool{}
	for _, guardian := range p.Guardians {
		if !validLabel(guardian.ID) || !validHash(guardian.PublicKey) {
			return fmt.Errorf("%w: guardian", ErrInvalid)
		}
		if ids[guardian.ID] {
			return fmt.Errorf("%w: duplicate guardian id", ErrInvalid)
		}
		if keys[guardian.PublicKey] {
			return fmt.Errorf("%w: duplicate guardian key", ErrInvalid)
		}
		if guardian.PublicKey == p.OwnerPublicKey {
			return fmt.Errorf("%w: owner cannot be a guardian", ErrInvalid)
		}
		ids[guardian.ID] = true
		keys[guardian.PublicKey] = true
	}
	return nil
}

// Approval is one guardian signature over a recovery request.
type Approval struct {
	Guardian  string `json:"guardian"`
	Signature string `json:"signature"`
}

// Request is one recovery attempt.
type Request struct {
	ID           string     `json:"id"`
	Account      string     `json:"account"`
	NewPublicKey string     `json:"new_public_key"`
	CreatedAt    uint64     `json:"created_at"`
	ExecutableAt uint64     `json:"executable_at"`
	State        string     `json:"state"`
	Approvals    []Approval `json:"approvals"`
}

// RequestID derives the stable ID for an account, new key, and nonce.
func RequestID(account, newKey, nonce string) string {
	h := sha256.New()
	h.Write([]byte("arne-recovery-v1\x00"))
	for _, part := range []string{account, newKey, nonce} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(part)))
		h.Write(size[:])
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// ApprovalPayload is the message a guardian signs.
func ApprovalPayload(id, account, newKey string) []byte {
	return []byte("arne-recovery-approval-v1\n" + id + "\n" + account + "\n" + newKey)
}

// CancelPayload is the message the owner signs to abort a request.
func CancelPayload(id, account string) []byte {
	return []byte("arne-recovery-cancel-v1\n" + id + "\n" + account)
}

// Manager holds a policy and its recovery requests.
type Manager struct {
	policy   Policy
	requests map[string]*Request
	order    []string
}

// New validates a policy and creates an empty manager.
func New(policy Policy) (*Manager, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Manager{policy: policy, requests: map[string]*Request{}}, nil
}

// Policy returns the manager's policy.
func (m *Manager) Policy() Policy {
	return m.policy
}

// Start creates a pending request. Repeating the same account, key, and nonce
// returns the existing request instead of creating another.
func (m *Manager) Start(newKey, nonce string, now uint64) (Request, error) {
	if !validHash(newKey) || !validLabel(nonce) {
		return Request{}, fmt.Errorf("%w: new key or nonce", ErrInvalid)
	}
	id := RequestID(m.policy.Account, newKey, nonce)
	if existing, ok := m.requests[id]; ok {
		return *existing, nil
	}
	request := &Request{
		ID: id, Account: m.policy.Account, NewPublicKey: newKey,
		CreatedAt: now, ExecutableAt: now + m.policy.Delay, State: StatePending,
	}
	m.requests[id] = request
	m.order = append(m.order, id)
	return *request, nil
}

// Approve verifies and records one guardian signature. Reaching the threshold
// moves the request to ready.
func (m *Manager) Approve(id, guardianID, signature string) (Request, error) {
	request, ok := m.requests[id]
	if !ok {
		return Request{}, ErrUnknown
	}
	if request.State != StatePending && request.State != StateReady {
		return Request{}, fmt.Errorf("%w: %s", ErrState, request.State)
	}
	guardian, ok := m.guardian(guardianID)
	if !ok {
		return Request{}, ErrGuardian
	}
	for _, approval := range request.Approvals {
		if approval.Guardian == guardianID {
			return *request, ErrDuplicate
		}
	}
	pub, _ := hex.DecodeString(guardian.PublicKey)
	sig, err := hex.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize ||
		!ed25519.Verify(pub, ApprovalPayload(id, request.Account, request.NewPublicKey), sig) {
		return Request{}, ErrSignature
	}
	request.Approvals = append(request.Approvals, Approval{Guardian: guardianID, Signature: signature})
	sort.Slice(request.Approvals, func(i, j int) bool {
		return request.Approvals[i].Guardian < request.Approvals[j].Guardian
	})
	if len(request.Approvals) >= m.policy.Threshold {
		request.State = StateReady
	}
	return *request, nil
}

// Execute applies a ready request once the timelock has elapsed.
func (m *Manager) Execute(id string, now uint64) (Request, error) {
	request, ok := m.requests[id]
	if !ok {
		return Request{}, ErrUnknown
	}
	if request.State != StateReady {
		if request.State == StatePending {
			return Request{}, ErrThreshold
		}
		return Request{}, fmt.Errorf("%w: %s", ErrState, request.State)
	}
	if now < request.ExecutableAt {
		return Request{}, ErrTimelock
	}
	request.State = StateExecuted
	return *request, nil
}

// Cancel aborts a request with the owner's signature before it executes.
func (m *Manager) Cancel(id, ownerSignature string, now uint64) (Request, error) {
	request, ok := m.requests[id]
	if !ok {
		return Request{}, ErrUnknown
	}
	if request.State != StatePending && request.State != StateReady {
		return Request{}, fmt.Errorf("%w: %s", ErrState, request.State)
	}
	pub, _ := hex.DecodeString(m.policy.OwnerPublicKey)
	sig, err := hex.DecodeString(ownerSignature)
	if err != nil || len(sig) != ed25519.SignatureSize ||
		!ed25519.Verify(pub, CancelPayload(id, request.Account), sig) {
		return Request{}, ErrSignature
	}
	request.State = StateCancelled
	return *request, nil
}

// Get returns a request by ID.
func (m *Manager) Get(id string) (Request, bool) {
	request, ok := m.requests[id]
	if !ok {
		return Request{}, false
	}
	return *request, true
}

// Requests lists requests in creation order.
func (m *Manager) Requests() []Request {
	out := make([]Request, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, *m.requests[id])
	}
	return out
}

func (m *Manager) guardian(id string) (Guardian, bool) {
	for _, guardian := range m.policy.Guardians {
		if guardian.ID == id {
			return guardian, true
		}
	}
	return Guardian{}, false
}

func validLabel(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}
