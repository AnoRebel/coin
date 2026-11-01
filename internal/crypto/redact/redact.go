// Package redact keeps secret bytes out of logs, errors, and JSON.
//
// Wrap key material in Secret and pass it around; formatting, logging, and JSON
// encoding all redact it. Read the bytes only where they are needed.
package redact

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
)

// ErrRedacted is returned when a secret is encoded as JSON.
var ErrRedacted = errors.New("redact: refusing to serialize secret material")

// Secret is a byte slice that never prints its contents.
type Secret struct {
	b []byte
}

// New copies b into a Secret.
func New(b []byte) Secret { return Secret{b: append([]byte(nil), b...)} }

// Bytes returns a copy of the secret.
func (s Secret) Bytes() []byte { return append([]byte(nil), s.b...) }

// Len returns the secret length without exposing it.
func (s Secret) Len() int { return len(s.b) }

// Zero overwrites the secret in place.
func (s Secret) Zero() {
	for i := range s.b {
		s.b[i] = 0
	}
}

// String redacts the secret.
func (s Secret) String() string { return "[REDACTED]" }

// GoString redacts the secret for %#v.
func (s Secret) GoString() string { return "[REDACTED]" }

// LogValue redacts the secret for log/slog.
func (s Secret) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// MarshalJSON refuses to encode secret material.
func (s Secret) MarshalJSON() ([]byte, error) { return nil, ErrRedacted }

// Equal compares two secrets in constant time.
func Equal(a, b Secret) bool {
	if len(a.b) != len(b.b) {
		return false
	}
	return subtle.ConstantTimeCompare(a.b, b.b) == 1
}

// EqualBytes compares a secret with plain bytes in constant time.
func (s Secret) EqualBytes(b []byte) bool {
	if len(s.b) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(s.b, b) == 1
}

// compile-time check that Secret is a json.Marshaler.
var _ json.Marshaler = Secret{}
