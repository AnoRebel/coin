// Package bls wraps BLS12-381 signatures with the proof-of-possession scheme.
//
// Validator keys must register a proof of possession before they can take part
// in aggregate verification. Without that check a rogue key can be chosen to
// cancel another key in an aggregate; the registry rejects such a key because
// the attacker cannot produce a PoP for it.
package bls

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	blst "github.com/supranational/blst/bindings/go"
)

// DST is the proof-of-possession ciphersuite domain separation tag. It matches
// the standard BLS signature draft so other implementations interoperate.
const DST = "BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_"

const popDomain = "arne-bls-pop-v1"

// PublicKey is a compressed G1 point (48 bytes).
type PublicKey []byte

// Signature is a compressed G2 point (96 bytes).
type Signature []byte

var (
	ErrInvalid   = errors.New("bls: invalid key, signature, or input")
	ErrNoPoP     = errors.New("bls: validator has no registered proof of possession")
	ErrRogueKey  = errors.New("bls: proof of possession does not match the public key")
	ErrDuplicate = errors.New("bls: validator already registered")
)

// SecretFromSeed derives a BLS secret key from a wallet seed. The digest is
// rehashed until it is a valid scalar, which is what both implementations do.
func SecretFromSeed(seed []byte) ([]byte, error) {
	if len(seed) == 0 {
		return nil, fmt.Errorf("%w: seed is required", ErrInvalid)
	}
	h := sha256.Sum256(append([]byte("arne-bls-key-v1"), seed...))
	for range 256 {
		var sk blst.SecretKey
		if sk.Deserialize(h[:]) != nil && sk.Valid() {
			return sk.Serialize(), nil
		}
		h = sha256.Sum256(h[:])
	}
	return nil, fmt.Errorf("%w: could not derive a scalar", ErrInvalid)
}

func secretKey(secret []byte) (*blst.SecretKey, error) {
	if len(secret) != 32 {
		return nil, fmt.Errorf("%w: secret key length", ErrInvalid)
	}
	var sk blst.SecretKey
	if sk.Deserialize(secret) == nil || !sk.Valid() {
		return nil, fmt.Errorf("%w: secret key", ErrInvalid)
	}
	return &sk, nil
}

// PublicKeyOf returns the compressed public key for a secret key.
func PublicKeyOf(secret []byte) (PublicKey, error) {
	sk, err := secretKey(secret)
	if err != nil {
		return nil, err
	}
	return PublicKey(new(blst.P1Affine).From(sk).Compress()), nil
}

// Sign signs msg with the proof-of-possession ciphersuite.
func Sign(secret, msg []byte) (Signature, error) {
	sk, err := secretKey(secret)
	if err != nil {
		return nil, err
	}
	if len(msg) == 0 {
		return nil, fmt.Errorf("%w: empty message", ErrInvalid)
	}
	return Signature(new(blst.P2Affine).Sign(sk, msg, []byte(DST)).Compress()), nil
}

// Verify checks one signature.
func Verify(pub PublicKey, msg []byte, sig Signature) bool {
	pk := new(blst.P1Affine).Uncompress(pub)
	s := new(blst.P2Affine).Uncompress(sig)
	if pk == nil || s == nil || len(msg) == 0 {
		return false
	}
	return s.Verify(true, pk, true, msg, []byte(DST))
}

// Aggregate combines signatures. An empty list is rejected.
func Aggregate(sigs []Signature) (Signature, error) {
	if len(sigs) == 0 {
		return nil, fmt.Errorf("%w: no signatures", ErrInvalid)
	}
	agg := new(blst.P2Aggregate)
	for _, sig := range sigs {
		s := new(blst.P2Affine).Uncompress(sig)
		if s == nil || !agg.Add(s, true) {
			return nil, fmt.Errorf("%w: signature", ErrInvalid)
		}
	}
	return Signature(agg.ToAffine().Compress()), nil
}

// VerifyAggregate verifies one message signed by several keys. Callers that
// accept keys from others MUST check proof of possession first; this function
// only performs the curve math.
func VerifyAggregate(pubs []PublicKey, msg []byte, sig Signature) bool {
	if len(pubs) == 0 || len(msg) == 0 {
		return false
	}
	agg := new(blst.P1Aggregate)
	for _, pub := range pubs {
		pk := new(blst.P1Affine).Uncompress(pub)
		if pk == nil || !agg.Add(pk, true) {
			return false
		}
	}
	s := new(blst.P2Affine).Uncompress(sig)
	if s == nil {
		return false
	}
	return s.Verify(true, agg.ToAffine(), true, msg, []byte(DST))
}

// PopMessage binds a proof of possession to the network, validator ID, and key.
func PopMessage(network, validator string, pub PublicKey) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte(popDomain))
	for _, part := range [][]byte{[]byte(network), []byte(validator), pub} {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

// SignPoP produces the proof of possession for a validator.
func SignPoP(secret []byte, network, validator string) (Signature, error) {
	pub, err := PublicKeyOf(secret)
	if err != nil {
		return nil, err
	}
	return Sign(secret, PopMessage(network, validator, pub))
}

// Registry records validator keys that proved possession of their secret.
type Registry struct {
	mu      sync.RWMutex
	network string
	keys    map[string]Entry
}

// Entry is one registered validator key.
type Entry struct {
	PublicKey PublicKey `json:"public_key"`
	PoP       Signature `json:"pop"`
}

// NewRegistry returns an empty registry bound to one network.
func NewRegistry(network string) (*Registry, error) {
	if network == "" {
		return nil, fmt.Errorf("%w: network is required", ErrInvalid)
	}
	return &Registry{network: network, keys: map[string]Entry{}}, nil
}

// Register verifies the proof of possession before recording the key.
func (r *Registry) Register(validator string, pub PublicKey, pop Signature) error {
	if validator == "" || len(pub) == 0 || len(pop) == 0 {
		return fmt.Errorf("%w: validator, public key, and PoP are required", ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.keys[validator]; dup {
		return fmt.Errorf("%w: %q", ErrDuplicate, validator)
	}
	if !Verify(pub, PopMessage(r.network, validator, pub), pop) {
		return fmt.Errorf("%w: %q", ErrRogueKey, validator)
	}
	r.keys[validator] = Entry{PublicKey: append(PublicKey(nil), pub...), PoP: append(Signature(nil), pop...)}
	return nil
}

// PublicKey returns a registered key.
func (r *Registry) PublicKey(validator string) (PublicKey, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.keys[validator]
	return e.PublicKey, ok
}

// Verify checks a signature from a registered validator.
func (r *Registry) Verify(validator string, msg []byte, sig Signature) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.keys[validator]
	if !ok {
		return false
	}
	return Verify(e.PublicKey, msg, sig)
}

// VerifyAggregate verifies an aggregate over registered validators. Every ID
// must have a registered PoP, so a rogue key cannot be slipped into the set.
func (r *Registry) VerifyAggregate(validators []string, msg []byte, sig Signature) error {
	if len(validators) == 0 {
		return fmt.Errorf("%w: no validators", ErrInvalid)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]bool{}
	pubs := make([]PublicKey, 0, len(validators))
	for _, id := range validators {
		if seen[id] {
			return fmt.Errorf("%w: duplicate validator %q", ErrInvalid, id)
		}
		seen[id] = true
		e, ok := r.keys[id]
		if !ok {
			return fmt.Errorf("%w: %q", ErrNoPoP, id)
		}
		pubs = append(pubs, e.PublicKey)
	}
	if !VerifyAggregate(pubs, msg, sig) {
		return fmt.Errorf("%w: aggregate signature", ErrInvalid)
	}
	return nil
}
