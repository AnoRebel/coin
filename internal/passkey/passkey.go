// Package passkey verifies WebAuthn passkey assertions for scoped session and
// transaction authorization.
//
// A relying party (the wallet or console) chooses a scope: a session purpose,
// or a specific transaction payload. Challenge derives the WebAuthn challenge
// from that scope, and Authorize recomputes it from the scope it is asked to
// approve. An assertion therefore cannot be replayed for a different scope,
// origin, relying party, or after its expiry. The credential public key is
// stored at enrollment; no root account key ever reaches the authenticator.
//
// The package verifies assertions. It does not parse attestation objects or
// COSE keys: the enrolling layer extracts the credential ID, algorithm, and
// public key and passes them in.
package passkey

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"unicode/utf8"
)

const (
	PurposeSession     = "session"
	PurposeTransaction = "transaction"

	AlgEd25519 = "ed25519"
	AlgES256   = "es256"

	// MinAuthenticatorData is the RP hash, flags, and counter before extensions.
	MinAuthenticatorData = 37
	MaxCredentialID      = 1024

	flagUserPresent  = 0x01
	flagUserVerified = 0x04
)

var (
	ErrInvalid           = errors.New("passkey: invalid scope or credential")
	ErrDuplicate         = errors.New("passkey: credential already enrolled")
	ErrCredential        = errors.New("passkey: unknown credential")
	ErrExpired           = errors.New("passkey: assertion expired")
	ErrClientData        = errors.New("passkey: invalid client data")
	ErrChallenge         = errors.New("passkey: challenge mismatch")
	ErrOrigin            = errors.New("passkey: origin mismatch")
	ErrRPID              = errors.New("passkey: relying party mismatch")
	ErrFlags             = errors.New("passkey: user presence or verification missing")
	ErrAuthenticatorData = errors.New("passkey: invalid authenticator data")
	ErrCounter           = errors.New("passkey: counter did not advance")
	ErrSignature         = errors.New("passkey: bad signature")
)

// Scope is what an assertion is allowed to authorize.
type Scope struct {
	Purpose   string `json:"purpose"`
	Payload   string `json:"payload,omitempty"`
	ExpiresAt uint64 `json:"expires_at"`
	Nonce     string `json:"nonce"`
}

