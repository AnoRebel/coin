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

func TestPebbleCommitRecoversAtLastHeight(t *testing.T) {
	dir := t.TempDir()
	p, err := OpenPebble(dir)
	if err != nil {
		t.Fatal(err)
	}
	for h := uint64(1); h <= 2; h++ {
		b := ledger.Block{Height: h, StateRoot: string(rune('a' + h))}
		snap := map[string][]byte{"k": []byte{byte(h)}}
		if err := p.SaveCommit(b, snap); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen as a crash restart: the synced batch is the recovery point.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = OpenPebble(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	last, ok, err := p.LastBlock()
	if err != nil || !ok || last.Height != 2 {
		t.Fatalf("last = %+v %v %v", last, ok, err)
	}
	back, height, ok, err := p.LoadState()
	if err != nil || !ok || height != last.Height || string(back["k"]) != string([]byte{2}) {
		t.Fatalf("state = %v %d %v %v", back, height, ok, err)
	}
}

func TestPebbleTamperedStateRejected(t *testing.T) {
	p, err := OpenPebble(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.SaveCommit(ledger.Block{Height: 1}, map[string][]byte{"k": {1}}); err != nil {
		t.Fatal(err)
	}
	rec, closer, err := p.db.Get(keyState)
	if err != nil {
		t.Fatal(err)
	}
	closer.Close()
	rec[len(rec)-1] ^= 1
	if err := p.db.Set(keyState, rec, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.LoadState(); err == nil {
		t.Fatal("tampered state accepted")
	}
}

func TestPruneRetainsConfiguredWindow(t *testing.T) {
	for name, make := range map[string]func(t *testing.T) Store{
		"memory": func(*testing.T) Store { return NewMemory() },
		"pebble": func(t *testing.T) Store {
			p, err := OpenPebble(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { p.Close() })
			return p
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := make(t)
			for h := uint64(1); h <= 5; h++ {
				b := ledger.Block{Height: h, StateRoot: string(rune('a' + h))}
				if err := s.SaveCommit(b, map[string][]byte{"k": []byte{byte(h)}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Prune(4); err != nil {
				t.Fatal(err)
			}
			for h := uint64(1); h <= 3; h++ {
				if _, ok, err := s.Block(h); err != nil || ok {
					t.Fatalf("block %d survived prune: %v", h, err)
				}
			}
			for h := uint64(4); h <= 5; h++ {
				if _, ok, err := s.Block(h); err != nil || !ok {
					t.Fatalf("block %d pruned early: %v", h, err)
				}
			}
			last, ok, err := s.LastBlock()
			if err != nil || !ok || last.Height != 5 {
				t.Fatalf("last = %+v %v %v", last, ok, err)
			}
			if _, h, ok, err := s.LoadState(); err != nil || !ok || h != 5 {
				t.Fatalf("state = %d %v %v", h, ok, err)
			}
		})
	}
}
