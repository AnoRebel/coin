package protocol

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestCanonicalCBORVectors(t *testing.T) {
	cases := []struct {
		name string
		in   any
		hex  string
	}{
		{"nil", nil, "f6"},
		{"true", true, "f5"},
		{"zero", uint64(0), "00"},
		{"23", uint64(23), "17"},
		{"24", uint64(24), "1818"},
		{"255", uint64(255), "18ff"},
		{"256", uint64(256), "190100"},
		{"neg1", int64(-1), "20"},
		{"text", "arce", "6461726365"},
		{"bytes", []byte{1, 2}, "420102"},
		{"array", []any{uint64(1), "a"}, "82016161"},
		{"map-sort-length-first", map[string]any{"aa": uint64(2), "b": uint64(1)}, "a261620162616102"},
		{"map-nested", map[string]any{"k": []any{true, nil}}, "a1616b82f5f6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := hex.DecodeString(tc.hex)
			if !bytes.Equal(got, want) {
				t.Fatalf("encode = %x, want %s", got, tc.hex)
			}
			back, err := Decode(want)
			if err != nil {
				t.Fatal(err)
			}
			// Round-trip must be byte-identical.
			again, err := Encode(back)
			if err != nil || !bytes.Equal(again, want) {
				t.Fatalf("round-trip = %x (%v)", again, err)
			}
		})
	}
}

func TestRejectNonCanonical(t *testing.T) {
	bad := []string{
		"1800",             // non-shortest zero
		"1f",               // indefinite array
		"5f",               // indefinite map
		"a2616101616102",   // duplicate key
		"a262616102616201", // map keys not in canonical order
		"fa00000000",       // float
		"c000",             // tag
		"8181",             // truncated
	}
	for _, h := range bad {
		b, _ := hex.DecodeString(h)
		if _, err := Decode(b); err == nil {
			t.Errorf("Decode(%s) accepted non-canonical input", h)
		}
	}
}

func TestEncodeUnsupported(t *testing.T) {
	if _, err := Encode(map[string]any{"f": 1.5}); err == nil {
		t.Fatal("floats must be rejected")
	}
}