// Validate checks the purpose, payload shape, expiry, and nonce.
func (s Scope) Validate() error {
	switch s.Purpose {
	case PurposeSession:
		if s.Payload != "" {
			return fmt.Errorf("%w: session scope cannot bind a payload", ErrInvalid)
		}
	case PurposeTransaction:
		if !validHash(s.Payload) {
			return fmt.Errorf("%w: transaction scope needs a 32-byte payload hash", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: purpose", ErrInvalid)
	}
	if s.ExpiresAt == 0 {
		return fmt.Errorf("%w: expiry", ErrInvalid)
	}
	if !validLabel(s.Nonce) {
		return fmt.Errorf("%w: nonce", ErrInvalid)
	}
	return nil
}

// Challenge derives the base64url challenge for a scope.
func Challenge(scope Scope) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	preimage := "arne-passkey-v1\x00" + scope.Purpose + "\x00" + scope.Payload +
		"\x00" + strconv.FormatUint(scope.ExpiresAt, 10) + "\x00" + scope.Nonce
	sum := sha256.Sum256([]byte(preimage))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// Credential is an enrolled passkey.
type Credential struct {
	ID               string `json:"id"`
	Account          string `json:"account"`
	RPID             string `json:"rpid"`
	Origin           string `json:"origin"`
	Alg              string `json:"alg"`
	PublicKey        string `json:"public_key"`
	UserVerification bool   `json:"user_verification"`
	Counter          uint32 `json:"counter"`
}

// Validate checks the credential identity and public key.
func (c Credential) Validate() error {
	if !validCredentialID(c.ID) || !validLabel(c.Account) || !validLabel(c.RPID) {
		return fmt.Errorf("%w: credential identity", ErrInvalid)
	}
	if !validOrigin(c.Origin) {
		return fmt.Errorf("%w: origin", ErrInvalid)
	}
	switch c.Alg {
	case AlgEd25519:
		raw, err := hex.DecodeString(c.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: ed25519 key", ErrInvalid)
		}
	case AlgES256:
		raw, err := hex.DecodeString(c.PublicKey)
		if err != nil || len(raw) != 65 || raw[0] != 4 {
			return fmt.Errorf("%w: es256 key", ErrInvalid)
		}
		if _, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw); err != nil {
			return fmt.Errorf("%w: es256 key", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: algorithm", ErrInvalid)
	}
	return nil
}

// Assertion is the browser's WebAuthn assertion.
type Assertion struct {
	ClientDataJSON    []byte
	AuthenticatorData []byte
	Signature         []byte
}

type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

// Manager holds enrolled credentials and their signature counters.
type Manager struct {
	mu          sync.Mutex
	credentials map[string]Credential
}

// NewManager creates an empty registry.
func NewManager() (*Manager, error) {
	return &Manager{credentials: map[string]Credential{}}, nil
}

// Enroll records a credential. Re-enrolling the same ID is rejected.
func (m *Manager) Enroll(credential Credential) error {
	if err := credential.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.credentials[credential.ID]; ok {
		return ErrDuplicate
	}
	m.credentials[credential.ID] = credential
	return nil
}

// Revoke removes a credential.
func (m *Manager) Revoke(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.credentials[id]; !ok {
		return ErrCredential
	}
	delete(m.credentials, id)
	return nil
}

// Get returns an enrolled credential.
func (m *Manager) Get(id string) (Credential, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	credential, ok := m.credentials[id]
	return credential, ok
}

// Authorize verifies an assertion against a scope and advances the counter.
func (m *Manager) Authorize(id string, scope Scope, assertion Assertion, now uint64) (Credential, error) {
	if err := scope.Validate(); err != nil {
		return Credential{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	credential, ok := m.credentials[id]
	if !ok {
		return Credential{}, ErrCredential
	}
	if now >= scope.ExpiresAt {
		return Credential{}, ErrExpired
	}

	var client clientData
	if err := json.Unmarshal(assertion.ClientDataJSON, &client); err != nil || client.Type != "webauthn.get" {
		return Credential{}, ErrClientData
	}
	expected, err := Challenge(scope)
	if err != nil {
		return Credential{}, err
	}
	if client.Challenge != expected {
		return Credential{}, ErrChallenge
	}
	if client.Origin != credential.Origin {
		return Credential{}, ErrOrigin
	}

	if len(assertion.AuthenticatorData) < MinAuthenticatorData {
		return Credential{}, ErrAuthenticatorData
	}
	rpHash := sha256.Sum256([]byte(credential.RPID))
	if !bytes.Equal(assertion.AuthenticatorData[:32], rpHash[:]) {
		return Credential{}, ErrRPID
	}
	flags := assertion.AuthenticatorData[32]
	if flags&flagUserPresent == 0 || (credential.UserVerification && flags&flagUserVerified == 0) {
		return Credential{}, ErrFlags
	}
	counter := binary.BigEndian.Uint32(assertion.AuthenticatorData[33:37])
	if counter != 0 && credential.Counter != 0 && counter <= credential.Counter {
		return Credential{}, ErrCounter
	}
	if !verifySignature(credential, assertion) {
		return Credential{}, ErrSignature
	}
	if counter > credential.Counter {
		credential.Counter = counter
		m.credentials[id] = credential
	}
	return credential, nil
}

func verifySignature(credential Credential, assertion Assertion) bool {
	clientHash := sha256.Sum256(assertion.ClientDataJSON)
	message := make([]byte, 0, len(assertion.AuthenticatorData)+len(clientHash))
	message = append(message, assertion.AuthenticatorData...)
	message = append(message, clientHash[:]...)
	publicKey, err := hex.DecodeString(credential.PublicKey)
	if err != nil {
		return false
	}
	switch credential.Alg {
	case AlgEd25519:
		return ed25519.Verify(ed25519.PublicKey(publicKey), message, assertion.Signature)
	case AlgES256:
		key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), publicKey)
		if err != nil {
			return false
		}
		digest := sha256.Sum256(message)
		return ecdsa.VerifyASN1(key, digest[:], assertion.Signature)
	}
	return false
}

func validOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return true
}

func validLabel(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validCredentialID(value string) bool {
	if value == "" || len(value) > MaxCredentialID {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
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
