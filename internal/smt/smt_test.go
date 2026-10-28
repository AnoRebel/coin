package smt

import (
	"bytes"
	"testing"
)

func TestEmptyRoot(t *testing.T) {
	if New().Root() != Empty {
		t.Fatal("empty root must be zero")
	}
}

func TestInsertOrderIndependent(t *testing.T) {
	a := New()
	a.Set("account/x/nonce", []byte{0, 0, 0, 0, 0, 0, 0, 1})
	a.Set("balance/x/arce:devnet", []byte{1, 2, 3})
	a.Set("asset/arce:devnet", []byte("arce"))

	b := New()
	b.Set("asset/arce:devnet", []byte("arce"))
	b.Set("balance/x/arce:devnet", []byte{1, 2, 3})
	b.Set("account/x/nonce", []byte{0, 0, 0, 0, 0, 0, 0, 1})

	if a.Root() != b.Root() {
		t.Fatal("root depends on insertion order")
	}
	if a.Root() == Empty {
		t.Fatal("non-empty root is zero")
	}
}

func TestInclusionAndExclusionProofs(t *testing.T) {
	tr := New()
	tr.Set("a", []byte("1"))
	tr.Set("b", []byte("2"))
	tr.Set("c", []byte("3"))
	root := tr.Root()

	for _, k := range []string{"a", "b", "c"} {
		p := tr.Prove(k)
		if err := p.Verify(root); err != nil {
			t.Fatalf("proof for %s: %v", k, err)
		}
		if !bytes.Equal(p.Value, tr.Snapshot()[k]) {
			t.Fatalf("proof value mismatch for %s", k)
		}
	}
	ex := tr.Prove("missing")
	if ex.Value != nil {
		t.Fatal("exclusion proof carries a value")
	}
	if err := ex.Verify(root); err != nil {
		t.Fatalf("exclusion proof: %v", err)
	}
	// Wrong root fails.
	root[0] ^= 1
	if err := tr.Prove("a").Verify(root); err == nil {
		t.Fatal("proof verified against wrong root")
	}
}

func TestDeleteChangesRoot(t *testing.T) {
	tr := New()
	tr.Set("a", []byte("1"))
	r1 := tr.Root()
	tr.Delete("a")
	if tr.Root() == r1 {
		t.Fatal("delete did not change root")
	}
	if tr.Root() != Empty {
		t.Fatal("deleting the only key must return the empty root")
	}
}
