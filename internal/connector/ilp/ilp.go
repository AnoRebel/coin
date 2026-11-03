// Package ilp implements the ILPv4 and Open Payments pieces an ARNE connector
// needs: address validation, prepare and fulfill packets with a SHA-256
// execution condition, quotes, grants, an HTLC escrow with claim and refund,
// and STREAM receipt accounting.
//
// Times are unix seconds. The wire form of an ILP prepare uses ISO 8601, so an
// adapter converts at the boundary; keeping the model in unix seconds makes the
// expiry and refund comparisons exact and identical in Go and Python.
package ilp

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalid    = errors.New("ilp: invalid")
	ErrAddress    = errors.New("ilp: invalid address")
	ErrExpired    = errors.New("ilp: expired")
	ErrNotExpired = errors.New("ilp: not yet expired")
	ErrCondition  = errors.New("ilp: condition does not match fulfillment")
	ErrState      = errors.New("ilp: wrong state")
	ErrReplay     = errors.New("ilp: replay")
	ErrCode       = errors.New("ilp: invalid reject code")
	ErrAccess     = errors.New("ilp: access denied")
)

// Amount is a canonical asset and a positive base-unit value.
type Amount struct {
	Asset string `json:"asset"`
	Value uint64 `json:"value"`
}

func (a Amount) validate() error {
	if strings.TrimSpace(a.Asset) == "" {
		return fmt.Errorf("%w: asset is required", ErrInvalid)
	}
	if a.Value == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	return nil
}

// ValidateAddress checks an ILP address: at most 1023 bytes, at least two
// dot-separated segments, and every segment 1..64 characters of
// [A-Za-z0-9_~-]. A single trailing dot (the "address prefix" form) is allowed.
func ValidateAddress(address string) error {
	if address == "" || len(address) > 1023 {
		return fmt.Errorf("%w: %q", ErrAddress, address)
	}
	if strings.HasPrefix(address, ".") || strings.Contains(address, "..") {
		return fmt.Errorf("%w: %q", ErrAddress, address)
	}
	trimmed := strings.TrimSuffix(address, ".")
	segments := strings.Split(trimmed, ".")
	if len(segments) < 2 {
		return fmt.Errorf("%w: %q needs two segments", ErrAddress, address)
	}
	for _, segment := range segments {
		if segment == "" || len(segment) > 64 {
			return fmt.Errorf("%w: %q", ErrAddress, address)
		}
		for _, r := range segment {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case r == '-', r == '_', r == '~':
			default:
				return fmt.Errorf("%w: %q", ErrAddress, address)
			}
		}
	}
	return nil
}

// Condition is the SHA-256 execution condition for a 32-byte fulfillment.
func Condition(fulfillment []byte) ([]byte, error) {
	if len(fulfillment) != sha256.Size {
		return nil, fmt.Errorf("%w: fulfillment must be 32 bytes", ErrInvalid)
	}
	sum := sha256.Sum256(fulfillment)
	return sum[:], nil
}

// Prepare is an ILPv4 prepare packet.
type Prepare struct {
	Amount      uint64 `json:"amount"`
	ExpiresAt   int64  `json:"expires_at"`
	Condition   []byte `json:"condition"`
	Destination string `json:"destination"`
	Data        []byte `json:"data,omitempty"`
}

// Validate checks the amount, destination, condition shape, and expiry.
func (p Prepare) Validate(now int64) error {
	if p.Amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if err := ValidateAddress(p.Destination); err != nil {
		return err
	}
	if len(p.Condition) != sha256.Size {
		return fmt.Errorf("%w: condition must be 32 bytes", ErrInvalid)
	}
	if now >= p.ExpiresAt {
		return fmt.Errorf("%w: prepare at %d expired at %d", ErrExpired, now, p.ExpiresAt)
	}
	return nil
}

// Fulfill reveals the preimage. It fails if the prepare expired or the
// fulfillment does not hash to the condition.
func (p Prepare) Fulfill(now int64, fulfillment []byte) ([]byte, error) {
	if err := p.Validate(now); err != nil {
		return nil, err
	}
	if err := matches(p.Condition, fulfillment); err != nil {
		return nil, err
	}
	return append([]byte(nil), fulfillment...), nil
}

// Reject is an ILPv4 reject packet. Codes are three characters starting with F
// (final), T (temporary), or R (relative).
type Reject struct {
	Code        string `json:"code"`
	TriggeredBy string `json:"triggered_by"`
	Message     string `json:"message"`
}

