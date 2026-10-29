package memo

import (
	"bytes"
	"errors"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

const mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

func testKey(t *testing.T, index uint32) *protocol.KeyPair {
	t.Helper()
	kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, index, "arne:devnet")
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func TestSealOpen(t *testing.T) {
	alice, bob, carol := testKey(t, 0), testKey(t, 1), testKey(t, 2)
	pub, err := PublicKey(bob.Private.Seed())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := RandomSeal(pub, []byte("invoice 42"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("invoice")) {
		t.Fatal("plaintext visible in sealed memo")
	}
	if err := Validate(sealed); err != nil {
		t.Fatal(err)
	}
	plain, err := Open(bob.Private.Seed(), sealed)
	if err != nil || string(plain) != "invoice 42" {
		t.Fatalf("open = %q %v", plain, err)
	}
	if _, err := Open(alice.Private.Seed(), sealed); !errors.Is(err, ErrOpen) {
		t.Fatalf("wrong recipient opened memo: %v", err)
	}
	if _, err := Open(carol.Private.Seed(), sealed); !errors.Is(err, ErrOpen) {
		t.Fatal("third party opened memo")
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := Open(bob.Private.Seed(), tampered); err == nil {
		t.Fatal("tampered memo opened")
	}
}

func TestSealLimits(t *testing.T) {
	bob := testKey(t, 1)
	pub, err := PublicKey(bob.Private.Seed())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RandomSeal(pub, make([]byte, maxPlaintext+1)); err == nil {
		t.Fatal("oversize memo sealed")
	}
	sealed, err := RandomSeal(pub, make([]byte, maxPlaintext))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(sealed); err != nil {
		t.Fatalf("max-size memo rejected: %v", err)
	}
	if err := Validate([]byte("not cbor")); err == nil {
		t.Fatal("garbage accepted")
	}
	short, err := protocol.Encode(map[string]any{
		"v": uint64(Version), "epk": make([]byte, 32), "nonce": make([]byte, 12), "ct": []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(short); err == nil {
		t.Fatal("short ciphertext accepted")
	}
}
