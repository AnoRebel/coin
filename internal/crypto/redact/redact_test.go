package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestFormattingAndLoggingRedact(t *testing.T) {
	secret := New([]byte("super-secret-key-material"))
	if got := fmt.Sprintf("%v", secret); got != "[REDACTED]" {
		t.Fatalf("%%v = %q", got)
	}
	if got := fmt.Sprintf("%#v", secret); got != "[REDACTED]" {
		t.Fatalf("%%#v = %q", got)
	}
	if got := fmt.Sprintf("%s", secret); got != "[REDACTED]" {
		t.Fatalf("%%s = %q", got)
	}
	type holder struct{ Key Secret }
	if got := fmt.Sprintf("%+v", holder{Key: secret}); strings.Contains(got, "super-secret") {
		t.Fatalf("struct printed the secret: %q", got)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("signing", "key", secret, "holder", holder{Key: secret})
	if strings.Contains(buf.String(), "super-secret") {
		t.Fatalf("log leaked the secret: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "[REDACTED]") {
		t.Fatalf("log did not mark the redaction: %s", buf.String())
	}
}

func TestJSONRefusesSecrets(t *testing.T) {
	secret := New([]byte("super-secret"))
	if _, err := json.Marshal(secret); !errors.Is(err, ErrRedacted) {
		t.Fatalf("marshal = %v", err)
	}
	if _, err := json.Marshal(struct{ Key Secret }{Key: secret}); !errors.Is(err, ErrRedacted) {
		t.Fatalf("struct marshal = %v", err)
	}
}

func TestBytesCopyAndZero(t *testing.T) {
	raw := []byte("secret")
	secret := New(raw)
	raw[0] = 'X'
	if !secret.EqualBytes([]byte("secret")) {
		t.Fatal("New did not copy")
	}
	out := secret.Bytes()
	out[0] = 'Y'
	if !secret.EqualBytes([]byte("secret")) {
		t.Fatal("Bytes did not copy")
	}
	secret.Zero()
	if secret.Len() != 6 || !secret.EqualBytes([]byte{0, 0, 0, 0, 0, 0}) {
		t.Fatalf("zero = %x", secret.Bytes())
	}
}

func TestEqualConstantTime(t *testing.T) {
	a := New([]byte("same"))
	if !Equal(a, New([]byte("same"))) {
		t.Fatal("equal secrets differ")
	}
	if Equal(a, New([]byte("samf"))) {
		t.Fatal("different secrets match")
	}
	if Equal(a, New([]byte("same-longer"))) {
		t.Fatal("different lengths match")
	}
	// Every single-byte difference must be detected; the comparison itself is
	// crypto/subtle.ConstantTimeCompare.
	for i := 0; i < a.Len(); i++ {
		b := a.Bytes()
		b[i] ^= 0xff
		if a.EqualBytes(b) {
			t.Fatalf("difference at byte %d not detected", i)
		}
	}
}
