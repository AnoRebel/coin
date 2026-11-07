package session

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type sessionVectors struct {
	Account     string `json:"account"`
	SessionSeed string `json:"session_seed"`
	OtherSeed   string `json:"other_seed"`
	Cases       []struct {
		Name      string        `json:"name"`
		Grant     grantJSON     `json:"grant"`
		Request   requestJSON   `json:"request"`
		Requests  []requestJSON `json:"requests"`
		Error     string        `json:"error"`
		Remaining uint64        `json:"expect_remaining"`
	} `json:"cases"`
	InvalidGrants []grantJSON `json:"invalid_grants"`
}

type grantJSON struct {
	Name      string   `json:"name"`
	Scopes    []Scope  `json:"scopes"`
	Assets    []string `json:"assets"`
	Limit     uint64   `json:"limit"`
	ExpiresAt uint64   `json:"expires_at"`
}

type requestJSON struct {
	Account string `json:"account"`
	Scope   Scope  `json:"scope"`
	Asset   string `json:"asset"`
	Amount  uint64 `json:"amount"`
	At      uint64 `json:"at"`
}

func loadSessionVectors(t *testing.T) sessionVectors {
	t.Helper()
	raw, err := os.ReadFile("../../spec/vectors/session.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors sessionVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func sessionPublicKey(t *testing.T, seedHex string) string {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("bad seed")
	}
	return hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
}

func buildGrant(t *testing.T, vectors sessionVectors, spec grantJSON) Grant {
	t.Helper()
	return Grant{
		Account: vectors.Account, PublicKey: sessionPublicKey(t, vectors.SessionSeed),
		Scopes: spec.Scopes, Assets: spec.Assets, Limit: spec.Limit, ExpiresAt: spec.ExpiresAt,
	}
}

func TestGrantVectors(t *testing.T) {
	vectors := loadSessionVectors(t)
	for _, spec := range vectors.InvalidGrants {
		grant := buildGrant(t, vectors, spec)
		if _, err := New(grant); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want invalid, got %v", spec.Name, err)
		}
	}
}

func TestAuthorizationVectors(t *testing.T) {
	vectors := loadSessionVectors(t)
	for _, test := range vectors.Cases {
		session, err := New(buildGrant(t, vectors, test.Grant))
		if err != nil {
			t.Fatalf("%s: %v", test.Name, err)
		}
		requests := test.Requests
		if len(requests) == 0 {
			requests = []requestJSON{test.Request}
		}
		var failure error
		for _, raw := range requests {
			if failure != nil {
				break
			}
			account := raw.Account
			if account == "" {
				account = vectors.Account
			}
			failure = session.Authorize(Request{
				Account: account, Scope: raw.Scope, Asset: raw.Asset,
				Amount: raw.Amount, At: raw.At,
			})
		}
		if test.Error != "" {
			if failure == nil || !errors.Is(failure, sessionError(test.Error)) {
				t.Fatalf("%s: want %s, got %v", test.Name, test.Error, failure)
			}
		} else if failure != nil {
			t.Fatalf("%s: %v", test.Name, failure)
		}
		if test.Remaining != 0 && session.Remaining() != test.Remaining {
			t.Fatalf("%s: remaining %d, want %d", test.Name, session.Remaining(), test.Remaining)
		}
	}
}

func TestSigningAndWatchOnly(t *testing.T) {
	vectors := loadSessionVectors(t)
	grant := Grant{
		Account: vectors.Account, PublicKey: sessionPublicKey(t, vectors.SessionSeed),
		Scopes: []Scope{ScopeTransfer}, Assets: []string{"arce"}, Limit: 100, ExpiresAt: 100,
	}
	session, err := New(grant)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Account: vectors.Account, Scope: ScopeTransfer, Asset: "arce", Amount: 10, At: 1}
	seed, _ := hex.DecodeString(vectors.SessionSeed)
	payload := []byte("canonical")
	signature, err := session.Sign(seed, payload, request)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), payload, signature) {
		t.Fatal("signature does not verify")
	}
	other, _ := hex.DecodeString(vectors.OtherSeed)
	if _, err := session.Sign(other, payload, request); !errors.Is(err, ErrKey) {
		t.Fatalf("wrong key: %v", err)
	}
	if session.Remaining() != 90 {
		t.Fatalf("wrong-key attempt consumed the limit: %d", session.Remaining())
	}

	account, err := NewAccount(vectors.Account, grant.PublicKey, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := account.Sign(seed, payload); !errors.Is(err, ErrWatchOnly) {
		t.Fatalf("watch-only sign: %v", err)
	}
	account.WatchOnly = false
	if _, err := account.Sign(seed, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := account.Sign(other, payload); !errors.Is(err, ErrKey) {
		t.Fatalf("wrong account key: %v", err)
	}
}

func sessionError(code string) error {
	switch code {
	case "invalid":
		return ErrInvalid
	case "account":
		return ErrAccount
	case "scope":
		return ErrScope
	case "asset":
		return ErrAsset
	case "limit":
		return ErrLimit
	case "expired":
		return ErrExpired
	case "key":
		return ErrKey
	}
	return nil
}
