// Package signer defines a common interface for software, hardware, and
// external transaction signers.
//
// A signer receives only human-readable transaction details and the canonical
// payload built from them. It never receives an account seed unless it is a
// software signer that already holds one. A hardware adapter displays the
// details before signing and verifies the returned signature against the
// signer's public key.
package signer

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	SuiteEd25519 = "ed25519-v1"
	// MaxPayloadBytes bounds the canonical payload a device must review.
	MaxPayloadBytes = 4096
)

var (
	ErrInvalid     = errors.New("signer: invalid details")
	ErrUnsupported = errors.New("signer: unsupported suite or payload")
	ErrSignature   = errors.New("signer: bad signature")
	ErrDisplay     = errors.New("signer: confirmation failed")
)

// Details is the human-readable transaction a signer reviews.
type Details struct {
	Network   string `json:"network"`
	Account   string `json:"account"`
	To        string `json:"to"`
	Asset     string `json:"asset"`
	Amount    uint64 `json:"amount"`
	Fee       uint64 `json:"fee"`
	FeeAsset  string `json:"fee_asset"`
	Nonce     uint64 `json:"nonce"`
	ExpiresAt uint64 `json:"expires_at"`
	Memo      string `json:"memo,omitempty"`
}

// Validate checks that the details are complete and display-safe.
func (d Details) Validate() error {
	for _, value := range []string{d.Network, d.Account, d.To, d.Asset, d.FeeAsset} {
		if !validLabel(value) {
			return fmt.Errorf("%w: missing or invalid field", ErrInvalid)
		}
	}
	if d.Amount == 0 || d.ExpiresAt == 0 {
		return fmt.Errorf("%w: amount and expiry are required", ErrInvalid)
	}
	if len(d.Memo) > 256 || !utf8.ValidString(d.Memo) {
		return fmt.Errorf("%w: memo", ErrInvalid)
	}
	return nil
}

// Human renders the fields a device displays.
func (d Details) Human() string {
	lines := []string{
		"network: " + d.Network,
		"account: " + d.Account,
		"to: " + d.To,
		"asset: " + d.Asset,
		"amount: " + strconv.FormatUint(d.Amount, 10),
		"fee: " + strconv.FormatUint(d.Fee, 10) + " " + d.FeeAsset,
		"nonce: " + strconv.FormatUint(d.Nonce, 10),
		"expires: " + strconv.FormatUint(d.ExpiresAt, 10),
	}
	if d.Memo != "" {
		lines = append(lines, "memo: "+strconv.Quote(d.Memo))
	}
	return strings.Join(lines, "\n")
}

// Payload is the canonical bytes a signer signs.
func (d Details) Payload() []byte {
	return []byte("arne-sign-v1\n" + d.Human())
}

// Signer is a software, hardware, or external signer.
type Signer interface {
	PublicKey() string
	Suite() string
	Sign(Details) ([]byte, error)
}

// Software signs with an in-process Ed25519 key.
type Software struct {
	private ed25519.PrivateKey
}

// NewSoftware builds a software signer from a 32-byte seed.
func NewSoftware(seed []byte) (*Software, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: seed", ErrInvalid)
	}
	return &Software{private: ed25519.NewKeyFromSeed(seed)}, nil
}

// PublicKey returns the hex-encoded public key.
func (s *Software) PublicKey() string {
	return hex.EncodeToString(s.private.Public().(ed25519.PublicKey))
}

// Suite returns the signature suite.
func (s *Software) Suite() string {
	return SuiteEd25519
}

// Sign validates the details and signs their canonical payload.
func (s *Software) Sign(details Details) ([]byte, error) {
	if err := details.Validate(); err != nil {
		return nil, err
	}
	return ed25519.Sign(s.private, details.Payload()), nil
}

// Device is a hardware or external signer transport.
type Device interface {
	PublicKey() string
	Suite() string
	// Display shows the human-readable details and returns an error if the
	// user rejects them.
	Display(text string) error
	// Sign receives only the canonical payload, never an account seed.
	Sign(payload []byte) ([]byte, error)
}

// Hardware adapts a device behind the Signer interface.
type Hardware struct {
	device Device
}

// NewHardware checks that the device exposes a usable key and suite.
func NewHardware(device Device) (*Hardware, error) {
	if device == nil || device.Suite() == "" || !validPublicKey(device.PublicKey()) {
		return nil, fmt.Errorf("%w: device identity", ErrInvalid)
	}
	return &Hardware{device: device}, nil
}

// PublicKey returns the device public key.
func (h *Hardware) PublicKey() string {
	return h.device.PublicKey()
}

// Suite returns the device suite.
func (h *Hardware) Suite() string {
	return h.device.Suite()
}

// Sign displays the details, signs the canonical payload, and verifies the
// device signature before returning it.
func (h *Hardware) Sign(details Details) ([]byte, error) {
	if err := details.Validate(); err != nil {
		return nil, err
	}
	if h.device.Suite() != SuiteEd25519 {
		return nil, fmt.Errorf("%w: suite %q", ErrUnsupported, h.device.Suite())
	}
	payload := details.Payload()
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: payload too large", ErrUnsupported)
	}
	if err := h.device.Display(details.Human()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDisplay, err)
	}
	signature, err := h.device.Sign(payload)
	if err != nil {
		return nil, err
	}
	pub, err := hex.DecodeString(h.device.PublicKey())
	if err != nil || len(pub) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(pub, payload, signature) {
		return nil, ErrSignature
	}
	return signature, nil
}

// Verify checks a signature from any signer over the details.
func Verify(publicKey string, details Details, signature []byte) bool {
	pub, err := hex.DecodeString(publicKey)
	return err == nil && len(pub) == ed25519.PublicKeySize && details.Validate() == nil &&
		ed25519.Verify(pub, details.Payload(), signature)
}

func validPublicKey(value string) bool {
	pub, err := hex.DecodeString(value)
	return err == nil && len(pub) == ed25519.PublicKeySize
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
