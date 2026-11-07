// Package session enforces scoped, expiring session keys and watch-only
// accounts.
//
// A grant names an account, a session public key, scopes, allowed assets, a
// cumulative spending limit, and an expiry. Authorize checks one request and
// consumes the limit; Sign checks the key and the request before producing a
// signature. A watch-only account can never sign.
package session

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
)

// Scope is a permitted operation.
type Scope string

const (
	ScopeRead     Scope = "read"
	ScopeTransfer Scope = "transfer"
	ScopePayment  Scope = "payment"
	ScopeAudit    Scope = "audit"
)

var (
	ErrInvalid   = errors.New("session: invalid grant or request")
	ErrAccount   = errors.New("session: wrong account")
	ErrScope     = errors.New("session: scope not granted")
	ErrAsset     = errors.New("session: asset not granted")
	ErrLimit     = errors.New("session: spending limit exceeded")
	ErrExpired   = errors.New("session: expired")
	ErrKey       = errors.New("session: key does not match the session")
	ErrWatchOnly = errors.New("session: watch-only account cannot sign")
)

var scopes = map[Scope]bool{
	ScopeRead: true, ScopeTransfer: true, ScopePayment: true, ScopeAudit: true,
}

// Grant is a session-key authorization.
type Grant struct {
	Account   string   `json:"account"`
	PublicKey string   `json:"public_key"`
	Scopes    []Scope  `json:"scopes"`
	Assets    []string `json:"assets,omitempty"`
	Limit     uint64   `json:"limit"`
	ExpiresAt uint64   `json:"expires_at"`
}

// Request is one operation checked against a grant.
type Request struct {
	Account string `json:"account"`
	Scope   Scope  `json:"scope"`
	Asset   string `json:"asset,omitempty"`
	Amount  uint64 `json:"amount,omitempty"`
	At      uint64 `json:"at"`
}

// Session tracks the remaining limit of one grant.
type Session struct {
	grant     Grant
	remaining uint64
}

// New validates a grant and creates a session with its full limit.
func New(grant Grant) (*Session, error) {
	if !validLabel(grant.Account) || !validHash(grant.PublicKey) || grant.ExpiresAt == 0 {
		return nil, fmt.Errorf("%w: account, key, or expiry", ErrInvalid)
	}
	if len(grant.Scopes) == 0 {
		return nil, fmt.Errorf("%w: no scopes", ErrInvalid)
	}
	seenScopes := map[Scope]bool{}
	spending := false
	for _, scope := range grant.Scopes {
		if !scopes[scope] || seenScopes[scope] {
			return nil, fmt.Errorf("%w: scope %q", ErrInvalid, scope)
		}
		seenScopes[scope] = true
		if scope == ScopeTransfer || scope == ScopePayment {
			spending = true
		}
	}
	seenAssets := map[string]bool{}
	for _, asset := range grant.Assets {
		if !validLabel(asset) || seenAssets[asset] {
			return nil, fmt.Errorf("%w: asset %q", ErrInvalid, asset)
		}
		seenAssets[asset] = true
	}
	if spending && (len(grant.Assets) == 0 || grant.Limit == 0) {
		return nil, fmt.Errorf("%w: spending scope needs assets and a limit", ErrInvalid)
	}
	return &Session{grant: grant, remaining: grant.Limit}, nil
}

// Grant returns the session's grant.
func (s *Session) Grant() Grant {
	return s.grant
}

// Remaining returns the unspent limit.
func (s *Session) Remaining() uint64 {
	return s.remaining
}

// Authorize checks a request and consumes its amount when it is a spending
// operation.
func (s *Session) Authorize(request Request) error {
	if request.At >= s.grant.ExpiresAt {
		return ErrExpired
	}
	if request.Account != s.grant.Account {
		return ErrAccount
	}
	if !hasScope(s.grant.Scopes, request.Scope) {
		return ErrScope
	}
	if request.Scope != ScopeTransfer && request.Scope != ScopePayment {
		return nil
	}
	if request.Amount == 0 {
		return fmt.Errorf("%w: zero amount", ErrInvalid)
	}
	if !hasAsset(s.grant.Assets, request.Asset) {
		return ErrAsset
	}
	if request.Amount > s.remaining {
		return ErrLimit
	}
	s.remaining -= request.Amount
	return nil
}

// Sign authorizes a request and signs the payload with the session key.
func (s *Session) Sign(seed, payload []byte, request Request) ([]byte, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: seed", ErrInvalid)
	}
	private := ed25519.NewKeyFromSeed(seed)
	if hex.EncodeToString(private.Public().(ed25519.PublicKey)) != s.grant.PublicKey {
		return nil, ErrKey
	}
	if err := s.Authorize(request); err != nil {
		return nil, err
	}
	return ed25519.Sign(private, payload), nil
}

// Account is a wallet account that may be watch-only.
type Account struct {
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
	WatchOnly bool   `json:"watch_only"`
}

// NewAccount validates an account view.
func NewAccount(address, publicKey string, watchOnly bool) (Account, error) {
	if !validLabel(address) || !validHash(publicKey) {
		return Account{}, fmt.Errorf("%w: address or key", ErrInvalid)
	}
	return Account{Address: address, PublicKey: publicKey, WatchOnly: watchOnly}, nil
}

// Sign signs with the account key. Watch-only accounts always fail.
func (a Account) Sign(seed, payload []byte) ([]byte, error) {
	if a.WatchOnly {
		return nil, ErrWatchOnly
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: seed", ErrInvalid)
	}
	private := ed25519.NewKeyFromSeed(seed)
	if hex.EncodeToString(private.Public().(ed25519.PublicKey)) != a.PublicKey {
		return nil, ErrKey
	}
	return ed25519.Sign(private, payload), nil
}

func hasScope(granted []Scope, want Scope) bool {
	for _, scope := range granted {
		if scope == want {
			return true
		}
	}
	return false
}

func hasAsset(granted []string, want string) bool {
	for _, asset := range granted {
		if asset == want {
			return true
		}
	}
	return false
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
