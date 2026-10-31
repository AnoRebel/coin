package snapshot

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

const (
	testNetwork = "arne:devnet"
	testChain   = "arne-devnet-1"
)

func seedStore(t *testing.T, blocks int) (storage.Store, *ledger.State) {
	t.Helper()
	kp, _, err := protocol.NewKeyPair(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	params := ledger.DefaultParams()
	state, err := ledger.NewGenesis(ledger.Genesis{
		Network: testNetwork, Chain: testChain, Time: 1_700_000_000, Params: params,
		Allocations: []ledger.Allocation{{
			Address: kp.Address, Asset: protocol.NativeAsset(testNetwork), Amount: 10_000_000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.OpenPebble(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for h := uint64(1); h <= uint64(blocks); h++ {
		b, _, err := state.ApplyOrderedBlock(nil, 1_700_000_000+h, h, kp.Address)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCommit(b, state.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
	return store, state
}

func TestFullSnapshotRestoreAndTamperRejection(t *testing.T) {
	store, _ := seedStore(t, 3)
	f, err := Create(store, Options{Network: testNetwork, Chain: testChain})
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != KindFull || f.Height != 3 || len(f.Blocks) != 3 {
		t.Fatalf("snapshot = %s height %d blocks %d", f.Kind, f.Height, len(f.Blocks))
	}
	if err := Verify(f, nil); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "snap.json.gz")
	if err := WriteFile(path, f); err != nil {
		t.Fatal(err)
	}
	read, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(read, nil); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "restored")
	if err := Restore(read, nil, dest); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.OpenPebble(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	last, ok, err := restored.LastBlock()
	if err != nil || !ok {
		t.Fatalf("last block: ok=%v err=%v", ok, err)
	}
	if last.Height != 3 || last.StateRoot != f.StateRoot {
		t.Fatalf("restored block %d root %s", last.Height, last.StateRoot)
	}
	if err := Restore(read, nil, dest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restore into existing directory = %v", err)
	}

	tampered := *read
	tampered.Digest = strings.Repeat("0", 64)
	if err := Verify(&tampered, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("digest tamper = %v", err)
	}

	stateTamper := *read
	stateTamper.State = make(map[string]string, len(read.State))
	for k, v := range read.State {
		stateTamper.State[k] = v
	}
	for k := range stateTamper.State {
		stateTamper.State[k] = "00"
		break
	}
	if err := stamp(&stateTamper); err != nil {
		t.Fatal(err)
	}
	if err := Verify(&stateTamper, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("state tamper = %v", err)
	}

	blockTamper := *read
	blockTamper.Blocks = append([]ledger.Block(nil), read.Blocks...)
	blockTamper.Blocks[1].Height = 9
	if err := stamp(&blockTamper); err != nil {
		t.Fatal(err)
	}
	if err := Verify(&blockTamper, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("block tamper = %v", err)
	}
}

func TestIncrementalSnapshotChainsFromBase(t *testing.T) {
	store, state := seedStore(t, 2)
	base, err := Create(store, Options{Network: testNetwork, Chain: testChain})
	if err != nil {
		t.Fatal(err)
	}
	for h := uint64(3); h <= 4; h++ {
		b, _, err := state.ApplyOrderedBlock(nil, 1_700_000_000+h, h, "proposer")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCommit(b, state.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
	inc, err := Create(store, Options{Network: testNetwork, Chain: testChain, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if inc.Kind != KindIncremental || inc.BaseHeight != 2 || len(inc.Blocks) != 2 || inc.Height != 4 {
		t.Fatalf("incremental = %+v", inc)
	}
	if err := Verify(inc, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incremental without base = %v", err)
	}
	if err := Verify(inc, base); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "from-incremental")
	if err := Restore(inc, nil, dest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("restore without base = %v", err)
	}
	if err := Restore(inc, base, dest); err != nil {
		t.Fatal(err)
	}
	restored, err := storage.OpenPebble(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	last, ok, err := restored.LastBlock()
	if err != nil || !ok || last.Height != 4 || last.StateRoot != inc.StateRoot {
		t.Fatalf("restored = %+v ok=%v err=%v", last, ok, err)
	}
}

func TestCreateRefusesStaleBaseAndMissingBlocks(t *testing.T) {
	store, _ := seedStore(t, 2)
	base, err := Create(store, Options{Network: testNetwork, Chain: testChain})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(store, Options{Network: testNetwork, Chain: testChain, Base: base}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stale base = %v", err)
	}
	if _, err := Create(store, Options{Network: "arne:devnet", Chain: "wrong"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong chain = %v", err)
	}
	empty, err := storage.OpenPebble(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if _, err := Create(empty, Options{Network: testNetwork, Chain: testChain}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty store = %v", err)
	}
}
