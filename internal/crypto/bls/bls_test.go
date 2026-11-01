package bls

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type vectorDoc struct {
	Network    string `json:"network"`
	Message    string `json:"message"`
	Validators []struct {
		ID        string `json:"id"`
		Seed      string `json:"seed"`
		Secret    string `json:"secret"`
		PublicKey string `json:"public_key"`
		PoP       string `json:"pop"`
	} `json:"validators"`
	Aggregate struct {
		Validators []string `json:"validators"`
		Signature  string   `json:"signature"`
	} `json:"aggregate"`
	Rogue struct {
		Victim          string `json:"victim"`
		PublicKey       string `json:"public_key"`
		ForgedSignature string `json:"forged_signature"`
	} `json:"rogue"`
}

func load(t *testing.T) vectorDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "bls.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc vectorDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSharedVectors(t *testing.T) {
	doc := load(t)
	msg := []byte(doc.Message)
	registry, err := NewRegistry(doc.Network)
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string][]byte{}
	for _, v := range doc.Validators {
		secret, err := SecretFromSeed([]byte(v.Seed))
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(secret) != v.Secret {
			t.Fatalf("%s secret = %x, want %s", v.ID, secret, v.Secret)
		}
		pub, err := PublicKeyOf(secret)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(pub) != v.PublicKey {
			t.Fatalf("%s public key = %x, want %s", v.ID, pub, v.PublicKey)
		}
		pop, err := SignPoP(secret, doc.Network, v.ID)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(pop) != v.PoP {
			t.Fatalf("%s PoP = %x, want %s", v.ID, pop, v.PoP)
		}
		if err := registry.Register(v.ID, pub, pop); err != nil {
			t.Fatalf("register %s: %v", v.ID, err)
		}
		sig, err := Sign(secret, msg)
		if err != nil {
			t.Fatal(err)
		}
		if !registry.Verify(v.ID, msg, sig) {
			t.Fatalf("%s signature did not verify", v.ID)
		}
		secrets[v.ID] = secret
	}

	var sigs []Signature
	var pubs []PublicKey
	for _, id := range doc.Aggregate.Validators {
		sig, err := Sign(secrets[id], msg)
		if err != nil {
			t.Fatal(err)
		}
		sigs = append(sigs, sig)
		pub, _ := registry.PublicKey(id)
		pubs = append(pubs, pub)
	}
	agg, err := Aggregate(sigs)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(agg) != doc.Aggregate.Signature {
		t.Fatalf("aggregate = %x, want %s", agg, doc.Aggregate.Signature)
	}
	if err := registry.VerifyAggregate(doc.Aggregate.Validators, msg, agg); err != nil {
		t.Fatal(err)
	}
	if VerifyAggregate(pubs, []byte("other"), agg) {
		t.Fatal("aggregate verified for the wrong message")
	}
}

func TestMissingPoPIsRejected(t *testing.T) {
	doc := load(t)
	msg := []byte(doc.Message)
	registry, _ := NewRegistry(doc.Network)
	v1 := doc.Validators[0]
	if err := registry.Register(v1.ID, mustHex(t, v1.PublicKey), mustHex(t, v1.PoP)); err != nil {
		t.Fatal(err)
	}
	sig, err := Sign(mustHex(t, v1.Secret), msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.VerifyAggregate([]string{v1.ID, doc.Validators[1].ID}, msg, sig); !errors.Is(err, ErrNoPoP) {
		t.Fatalf("missing PoP: %v", err)
	}
	if err := registry.VerifyAggregate([]string{v1.ID, v1.ID}, msg, sig); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate id: %v", err)
	}
	if err := registry.Register(v1.ID, mustHex(t, v1.PublicKey), mustHex(t, v1.PoP)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate register: %v", err)
	}
}

func TestRogueKeyCannotRegister(t *testing.T) {
	doc := load(t)
	v1 := doc.Validators[0]
	registry, _ := NewRegistry(doc.Network)
	if err := registry.Register(v1.ID, mustHex(t, v1.PublicKey), mustHex(t, v1.PoP)); err != nil {
		t.Fatal(err)
	}
	rogue := mustHex(t, doc.Rogue.PublicKey)
	forged := mustHex(t, doc.Rogue.ForgedSignature)

	// The forged aggregate is valid curve math: that is the attack.
	pubs := []PublicKey{PublicKey(rogue), mustHex(t, v1.PublicKey)}
	if !VerifyAggregate(pubs, []byte(doc.Message), Signature(forged)) {
		t.Fatal("rogue aggregate should pass raw math; the PoP check is the defense")
	}
	// Registering the rogue key fails because the attacker cannot sign its PoP.
	if err := registry.Register("rogue", PublicKey(rogue), Signature(forged)); !errors.Is(err, ErrRogueKey) {
		t.Fatalf("rogue register: %v", err)
	}
	// And an unregistered key cannot join a registry aggregate.
	if err := registry.VerifyAggregate([]string{v1.ID, "rogue"}, []byte(doc.Message), Signature(forged)); !errors.Is(err, ErrNoPoP) {
		t.Fatalf("rogue aggregate: %v", err)
	}
}

func TestValidation(t *testing.T) {
	if _, err := NewRegistry(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty network: %v", err)
	}
	if _, err := SecretFromSeed(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty seed: %v", err)
	}
	if _, err := SecretFromSeed([]byte("short")); err != nil {
		t.Fatalf("short seed: %v", err)
	}
	if _, err := PublicKeyOf([]byte("short")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short secret: %v", err)
	}
	if _, err := Sign([]byte("short"), []byte("m")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short secret sign: %v", err)
	}
	if _, err := Aggregate(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty aggregate: %v", err)
	}
	if _, err := Aggregate([]Signature{[]byte("bad")}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad aggregate input: %v", err)
	}
	if Verify(nil, []byte("m"), nil) {
		t.Fatal("empty verify")
	}
	registry, _ := NewRegistry("arce:devnet")
	if err := registry.Register("", nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty register: %v", err)
	}
	if registry.Verify("missing", []byte("m"), []byte("s")) {
		t.Fatal("unknown validator verified")
	}
	if err := registry.VerifyAggregate(nil, []byte("m"), []byte("s")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty validators: %v", err)
	}
}
