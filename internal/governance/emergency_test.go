package governance

import (
	"errors"
	"testing"
)

func TestNewEmergencyValidation(t *testing.T) {
	for name, test := range map[string]struct {
		members   []string
		threshold int
		period    uint64
	}{
		"one member":     {[]string{"a"}, 2, 100},
		"duplicate":      {[]string{"a", "a"}, 2, 100},
		"low threshold":  {[]string{"a", "b"}, 1, 100},
		"high threshold": {[]string{"a", "b"}, 3, 100},
		"no period":      {[]string{"a", "b"}, 2, 0},
	} {
		if _, err := NewEmergency(test.members, test.threshold, test.period); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestEmergencyIDCanonical(t *testing.T) {
	a := EmergencyID("pause", "", []string{"transfers", "all"}, []string{"b", "a"}, "incident", 10)
	b := EmergencyID("pause", "", []string{"all", "transfers"}, []string{"a", "b"}, "incident", 10)
	if a != b {
		t.Fatalf("not canonical: %s != %s", a, b)
	}
	if len(a) != 32 {
		t.Fatalf("id %q", a)
	}
}
