package main

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

func seedSnapshotStore(t *testing.T, dir string, blocks int) *ledger.State {
	t.Helper()
	kp, _, err := protocol.NewKeyPair("arne:devnet")
	if err != nil {
		t.Fatal(err)
	}
	state, err := ledger.NewGenesis(ledger.Genesis{
		Network: "arne:devnet", Chain: "arne-devnet-1", Time: 1_700_000_000,
		Params: ledger.DefaultParams(),
		Allocations: []ledger.Allocation{{
			Address: kp.Address, Asset: protocol.NativeAsset("arne:devnet"), Amount: 10_000_000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.OpenPebble(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for h := uint64(1); h <= uint64(blocks); h++ {
		b, _, err := state.ApplyOrderedBlock(nil, 1_700_000_000+h, h, kp.Address)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCommit(b, state.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func runSnapshot(t *testing.T, args ...string) error {
	t.Helper()
	cmd := snapshotCmd()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

func TestSnapshotCLIFullAndIncremental(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	state := seedSnapshotStore(t, data, 2)
	full := filepath.Join(root, "full.snap")
	if err := runSnapshot(t, "create", "--data-dir", data, "--out", full); err != nil {
		t.Fatal(err)
	}
	if err := runSnapshot(t, "verify", "--in", full); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored-full")
	if err := runSnapshot(t, "restore", "--in", full, "--data-dir", restored); err != nil {
		t.Fatal(err)
	}

	store, err := storage.OpenPebble(data)
	if err != nil {
		t.Fatal(err)
	}
	for h := uint64(3); h <= 4; h++ {
		b, _, err := state.ApplyOrderedBlock(nil, 1_700_000_000+h, h, "")
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		if err := store.SaveCommit(b, state.Snapshot()); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	inc := filepath.Join(root, "inc.snap")
	if err := runSnapshot(t, "create", "--data-dir", data, "--out", inc, "--base", full); err != nil {
		t.Fatal(err)
	}
	if err := runSnapshot(t, "verify", "--in", inc); err == nil {
		t.Fatal("incremental verified without its base")
	}
	if err := runSnapshot(t, "verify", "--in", inc, "--base", full); err != nil {
		t.Fatal(err)
	}
	restoredInc := filepath.Join(root, "restored-inc")
	if err := runSnapshot(t, "restore", "--in", inc, "--base", full, "--data-dir", restoredInc); err != nil {
		t.Fatal(err)
	}
	check, err := storage.OpenPebble(restoredInc)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	last, ok, err := check.LastBlock()
	if err != nil || !ok || last.Height != 4 {
		t.Fatalf("restored incremental: %+v ok=%v err=%v", last, ok, err)
	}
}
