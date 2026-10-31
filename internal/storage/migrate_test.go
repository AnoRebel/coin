package storage

import (
	"errors"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/cockroachdb/pebble"
)

func openTestPebble(t *testing.T) *Pebble {
	t.Helper()
	p, err := OpenPebble(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestSchemaMigrateForwardIdempotentAndRollback(t *testing.T) {
	p := openTestPebble(t)
	if v, err := p.SchemaVersion(); err != nil || v != 0 {
		t.Fatalf("initial = %d, %v", v, err)
	}
	if err := p.SaveBlock(ledger.Block{Height: 1}); err != nil {
		t.Fatal(err)
	}
	if err := p.Migrate(SchemaVersion); err != nil {
		t.Fatal(err)
	}
	if v, err := p.SchemaVersion(); err != nil || v != SchemaVersion {
		t.Fatalf("after forward = %d, %v", v, err)
	}
	if err := p.Migrate(SchemaVersion); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if _, ok, err := p.Block(1); err != nil || !ok {
		t.Fatalf("block after migrate: ok=%v err=%v", ok, err)
	}

	if err := p.Migrate(0); err != nil {
		t.Fatal(err)
	}
	if v, _ := p.SchemaVersion(); v != 0 {
		t.Fatalf("after rollback = %d", v)
	}
	if err := p.Migrate(0); err != nil {
		t.Fatalf("second rollback: %v", err)
	}
	if _, ok, err := p.Block(1); err != nil || !ok {
		t.Fatalf("block after rollback: ok=%v err=%v", ok, err)
	}
}

func TestSchemaMigrationFailureLeavesVersionAndData(t *testing.T) {
	p := openTestPebble(t)
	if err := p.Migrate(1); err != nil {
		t.Fatal(err)
	}
	marker := []byte("partial")
	boom := errors.New("boom")
	failing := []Migration{
		{Version: 1, Name: "fails-down", Up: noopMigration, Down: func(b *pebble.Batch) error {
			if err := b.Set(marker, []byte("written"), nil); err != nil {
				return err
			}
			return boom
		}},
	}
	if err := migrate(p.db, 0, failing); !errors.Is(err, boom) {
		t.Fatalf("rollback = %v", err)
	}
	if v, _ := p.SchemaVersion(); v != 1 {
		t.Fatalf("version after failure = %d", v)
	}
	if _, closer, err := p.db.Get(marker); !errors.Is(err, pebble.ErrNotFound) {
		if closer != nil {
			closer.Close()
		}
		t.Fatalf("partial write survived: err=%v", err)
	}

	noDown := []Migration{{Version: 1, Name: "no-down", Up: noopMigration}}
	if err := migrate(p.db, 0, noDown); !errors.Is(err, ErrSchemaNoDown) {
		t.Fatalf("missing rollback = %v", err)
	}
	empty := openTestPebble(t)
	if err := migrate(empty.db, 1, nil); !errors.Is(err, ErrSchemaTarget) {
		t.Fatalf("missing forward step = %v", err)
	}
	if v, _ := empty.SchemaVersion(); v != 0 {
		t.Fatalf("version after missing step = %d", v)
	}
}

func TestSchemaRefusesNewerAndUnknown(t *testing.T) {
	p := openTestPebble(t)
	raw := []byte("2")
	if err := p.db.Set(keySchema, raw, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := p.Migrate(SchemaVersion); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("newer store = %v", err)
	}
	if err := p.Migrate(SchemaVersion + 1); !errors.Is(err, ErrSchemaTarget) {
		t.Fatalf("future target = %v", err)
	}
	if v, _ := p.SchemaVersion(); v != 2 {
		t.Fatalf("version changed: %d", v)
	}
}
