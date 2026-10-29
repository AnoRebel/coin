// Package memo implements opt-in sealed memos: the recipient's X25519 key
// decrypts them, while everyone else sees only ciphertext.
package memo

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Version is the sealed-memo envelope version.
const Version = 1

// maxPlaintext is the largest memo the ledger accepts, matching the plaintext
// memo limit in ledger.DefaultParams.
const maxPlaintext = 256

const info = "arne-memo-v1"

var (
	ErrSealed = errors.New("memo: malformed sealed memo")
	ErrOpen   = errors.New("memo: cannot decrypt")
)

// derive turns an Ed25519 seed into a deterministic X25519 key pair. The scalar
// is clamped explicitly so Go and Python produce identical keys.
func derive(seed []byte) (priv [32]byte, pub []byte, err error) {
	if len(seed) != 32 {
		return priv, nil, fmt.Errorf("%w: seed length", ErrSealed)
	}
	h := sha256.New()
	h.Write([]byte(info))
	h.Write(seed)
	copy(priv[:], h.Sum(nil))
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err = curve25519.X25519(priv[:], curve25519.Basepoint)
	return priv, pub, err
}

// PublicKey returns the memo public key for a wallet seed. Share it with senders
// who want to encrypt a memo to you.
func PublicKey(seed []byte) ([]byte, error) {
	_, pub, err := derive(seed)
	return pub, err
}

func key(shared []byte) ([]byte, error) {
	out := make([]byte, chacha20poly1305.KeySize)
	k := hkdf.New(sha256.New, shared, nil, []byte(info))
	if _, err := io.ReadFull(k, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Seal encrypts plaintext to a recipient memo public key. rnd supplies the
// ephemeral seed and nonce; pass crypto/rand.Reader in production.
func Seal(recipient, plaintext []byte, rnd io.Reader) ([]byte, error) {
	if len(recipient) != 32 {
		return nil, fmt.Errorf("%w: recipient key", ErrSealed)
	}
	if len(plaintext) > maxPlaintext {
		return nil, fmt.Errorf("%w: memo above %d bytes", ErrSealed, maxPlaintext)
	}
	ephSeed := make([]byte, 32)
	nonce := make([]byte, chacha20poly1305.NonceSize)
	if _, err := io.ReadFull(rnd, ephSeed); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rnd, nonce); err != nil {
		return nil, err
	}
	ephPriv, ephPub, err := derive(ephSeed)
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(ephPriv[:], recipient)
	if err != nil {
		return nil, fmt.Errorf("%w: recipient key", ErrSealed)
	}
	k, err := key(shared)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(k)
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	return protocol.Encode(map[string]any{
		"v": uint64(Version), "epk": ephPub, "nonce": nonce, "ct": ct,
	})
}

// Open decrypts a sealed memo with the recipient's wallet seed.
func Open(seed, sealed []byte) ([]byte, error) {
	env, err := parse(sealed)
	if err != nil {
		return nil, err
	}
	priv, _, err := derive(seed)
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(priv[:], env.epk)
	if err != nil {
		return nil, ErrOpen
	}
	k, err := key(shared)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(k)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, env.nonce, env.ct, nil)
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}

// Validate checks a sealed memo before it enters a transaction. The ledger
// cannot decrypt it; this bounds size and shape only.
func Validate(sealed []byte) error {
	env, err := parse(sealed)
	if err != nil {
		return err
	}
	if len(env.ct) < chacha20poly1305.Overhead || len(env.ct) > maxPlaintext+chacha20poly1305.Overhead {
		return fmt.Errorf("%w: ciphertext length", ErrSealed)
	}
	return nil
}

type envelope struct {
	epk   []byte
	nonce []byte
	ct    []byte
}

func parse(raw []byte) (envelope, error) {
	m, err := protocol.DecodeMap(raw)
	if err != nil {
		return envelope{}, fmt.Errorf("%w: %v", ErrSealed, err)
	}
	if len(m) != 4 {
		return envelope{}, fmt.Errorf("%w: unknown field", ErrSealed)
	}
	v, ok := m["v"].(uint64)
	if !ok || v != Version {
		return envelope{}, fmt.Errorf("%w: version", ErrSealed)
	}
	env := envelope{}
	for key, dst := range map[string]*[]byte{"epk": &env.epk, "nonce": &env.nonce, "ct": &env.ct} {
		b, ok := m[key].([]byte)
		if !ok {
			return envelope{}, fmt.Errorf("%w: %s", ErrSealed, key)
		}
		*dst = b
	}
	if len(env.epk) != curve25519.PointSize || len(env.nonce) != chacha20poly1305.NonceSize {
		return envelope{}, fmt.Errorf("%w: key or nonce length", ErrSealed)
	}
	return env, nil
}

// RandomSeal seals to recipient with crypto/rand.
func RandomSeal(recipient, plaintext []byte) ([]byte, error) {
	return Seal(recipient, plaintext, rand.Reader)
}
