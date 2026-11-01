// Package hybrid combines Ed25519 and ML-DSA-65 into one registration and one
// signature envelope.
//
// Verification requires both components. A missing or invalid component is an
// error, so a caller cannot downgrade a hybrid account to one algorithm by
// stripping a signature.
package hybrid

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

// Context is the ML-DSA domain separation context.
var Context = []byte("arne-hybrid-v1")

var (
	ErrInvalid = errors.New("hybrid: invalid key or signature")
	ErrPartial = errors.New("hybrid: signature is missing a component")
	ErrVerify  = errors.New("hybrid: signature does not verify")
)

// Key is a hybrid public key.
type Key struct {
	Ed25519 []byte `json:"ed25519"`
	MLDSA   []byte `json:"ml_dsa_65"`
}

// Signature carries one signature from each algorithm.
type Signature struct {
	Ed25519 []byte `json:"ed25519"`
	MLDSA   []byte `json:"ml_dsa_65"`
}

// Register validates a hybrid public key. Both components are required.
func Register(k Key) error {
	if len(k.Ed25519) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: ed25519 public key", ErrInvalid)
	}
	if len(k.MLDSA) != mldsa65.PublicKeySize {
		return fmt.Errorf("%w: ml-dsa-65 public key", ErrInvalid)
	}
	return nil
}

// Sign produces a deterministic hybrid signature. Ed25519 is deterministic and
// ML-DSA is used in its deterministic mode, so the same keys and message yield
// the same envelope.
func Sign(edPrivate, mlPrivate, message []byte) (Signature, error) {
	if len(edPrivate) != ed25519.PrivateKeySize {
		return Signature{}, fmt.Errorf("%w: ed25519 private key", ErrInvalid)
	}
	if len(mlPrivate) != mldsa65.PrivateKeySize {
		return Signature{}, fmt.Errorf("%w: ml-dsa-65 private key", ErrInvalid)
	}
	if len(message) == 0 {
		return Signature{}, fmt.Errorf("%w: empty message", ErrInvalid)
	}
	var sk mldsa65.PrivateKey
	if err := sk.UnmarshalBinary(mlPrivate); err != nil {
		return Signature{}, fmt.Errorf("%w: ml-dsa-65 private key", ErrInvalid)
	}
	ml := make([]byte, mldsa65.SignatureSize)
	if err := mldsa65.SignTo(&sk, message, Context, false, ml); err != nil {
		return Signature{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return Signature{
		Ed25519: ed25519.Sign(ed25519.PrivateKey(edPrivate), message),
		MLDSA:   ml,
	}, nil
}

// Verify requires and checks both components.
func Verify(k Key, message []byte, sig Signature) error {
	if err := Register(k); err != nil {
		return err
	}
	if len(message) == 0 {
		return fmt.Errorf("%w: empty message", ErrInvalid)
	}
	if len(sig.Ed25519) == 0 || len(sig.MLDSA) == 0 {
		return ErrPartial
	}
	if len(sig.Ed25519) != ed25519.SignatureSize || len(sig.MLDSA) != mldsa65.SignatureSize {
		return fmt.Errorf("%w: signature size", ErrInvalid)
	}
	if !ed25519.Verify(ed25519.PublicKey(k.Ed25519), message, sig.Ed25519) {
		return fmt.Errorf("%w: ed25519", ErrVerify)
	}
	var pk mldsa65.PublicKey
	if err := pk.UnmarshalBinary(k.MLDSA); err != nil {
		return fmt.Errorf("%w: ml-dsa-65 public key", ErrInvalid)
	}
	if !mldsa65.Verify(&pk, message, Context, sig.MLDSA) {
		return fmt.Errorf("%w: ml-dsa-65", ErrVerify)
	}
	return nil
}
