//go:build !cryptocore

package core

import (
	"errors"
	"testing"
)

func TestStubReportsUnavailable(t *testing.T) {
	if _, err := SHA256([]byte("x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("sha256 = %v", err)
	}
	if _, err := MLDSAVerify(nil, nil, nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ml-dsa verify = %v", err)
	}
}
