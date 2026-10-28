package storage

import (
	"testing"

	"github.com/anorebel/anorebelcoin/internal/ledger"
)

func roundTrip(t *testing.T, s Store) {
	t.Helper()
	blocks := []ledger.Block{
		{Height: 1, Time: 100, Proposer: "p", StateRoot: "aa", TxHashes: []string{"01"}},
		{Height: 2, Time: 200, Proposer: "p", StateRoot: "bb", TxHashes: []string{}},
	}
	for _, b := range blocks {
		if err := s.SaveBlock(b); err != nil {
			t.Fatal(err)
		}
	}
	last, ok, err := s.LastBlock()
	if err != nil || !ok || last.Height != 2 {
		t.Fatalf("last = %+v %v %v", last, ok, err)
	}
	got, ok, err := s.Block(1)
	if err != nil || !ok || got.StateRoot != "aa" {
		t.Fatalf("block 1 = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.Block(99); ok {
		t.Fatal("missing block reported present")
	}
	snap := map[string][]byte{"k": {1, 2, 3}}
	if err := s.SaveState(2, snap); err != nil {
		t.Fatal(err)
	}
	back, h, ok, err := s.LoadState()
	if err != nil || !ok || h != 2 || string(back["k"]) != string(snap["k"]) {
		t.Fatalf("state = %v %d %v %v", back, h, ok, err)
	}
}

func TestMemory(t *testing.T) { roundTrip(t, NewMemory()) }

func TestPebble(t *testing.T) {
	p, err := OpenPebble(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	roundTrip(t, p)
}

func TestTamperedRecordRejected(t *testing.T) {
	p, err := OpenPebble(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SaveBlock(ledger.Block{Height: 1}); err != nil {
		t.Fatal(err)
	}
	rec, closer, err := p.db.Get(blockKey(1))
	if err != nil {
		t.Fatal(err)
	}
	closer.Close()
	rec[len(rec)-1] ^= 1
	if err := p.db.Set(blockKey(1), rec, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Block(1); err == nil {
		t.Fatal("tampered record accepted")
	}
}
