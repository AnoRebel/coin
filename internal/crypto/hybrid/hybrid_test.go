package hybrid

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

type vectorDoc struct {
	Message string `json:"message"`
	Seed    string `json:"seed"`
	Key     struct {
		Ed25519 string `json:"ed25519"`
		MLDSA   string `json:"ml_dsa_65"`
	} `json:"key"`
	Signature struct {
		Ed25519 string `json:"ed25519"`
		MLDSA   string `json:"ml_dsa_65"`
	} `json:"signature"`
}

func load(t *testing.T) (vectorDoc, []byte, Key, Signature) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "hybrid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc vectorDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(doc.Seed)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{Ed25519: mustHex(t, doc.Key.Ed25519), MLDSA: mustHex(t, doc.Key.MLDSA)}
	sig := Signature{Ed25519: mustHex(t, doc.Signature.Ed25519), MLDSA: mustHex(t, doc.Signature.MLDSA)}
	return doc, seed, key, sig
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	out, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSharedVectors(t *testing.T) {
	doc, seed, key, sig := load(t)
	message := []byte(doc.Message)

	var mlSeed [32]byte
	copy(mlSeed[:], seed)
	mlPub, mlPriv := mldsa65.NewKeyFromSeed(&mlSeed)
	edPriv := ed25519.NewKeyFromSeed(seed)
	edPub := edPriv.Public().(ed25519.PublicKey)
	if hex.EncodeToString(edPub) != hex.EncodeToString(key.Ed25519) {
		t.Fatal("ed25519 key mismatch")
	}
	if hex.EncodeToString(mlPub.Bytes()) != hex.EncodeToString(key.MLDSA) {
		t.Fatal("ml-dsa key mismatch")
	}
	if err := Register(Key{Ed25519: edPub, MLDSA: mlPub.Bytes()}); err != nil {
		t.Fatal(err)
	}
	if err := Verify(key, message, sig); err != nil {
		t.Fatalf("vector signature: %v", err)
	}

	// Deterministic signing reproduces the vector exactly in both languages.
	got, err := Sign(edPriv, mlPriv.Bytes(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Ed25519, sig.Ed25519) || !bytes.Equal(got.MLDSA, sig.MLDSA) {
		t.Fatal("signature is not deterministic")
	}
}

func TestDowngradeAndPartialSignaturesFail(t *testing.T) {
	doc, _, key, sig := load(t)
	message := []byte(doc.Message)

	// A registration with only one algorithm is rejected.
	if err := Register(Key{Ed25519: key.Ed25519}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ed-only registration: %v", err)
	}
	if err := Register(Key{MLDSA: key.MLDSA}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ml-only registration: %v", err)
	}
	// Stripping either signature is a partial signature, not a valid one.
	for _, partial := range []Signature{
		{Ed25519: sig.Ed25519},
		{MLDSA: sig.MLDSA},
	} {
		if err := Verify(key, message, partial); !errors.Is(err, ErrPartial) {
			t.Fatalf("partial signature: %v", err)
		}
	}
	// A valid Ed25519 signature plus a corrupted ML-DSA one must fail.
	bad := Signature{Ed25519: sig.Ed25519, MLDSA: append([]byte(nil), sig.MLDSA...)}
	bad.MLDSA[0] ^= 0xff
	if err := Verify(key, message, bad); !errors.Is(err, ErrVerify) {
		t.Fatalf("corrupted ml-dsa: %v", err)
	}
	bad = Signature{Ed25519: append([]byte(nil), sig.Ed25519...), MLDSA: sig.MLDSA}
	bad.Ed25519[0] ^= 0xff
	if err := Verify(key, message, bad); !errors.Is(err, ErrVerify) {
		t.Fatalf("corrupted ed25519: %v", err)
	}
	if err := Verify(key, []byte("other message"), sig); !errors.Is(err, ErrVerify) {
		t.Fatalf("wrong message: %v", err)
	}
}

func TestValidation(t *testing.T) {
	if _, err := Sign(nil, nil, []byte("m")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty keys: %v", err)
	}
	_, _, key, sig := load(t)
	if err := Verify(key, nil, sig); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty message: %v", err)
	}
	if err := Verify(key, []byte("m"), Signature{Ed25519: []byte("short"), MLDSA: sig.MLDSA}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short ed signature: %v", err)
	}
	if err := Verify(Key{Ed25519: []byte("short"), MLDSA: key.MLDSA}, []byte("m"), sig); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short ed key: %v", err)
	}
}
