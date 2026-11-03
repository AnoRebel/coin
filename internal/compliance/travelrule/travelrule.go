// Package travelrule collects and exchanges originator and beneficiary data
// for transfers above a threshold.
//
// The data is IVMS101-shaped but never touches the ledger: this package only
// validates it, signs an envelope, and hands that envelope to a caller-supplied
// authenticated transport. A failed exchange means the counterparty could not
// be reached; the caller holds or rejects the transfer according to its policy.
package travelrule

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Version is the envelope and data shape this package speaks.
const Version = "ivms101"

const signatureDomain = "arne-travelrule-v1"

var (
	ErrInvalid     = errors.New("travelrule: invalid")
	ErrUnavailable = errors.New("travelrule: counterparty unavailable")
	ErrSignature   = errors.New("travelrule: bad signature")
)

// Party is one side of the transfer. Country is ISO 3166-1 alpha-2 when set.
type Party struct {
	Name    string `json:"name"`
	Account string `json:"account"`
	Address string `json:"address,omitempty"`
	Country string `json:"country,omitempty"`
}

// VASP identifies a virtual asset service provider. ID is a LEI or the
// provider's registered identifier.
type VASP struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Transfer is the data exchanged for a qualifying transfer.
type Transfer struct {
	Network         string `json:"network"`
	Asset           string `json:"asset"`
	Amount          uint64 `json:"amount"`
	Reference       string `json:"reference"`
	Originator      Party  `json:"originator"`
	Beneficiary     Party  `json:"beneficiary"`
	OriginatingVASP VASP   `json:"originating_vasp"`
	BeneficiaryVASP VASP   `json:"beneficiary_vasp"`
}

// Policy is the deployer's threshold in the asset's base units.
type Policy struct {
	Threshold uint64 `json:"threshold"`
}

// Status is the outcome of one exchange attempt.
type Status string

const (
	Exchanged      Status = "exchanged"
	BelowThreshold Status = "below_threshold"
	Unavailable    Status = "unavailable"
)

// Decision is the recorded outcome. At is the caller's unix time.
type Decision struct {
	Required  bool   `json:"required"`
	Status    Status `json:"status"`
	Threshold uint64 `json:"threshold"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail,omitempty"`
	At        int64  `json:"at"`
}

// Envelope is the signed exchange unit.
type Envelope struct {
	Version   string   `json:"version"`
	Sender    string   `json:"sender"`
	Transfer  Transfer `json:"transfer"`
	Signature string   `json:"signature"`
}

// Transport delivers an envelope to the counterparty VASP. Authentication is
// the transport's job (for example mTLS or an authenticated HTTPS endpoint);
// the envelope signature lets the receiver attribute the data to Sender.
type Transport interface {
	Send(context.Context, Envelope) error
}

// Requires reports whether the amount is above the threshold. The comparison is
// strict: exactly at the threshold does not qualify.
func (p Policy) Requires(amount uint64) bool { return amount > p.Threshold }

// Validate checks every field the exchange depends on. Limits are counted in
// runes so Go and Python agree.
func Validate(t Transfer) error {
	for _, check := range []struct {
		field string
		value string
		max   int
	}{
		{"network", t.Network, 64},
		{"asset", t.Asset, 128},
		{"reference", t.Reference, 128},
	} {
		if err := text(check.field, check.value, 1, check.max); err != nil {
			return err
		}
	}
	if t.Amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if err := party("originator", t.Originator); err != nil {
		return err
	}
	if err := party("beneficiary", t.Beneficiary); err != nil {
		return err
	}
	if err := vasp("originating_vasp", t.OriginatingVASP); err != nil {
		return err
	}
	return vasp("beneficiary_vasp", t.BeneficiaryVASP)
}

func party(label string, p Party) error {
	if err := text(label+".name", p.Name, 1, 100); err != nil {
		return err
	}
	if err := text(label+".account", p.Account, 1, 100); err != nil {
		return err
	}
	if p.Address != "" {
		if err := text(label+".address", p.Address, 1, 200); err != nil {
			return err
		}
	}
	if p.Country != "" && (len(p.Country) != 2 || !upperAlpha(p.Country)) {
		return fmt.Errorf("%w: %s.country must be ISO 3166-1 alpha-2", ErrInvalid, label)
	}
	return nil
}

func vasp(label string, v VASP) error {
	if err := text(label+".id", v.ID, 1, 64); err != nil {
		return err
	}
	if v.Name != "" {
		return text(label+".name", v.Name, 1, 100)
	}
	return nil
}

