// Package core binds the shared Rust cryptography core through cgo.
//
// The bindings are behind the `cryptocore` build tag so a checkout without a
// Rust toolchain still builds. Build the library first:
//
//	cd crypto/core && cargo build --release
//	go test -tags cryptocore ./internal/crypto/core/
//
// Every primitive is deterministic and checked against spec/vectors.
package core

import "errors"

// ErrUnavailable is returned when the package was built without the cryptocore
// tag or the shared library.
var ErrUnavailable = errors.New("crypto core: bindings not built (use -tags cryptocore)")

// Sizes of the encoded values.
const (
	SHA256Len           = 32
	BLAKE3Len           = 32
	Ed25519SeedLen      = 32
	Ed25519PublicLen    = 32
	Ed25519SignatureLen = 64
	BLSSecretLen        = 32
	BLSPublicLen        = 48
	BLSSignatureLen     = 96
	MLDSASeedLen        = 32
	MLDSAPublicLen      = 1952
	MLDSASignatureLen   = 3309
)