// NewReject validates a reject code and message length.
func NewReject(code, triggeredBy, message string) (Reject, error) {
	if len(code) != 3 || !strings.ContainsRune("FTR", rune(code[0])) {
		return Reject{}, fmt.Errorf("%w: %q", ErrCode, code)
	}
	if utf8.RuneCountInString(message) > 512 {
		return Reject{}, fmt.Errorf("%w: message must be at most 512 characters", ErrInvalid)
	}
	if triggeredBy != "" {
		if err := ValidateAddress(triggeredBy); err != nil {
			return Reject{}, err
		}
	}
	return Reject{Code: code, TriggeredBy: triggeredBy, Message: message}, nil
}

// Quote is an Open Payments quote. Fee may be zero.
type Quote struct {
	ID        string `json:"id"`
	ExpiresAt int64  `json:"expires_at"`
	Debit     Amount `json:"debit"`
	Receive   Amount `json:"receive"`
	Fee       Amount `json:"fee"`
}

// Validate checks the amounts and that the quote has not expired.
func (q Quote) Validate(now int64) error {
	if strings.TrimSpace(q.ID) == "" {
		return fmt.Errorf("%w: quote id is required", ErrInvalid)
	}
	if err := q.Debit.validate(); err != nil {
		return fmt.Errorf("debit: %w", err)
	}
	if err := q.Receive.validate(); err != nil {
		return fmt.Errorf("receive: %w", err)
	}
	if q.Fee.Asset != "" || q.Fee.Value != 0 {
		if err := q.Fee.validate(); err != nil {
			return fmt.Errorf("fee: %w", err)
		}
	}
	if now >= q.ExpiresAt {
		return fmt.Errorf("%w: quote at %d expired at %d", ErrExpired, now, q.ExpiresAt)
	}
	return nil
}

// Access is one Open Payments access right.
type Access struct {
	Type    string   `json:"type"`
	Actions []string `json:"actions"`
	Limits  []Amount `json:"limits,omitempty"`
}

var accessTypes = map[string]bool{
	"incoming-payment": true,
	"quote":            true,
	"outgoing-payment": true,
}

var accessActions = map[string]bool{
	"create": true, "read": true, "list": true, "complete": true, "delete": true,
}

// Grant is an Open Payments access token grant.
type Grant struct {
	AccessToken string   `json:"access_token"`
	Access      []Access `json:"access"`
	ExpiresAt   int64    `json:"expires_at"`
}

