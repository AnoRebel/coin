package storage

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble"
)

// SchemaVersion is the newest on-disk schema this binary understands.
const SchemaVersion uint32 = 1

var (
	ErrSchemaNewer  = errors.New("storage: on-disk schema is newer than this binary")
	ErrSchemaTarget = errors.New("storage: unsupported schema target")
	ErrSchemaNoDown = errors.New("storage: migration has no rollback")
)

var keySchema = []byte("meta/schema")

// Migration moves the store by one schema step. Up must be safe to replay
// after a crash before the version commit.
type Migration struct {
	Version uint32
	Name    string
	Up      func(*pebble.Batch) error
	Down    func(*pebble.Batch) error
}

func noopMigration(*pebble.Batch) error { return nil }

var schemaMigrations = []Migration{
	// Version 1 adopts the version marker. Records written before it already
	// use the v1 JSON layout, so no data changes.
	{Version: 1, Name: "baseline-version-marker", Up: noopMigration, Down: noopMigration},
}

// SchemaVersion reports the stored schema. An unversioned store is 0.
func (p *Pebble) SchemaVersion() (uint32, error) {
	return readSchemaVersion(p.db)
}

func readSchemaVersion(db *pebble.DB) (uint32, error) {
	raw, closer, err := db.Get(keySchema)
	if errors.Is(err, pebble.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	var v uint32
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("%w: schema version: %v", ErrCorrupt, err)
	}
	return v, nil
}

// Migrate moves the store to target. Each step commits its data change and the
// new version in one synced batch, and a second call at the same version is a
// no-op. Targets above this binary are refused.
func (p *Pebble) Migrate(target uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return migrate(p.db, target, schemaMigrations)
}

func migrate(db *pebble.DB, target uint32, list []Migration) error {
	if target > SchemaVersion {
		return fmt.Errorf("%w: target %d above binary %d", ErrSchemaTarget, target, SchemaVersion)
	}
	current, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if current > SchemaVersion {
		return fmt.Errorf("%w: store %d, binary %d", ErrSchemaNewer, current, SchemaVersion)
	}
	if current == target {
		return nil
	}
	if current < target {
		for _, m := range list {
			if m.Version <= current || m.Version > target {
				continue
			}
			if m.Up == nil {
				return fmt.Errorf("%w: %s", ErrSchemaTarget, m.Name)
			}
			if err := applyMigration(db, m.Up, m.Version); err != nil {
				return fmt.Errorf("migrate %d %s: %w", m.Version, m.Name, err)
			}
		}
	} else {
		for i := len(list) - 1; i >= 0; i-- {
			m := list[i]
			if m.Version > current || m.Version <= target {
				continue
			}
			if m.Down == nil {
				return fmt.Errorf("%w: %s", ErrSchemaNoDown, m.Name)
			}
			if err := applyMigration(db, m.Down, m.Version-1); err != nil {
				return fmt.Errorf("rollback %d %s: %w", m.Version, m.Name, err)
			}
		}
	}
	now, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	if now != target {
		return fmt.Errorf("%w: stopped at %d", ErrSchemaTarget, now)
	}
	return nil
}

func applyMigration(db *pebble.DB, fn func(*pebble.Batch) error, version uint32) error {
	raw, err := json.Marshal(version)
	if err != nil {
		return err
	}
	batch := db.NewBatch()
	defer batch.Close()
	if err := fn(batch); err != nil {
		return err
	}
	if err := batch.Set(keySchema, raw, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}
