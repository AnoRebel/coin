package main

import (
	"strings"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

func TestMigrateCLI(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.OpenPebble(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBlock(ledger.Block{Height: 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) error {
		cmd := migrateCmd()
		cmd.SetArgs(append([]string{"--data-dir", dir}, args...))
		cmd.SetOut(new(strings.Builder))
		return cmd.Execute()
	}
	version := func() uint32 {
		s, err := storage.OpenPebble(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		v, err := s.SchemaVersion()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	if got := version(); got != 0 {
		t.Fatalf("initial schema = %d", got)
	}
	if err := run("--status"); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != storage.SchemaVersion {
		t.Fatalf("after migrate = %d", got)
	}
	if err := run(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := run("--to", "2"); err == nil {
		t.Fatal("future target accepted")
	}
	if err := run("--to", "0"); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != 0 {
		t.Fatalf("after rollback = %d", got)
	}

	s, err := storage.OpenPebble(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(storage.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Block(1); err != nil || !ok {
		t.Fatalf("block after rollback and re-migrate: ok=%v err=%v", ok, err)
	}
}