// Validate checks the token, access rights, limits, and expiry.
func (g Grant) Validate(now int64) error {
	if strings.TrimSpace(g.AccessToken) == "" {
		return fmt.Errorf("%w: access token is required", ErrInvalid)
	}
	if len(g.Access) == 0 {
		return fmt.Errorf("%w: grant needs an access right", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, access := range g.Access {
		if !accessTypes[access.Type] {
			return fmt.Errorf("%w: access type %q", ErrInvalid, access.Type)
		}
		if seen[access.Type] {
			return fmt.Errorf("%w: duplicate access type %q", ErrInvalid, access.Type)
		}
		seen[access.Type] = true
		if len(access.Actions) == 0 {
			return fmt.Errorf("%w: %s needs an action", ErrInvalid, access.Type)
		}
		for _, action := range access.Actions {
			if !accessActions[action] {
				return fmt.Errorf("%w: action %q", ErrInvalid, action)
			}
		}
		for _, limit := range access.Limits {
			if err := limit.validate(); err != nil {
				return fmt.Errorf("limit: %w", err)
			}
		}
	}
	if now >= g.ExpiresAt {
		return fmt.Errorf("%w: grant at %d expired at %d", ErrExpired, now, g.ExpiresAt)
	}
	return nil
}

// Check reports whether the grant allows an action on an access type at now.
func (g Grant) Check(now int64, kind, action string) error {
	if err := g.Validate(now); err != nil {
		return err
	}
	for _, access := range g.Access {
		if access.Type != kind {
			continue
		}
		for _, allowed := range access.Actions {
			if allowed == action {
				return nil
			}
		}
		return fmt.Errorf("%w: %s on %s", ErrAccess, action, kind)
	}
	return fmt.Errorf("%w: no %s access", ErrAccess, kind)
}

// Status is an escrow's life cycle.
type Status string

const (
	Held     Status = "held"
	Claimed  Status = "claimed"
	Refunded Status = "refunded"
)

// Escrow is an HTLC between two accounts. The receiver claims with the
// preimage before ExpiresAt; the sender refunds only at or after it.
type Escrow struct {
	ID          string `json:"id"`
	Reference   string `json:"reference,omitempty"`
	Sender      string `json:"sender"`
	Receiver    string `json:"receiver"`
	Amount      Amount `json:"amount"`
	Condition   []byte `json:"condition"`
	ExpiresAt   int64  `json:"expires_at"`
	Status      Status `json:"status"`
	Fulfillment []byte `json:"fulfillment,omitempty"`
}

// NewEscrow validates and opens a held escrow. Condition must be the SHA-256 of
// the fulfillment the receiver will reveal.
func NewEscrow(id string, sender, receiver string, amount Amount, condition []byte, expiresAt int64) (*Escrow, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	if err := account(sender); err != nil {
		return nil, fmt.Errorf("sender: %w", err)
	}
	if err := account(receiver); err != nil {
		return nil, fmt.Errorf("receiver: %w", err)
	}
	if sender == receiver {
		return nil, fmt.Errorf("%w: sender and receiver must differ", ErrInvalid)
	}
	if err := amount.validate(); err != nil {
		return nil, err
	}
	if len(condition) != sha256.Size {
		return nil, fmt.Errorf("%w: condition must be 32 bytes", ErrInvalid)
	}
	return &Escrow{
		ID: id, Sender: sender, Receiver: receiver, Amount: amount,
		Condition: append([]byte(nil), condition...), ExpiresAt: expiresAt, Status: Held,
	}, nil
}

// Claim settles the escrow to the receiver. It fails after expiry, on a wrong
// preimage, or if the escrow is no longer held.
func (e *Escrow) Claim(now int64, fulfillment []byte) error {
	if e.Status != Held {
		return fmt.Errorf("%w: escrow is %s", ErrState, e.Status)
	}
	if now >= e.ExpiresAt {
		return fmt.Errorf("%w: claim at %d expired at %d", ErrExpired, now, e.ExpiresAt)
	}
	if err := matches(e.Condition, fulfillment); err != nil {
		return err
	}
	e.Status = Claimed
	e.Fulfillment = append([]byte(nil), fulfillment...)
	return nil
}

// Refund returns the escrow to the sender. It is only allowed at or after
// ExpiresAt, so a claim that arrives in time always wins.
func (e *Escrow) Refund(now int64) error {
	if e.Status != Held {
		return fmt.Errorf("%w: escrow is %s", ErrState, e.Status)
	}
	if now < e.ExpiresAt {
		return fmt.Errorf("%w: refund at %d expires at %d", ErrNotExpired, now, e.ExpiresAt)
	}
	e.Status = Refunded
	return nil
}

func account(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: account is required", ErrInvalid)
	}
	if n := utf8.RuneCountInString(value); n > 128 {
		return fmt.Errorf("%w: account must be at most 128 characters", ErrInvalid)
	}
	return nil
}

func matches(condition, fulfillment []byte) error {
	if len(condition) != sha256.Size || len(fulfillment) != sha256.Size {
		return ErrCondition
	}
	sum := sha256.Sum256(fulfillment)
	if !bytes.Equal(sum[:], condition) {
		return ErrCondition
	}
	return nil
}

// Receipt is a STREAM receipt: the cumulative total received on a stream up to
// a sequence number.
type Receipt struct {
	StreamID      string `json:"stream_id"`
	Sequence      uint64 `json:"sequence"`
	TotalReceived uint64 `json:"total_received"`
}

// Stream tracks receipts for one stream. A receipt must advance the sequence
// and never reduce the total, so a replayed or reordered receipt is refused.
type Stream struct {
	id       string
	sequence uint64
	total    uint64
}

// NewStream starts a stream. The first receipt must have sequence >= 1.
func NewStream(id string) (*Stream, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: stream id is required", ErrInvalid)
	}
	return &Stream{id: id}, nil
}

// Apply accepts a receipt. It fails on a foreign stream, a non-advancing
// sequence (replay or reorder), or a total that decreased.
func (s *Stream) Apply(receipt Receipt) error {
	if receipt.StreamID != s.id {
		return fmt.Errorf("%w: stream %q", ErrInvalid, receipt.StreamID)
	}
	if receipt.Sequence <= s.sequence {
		return fmt.Errorf("%w: sequence %d after %d", ErrReplay, receipt.Sequence, s.sequence)
	}
	if receipt.TotalReceived < s.total {
		return fmt.Errorf("%w: total %d below %d", ErrInvalid, receipt.TotalReceived, s.total)
	}
	s.sequence = receipt.Sequence
	s.total = receipt.TotalReceived
	return nil
}

// Total is the last accepted cumulative total.
func (s *Stream) Total() uint64 { return s.total }

// Sequence is the last accepted sequence.
func (s *Stream) Sequence() uint64 { return s.sequence }
