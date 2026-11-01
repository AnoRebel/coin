//go:build cryptocore

package core

/*
#cgo CFLAGS: -I${SRCDIR}/../../../crypto/core
#cgo LDFLAGS: ${SRCDIR}/../../../crypto/core/target/release/libarne_crypto.a -lpthread -ldl -lm
#include "arne_crypto.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

func status(code C.int) error {
	switch code {
	case 0:
		return nil
	case 1:
		return errors.New("crypto core: invalid input")
	case 2:
		return errors.New("crypto core: invalid key")
	case 3:
		return errors.New("crypto core: invalid signature")
	default:
		return fmt.Errorf("crypto core: status %d", int(code))
	}
}

func ptr(b []byte) *C.uchar {
	if len(b) == 0 {
		return nil
	}
	return (*C.uchar)(unsafe.Pointer(&b[0]))
}

// SHA256 returns the SHA-256 digest.
func SHA256(data []byte) ([32]byte, error) {
	var out [32]byte
	err := status(C.arne_sha256(ptr(data), C.size_t(len(data)), (*C.uchar)(unsafe.Pointer(&out[0]))))
	return out, err
}

// BLAKE3 returns the BLAKE3 digest.
func BLAKE3(data []byte) ([32]byte, error) {
	var out [32]byte
	err := status(C.arne_blake3(ptr(data), C.size_t(len(data)), (*C.uchar)(unsafe.Pointer(&out[0]))))
	return out, err
}

// Ed25519PublicKey derives the public key from a 32-byte seed.
func Ed25519PublicKey(seed []byte) ([]byte, error) {
	if len(seed) != Ed25519SeedLen {
		return nil, fmt.Errorf("%w: seed length", ErrUnavailable)
	}
	out := make([]byte, Ed25519PublicLen)
	if err := status(C.arne_ed25519_keypair(ptr(seed), ptr(out))); err != nil {
		return nil, err
	}
	return out, nil
}

// Ed25519Sign signs with the deterministic RFC 8032 scheme.
func Ed25519Sign(seed, message []byte) ([]byte, error) {
	if len(seed) != Ed25519SeedLen {
		return nil, fmt.Errorf("%w: seed length", ErrUnavailable)
	}
	out := make([]byte, Ed25519SignatureLen)
	if err := status(C.arne_ed25519_sign(ptr(seed), ptr(message), C.size_t(len(message)), ptr(out))); err != nil {
		return nil, err
	}
	return out, nil
}

// Ed25519Verify checks a signature. Malformed input is a failed verification.
func Ed25519Verify(publicKey, message, signature []byte) (bool, error) {
	if len(publicKey) != Ed25519PublicLen || len(signature) != Ed25519SignatureLen {
		return false, nil
	}
	return C.arne_ed25519_verify(ptr(publicKey), ptr(message), C.size_t(len(message)), ptr(signature)) == 1, nil
}

// BLSSecretFromSeed derives a BLS secret key from wallet seed material.
func BLSSecretFromSeed(seed []byte) ([]byte, error) {
	out := make([]byte, BLSSecretLen)
	if err := status(C.arne_bls_secret_from_seed(ptr(seed), C.size_t(len(seed)), ptr(out))); err != nil {
		return nil, err
	}
	return out, nil
}

// BLSPublicKey returns the compressed public key for a secret key.
func BLSPublicKey(secret []byte) ([]byte, error) {
	if len(secret) != BLSSecretLen {
		return nil, fmt.Errorf("%w: secret length", ErrUnavailable)
	}
	out := make([]byte, BLSPublicLen)
	if err := status(C.arne_bls_public_key(ptr(secret), ptr(out))); err != nil {
		return nil, err
	}
	return out, nil
}

// BLSSign signs with the proof-of-possession ciphersuite.
func BLSSign(secret, message []byte) ([]byte, error) {
	if len(secret) != BLSSecretLen {
		return nil, fmt.Errorf("%w: secret length", ErrUnavailable)
	}
	out := make([]byte, BLSSignatureLen)
	if err := status(C.arne_bls_sign(ptr(secret), ptr(message), C.size_t(len(message)), ptr(out))); err != nil {
		return nil, err
	}
	return out, nil
}

// BLSVerify checks one signature.
func BLSVerify(publicKey, message, signature []byte) (bool, error) {
	if len(publicKey) != BLSPublicLen || len(signature) != BLSSignatureLen {
		return false, nil
	}
	return C.arne_bls_verify(ptr(publicKey), ptr(message), C.size_t(len(message)), ptr(signature)) == 1, nil
}

// BLSAggregate combines signatures.
func BLSAggregate(signatures [][]byte) ([]byte, error) {
	if len(signatures) == 0 {
		return nil, errors.New("crypto core: no signatures")
	}
	joined := make([]byte, 0, len(signatures)*BLSSignatureLen)
	for _, sig := range signatures {
		if len(sig) != BLSSignatureLen {
			return nil, errors.New("crypto core: invalid signature")
		}
		joined = append(joined, sig...)
	}
	out := make([]byte, BLSSignatureLen)
	if err := status(C.arne_bls_aggregate(ptr(joined), C.size_t(len(signatures)), ptr(out))); err != nil {
		return nil, err
	}
	return out, nil
}

// BLSAggregateVerify checks an aggregate over one message.
func BLSAggregateVerify(publicKeys [][]byte, message, signature []byte) (bool, error) {
	if len(publicKeys) == 0 || len(signature) != BLSSignatureLen {
		return false, nil
	}
	joined := make([]byte, 0, len(publicKeys)*BLSPublicLen)
	for _, key := range publicKeys {
		if len(key) != BLSPublicLen {
			return false, nil
		}
		joined = append(joined, key...)
	}
	return C.arne_bls_aggregate_verify(ptr(joined), C.size_t(len(publicKeys)), ptr(message), C.size_t(len(message)), ptr(signature)) == 1, nil
}

// MLDSAPublicKey derives the ML-DSA-65 public key from a FIPS 204 seed.
func MLDSAPublicKey(seed []byte) ([]byte, error) {
	if len(seed) != MLDSASeedLen {
		return nil, fmt.Errorf("%w: seed length", ErrUnavailable)
	}
	out := make([]byte, MLDSAPublicLen)
	if err := status(C.arne_ml_dsa65_keypair(ptr(seed), ptr(out))); err != nil {
		return nil, err
	}
	return out, nil
}

// MLDSASign signs deterministically under a context string.
func MLDSASign(seed, message, context []byte) ([]byte, error) {
	if len(seed) != MLDSASeedLen {
		return nil, fmt.Errorf("%w: seed length", ErrUnavailable)
	}
	out := make([]byte, MLDSASignatureLen)
	if err := status(C.arne_ml_dsa65_sign(ptr(seed), ptr(message), C.size_t(len(message)), ptr(context), C.size_t(len(context)), ptr(out))); err != nil {
		return nil, err
	}
	return out, nil
}

// MLDSAVerify checks a deterministic signature under a context string.
func MLDSAVerify(publicKey, message, context, signature []byte) (bool, error) {
	if len(publicKey) != MLDSAPublicLen || len(signature) != MLDSASignatureLen {
		return false, nil
	}
	return C.arne_ml_dsa65_verify(ptr(publicKey), ptr(message), C.size_t(len(message)), ptr(context), C.size_t(len(context)), ptr(signature)) == 1, nil
}
