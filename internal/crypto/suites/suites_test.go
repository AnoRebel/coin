package suites

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func edVerifier(pub, msg, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

func TestSharedVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "suites.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Suites []struct {
			ID        string `json:"id"`
			Algorithm string `json:"algorithm"`
			Status    Status `json:"status"`
		} `json:"suites"`
		Cases []struct {
			Suite    string `json:"suite"`
			Register bool   `json:"register"`
			Verify   bool   `json:"verify"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	entries := make([]Suite, 0, len(doc.Suites))
	for _, s := range doc.Suites {
		entries = append(entries, Suite{ID: s.ID, Algorithm: s.Algorithm, Status: s.Status, Verify: edVerifier})
	}
	r, err := New(entries)
	if err != nil {
		t.Fatal(err)
	}

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	msg := []byte("register account")
	sig := ed25519.Sign(priv, msg)

	for _, c := range doc.Cases {
		regErr := r.CanRegister(c.Suite)
		if (regErr == nil) != c.Register {
			t.Fatalf("%s register = %v, want %v", c.Suite, regErr, c.Register)
		}
		verErr := r.Verify(c.Suite, pub, msg, sig)
		if (verErr == nil) != c.Verify {
			t.Fatalf("%s verify = %v, want %v", c.Suite, verErr, c.Verify)
		}
		if !c.Register && regErr != nil {
			switch c.Suite {
			case "ed25519-v1":
				if !errors.Is(regErr, ErrDeprecated) {
					t.Fatalf("deprecated error: %v", regErr)
				}
			case "rsa-legacy":
				if !errors.Is(regErr, ErrDisabled) {
					t.Fatalf("disabled error: %v", regErr)
				}
			default:
				if !errors.Is(regErr, ErrUnknown) {
					t.Fatalf("unknown error: %v", regErr)
				}
			}
		}
	}
}

func TestValidation(t *testing.T) {
	bad := [][]Suite{
		{{ID: "", Algorithm: "ed25519", Status: Active, Verify: edVerifier}},
		{{ID: "a", Algorithm: "", Status: Active, Verify: edVerifier}},
		{{ID: "a", Algorithm: "ed25519", Status: "retired", Verify: edVerifier}},
		{{ID: "a", Algorithm: "ed25519", Status: Active}},
		{{ID: "a", Algorithm: "ed25519", Status: Active, Verify: edVerifier},
			{ID: "a", Algorithm: "ed25519", Status: Active, Verify: edVerifier}},
	}
	for i, entries := range bad {
		if _, err := New(entries); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	r, err := New([]Suite{{ID: "off", Algorithm: "ed25519", Status: Disabled}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Verify("off", nil, nil, nil); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled verify: %v", err)
	}
	if got := r.IDs(); len(got) != 1 || got[0] != "off" {
		t.Fatalf("ids = %v", got)
	}
}