func text(field, value string, min, max int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	if n := utf8.RuneCountInString(value); n < min || n > max {
		return fmt.Errorf("%w: %s must be %d..%d characters", ErrInvalid, field, min, max)
	}
	return nil
}

func upperAlpha(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

func partyMap(p Party) map[string]any {
	m := map[string]any{"name": p.Name, "account": p.Account}
	if p.Address != "" {
		m["address"] = p.Address
	}
	if p.Country != "" {
		m["country"] = p.Country
	}
	return m
}

func vaspMap(v VASP) map[string]any {
	m := map[string]any{"id": v.ID}
	if v.Name != "" {
		m["name"] = v.Name
	}
	return m
}

// Message returns the bytes signed by the sender: a domain-separated SHA-256
// over the canonical CBOR form of the transfer.
func Message(t Transfer) ([]byte, error) {
	encoded, err := protocol.Encode(map[string]any{
		"network":          t.Network,
		"asset":            t.Asset,
		"amount":           t.Amount,
		"reference":        t.Reference,
		"originator":       partyMap(t.Originator),
		"beneficiary":      partyMap(t.Beneficiary),
		"originating_vasp": vaspMap(t.OriginatingVASP),
		"beneficiary_vasp": vaspMap(t.BeneficiaryVASP),
	})
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte(signatureDomain))
	h.Write([]byte{0})
	h.Write(encoded)
	return h.Sum(nil), nil
}

// Seal validates the transfer and signs it with the sender's key.
func Seal(t Transfer, key *protocol.KeyPair) (*Envelope, error) {
	if err := Validate(t); err != nil {
		return nil, err
	}
	if key == nil || len(key.Private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: signing key is required", ErrInvalid)
	}
	msg, err := Message(t)
	if err != nil {
		return nil, err
	}
	return &Envelope{
		Version:   Version,
		Sender:    hex.EncodeToString(key.Public),
		Transfer:  t,
		Signature: hex.EncodeToString(ed25519.Sign(key.Private, msg)),
	}, nil
}

// Open verifies the envelope signature and validates the payload.
func Open(e Envelope) (Transfer, error) {
	if e.Version != Version {
		return Transfer{}, fmt.Errorf("%w: version %q", ErrInvalid, e.Version)
	}
	sender, err := hex.DecodeString(e.Sender)
	if err != nil || len(sender) != ed25519.PublicKeySize {
		return Transfer{}, fmt.Errorf("%w: sender public key", ErrInvalid)
	}
	sig, err := hex.DecodeString(e.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Transfer{}, fmt.Errorf("%w: signature", ErrInvalid)
	}
	msg, err := Message(e.Transfer)
	if err != nil {
		return Transfer{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(sender), msg, sig) {
		return Transfer{}, ErrSignature
	}
	if err := Validate(e.Transfer); err != nil {
		return Transfer{}, err
	}
	return e.Transfer, nil
}

// Decide validates the transfer and reports whether exchange is required.
func Decide(p Policy, t Transfer, at int64) (Decision, error) {
	if err := Validate(t); err != nil {
		return Decision{}, err
	}
	if p.Requires(t.Amount) {
		return Decision{Required: true, Status: "", Threshold: p.Threshold, Reason: "amount above threshold", At: at}, nil
	}
	return Decision{Required: false, Status: BelowThreshold, Threshold: p.Threshold, Reason: "amount at or below threshold", At: at}, nil
}

// Exchange seals and sends the transfer when the policy requires it. A
// transport failure becomes an Unavailable decision, never a panicked
// settlement; Detail carries the transport error for the caller's log.
func Exchange(ctx context.Context, p Policy, t Transfer, key *protocol.KeyPair, transport Transport, at int64) (Decision, error) {
	decision, err := Decide(p, t, at)
	if err != nil {
		return Decision{}, err
	}
	if !decision.Required {
		return decision, nil
	}
	envelope, err := Seal(t, key)
	if err != nil {
		return Decision{}, err
	}
	if transport == nil {
		decision.Status = Unavailable
		decision.Reason = "counterparty unavailable"
		decision.Detail = "no transport"
		return decision, nil
	}
	if err := transport.Send(ctx, *envelope); err != nil {
		decision.Status = Unavailable
		decision.Reason = "counterparty unavailable"
		decision.Detail = err.Error()
		return decision, nil
	}
	decision.Status = Exchanged
	decision.Reason = "data exchanged"
	return decision, nil
}
