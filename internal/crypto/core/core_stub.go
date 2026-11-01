//go:build !cryptocore

package core

// SHA256 is unavailable without the cryptocore build tag.
func SHA256([]byte) ([32]byte, error) { return [32]byte{}, ErrUnavailable }

// BLAKE3 is unavailable without the cryptocore build tag.
func BLAKE3([]byte) ([32]byte, error) { return [32]byte{}, ErrUnavailable }

// Ed25519PublicKey is unavailable without the cryptocore build tag.
func Ed25519PublicKey([]byte) ([]byte, error) { return nil, ErrUnavailable }

// Ed25519Sign is unavailable without the cryptocore build tag.
func Ed25519Sign(_, _ []byte) ([]byte, error) { return nil, ErrUnavailable }

// Ed25519Verify is unavailable without the cryptocore build tag.
func Ed25519Verify(_, _, _ []byte) (bool, error) { return false, ErrUnavailable }

// BLSSecretFromSeed is unavailable without the cryptocore build tag.
func BLSSecretFromSeed([]byte) ([]byte, error) { return nil, ErrUnavailable }

// BLSPublicKey is unavailable without the cryptocore build tag.
func BLSPublicKey([]byte) ([]byte, error) { return nil, ErrUnavailable }

// BLSSign is unavailable without the cryptocore build tag.
func BLSSign(_, _ []byte) ([]byte, error) { return nil, ErrUnavailable }

// BLSVerify is unavailable without the cryptocore build tag.
func BLSVerify(_, _, _ []byte) (bool, error) { return false, ErrUnavailable }

// BLSAggregate is unavailable without the cryptocore build tag.
func BLSAggregate([][]byte) ([]byte, error) { return nil, ErrUnavailable }

// BLSAggregateVerify is unavailable without the cryptocore build tag.
func BLSAggregateVerify(_ [][]byte, _, _ []byte) (bool, error) { return false, ErrUnavailable }

// MLDSAPublicKey is unavailable without the cryptocore build tag.
func MLDSAPublicKey([]byte) ([]byte, error) { return nil, ErrUnavailable }

// MLDSASign is unavailable without the cryptocore build tag.
func MLDSASign(_, _, _ []byte) ([]byte, error) { return nil, ErrUnavailable }

// MLDSAVerify is unavailable without the cryptocore build tag.
func MLDSAVerify(_, _, _, _ []byte) (bool, error) { return false, ErrUnavailable }
