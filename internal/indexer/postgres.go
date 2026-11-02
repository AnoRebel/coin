package indexer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anorebel/anorebelcoin/internal/ledger"
)

const schemaVersion = 1

// Postgres is the durable index target.
type Postgres struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	// ready is set after the schema is installed; a database that is down at
	// startup is retried on the next sync instead of disabling the indexer.
	ready bool
}

// OpenPostgres connects and installs the schema on first use.
func OpenPostgres(_ context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) ensureReady(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ready {
		return nil
	}
	if err := p.ensureSchema(ctx); err != nil {
		return err
	}
	p.ready = true
	return nil
}

func (p *Postgres) ensureSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS indexer_schema (version integer NOT NULL)`,
		`INSERT INTO indexer_schema (version) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM indexer_schema)`,
		`CREATE TABLE IF NOT EXISTS blocks (
			height bigint PRIMARY KEY,
			hash text NOT NULL,
			prev_root text NOT NULL,
			state_root text NOT NULL,
			tx_root text NOT NULL,
			time bigint NOT NULL,
			proposer text NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS transactions (
			hash text PRIMARY KEY,
			height bigint NOT NULL REFERENCES blocks(height) ON DELETE CASCADE,
			position integer NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS transactions_height ON transactions(height)`,
	}
	for _, statement := range statements {
		if _, err := p.pool.Exec(ctx, statement); err != nil {
			return err
		}
	}
	var version int
	if err := p.pool.QueryRow(ctx, `SELECT max(version) FROM indexer_schema`).Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("indexer: schema %d is newer than this binary (%d)", version, schemaVersion)
	}
	return nil
}

// Apply writes one block and its transactions. Reapplying the same block is a
// no-op; a different block at the same height, or a predecessor mismatch,
// deletes the orphaned suffix first.
func (p *Postgres) Apply(ctx context.Context, block ledger.Block) error {
	if err := p.ensureReady(ctx); err != nil {
		return err
	}
	hash, err := block.Hash()
	if err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var orphan uint64
	if block.Height > 1 {
		var prevRoot string
		err := tx.QueryRow(ctx, `SELECT state_root FROM blocks WHERE height = $1`, block.Height-1).Scan(&prevRoot)
		if err == nil && prevRoot != block.PrevRoot {
			orphan = block.Height - 1
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	var stored string
	err = tx.QueryRow(ctx, `SELECT hash FROM blocks WHERE height = $1`, block.Height).Scan(&stored)
	if err == nil && stored != hash {
		if orphan == 0 || block.Height < orphan {
			orphan = block.Height
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if orphan > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM blocks WHERE height >= $1`, orphan); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO blocks (height, hash, prev_root, state_root, tx_root, time, proposer)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (height) DO UPDATE SET
			hash = EXCLUDED.hash, prev_root = EXCLUDED.prev_root,
			state_root = EXCLUDED.state_root, tx_root = EXCLUDED.tx_root,
			time = EXCLUDED.time, proposer = EXCLUDED.proposer`,
		block.Height, hash, block.PrevRoot, block.StateRoot, block.TxRoot, block.Time, block.Proposer,
	); err != nil {
		return err
	}
	for position, txHash := range block.TxHashes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO transactions (hash, height, position) VALUES ($1, $2, $3)
			ON CONFLICT (hash) DO UPDATE SET height = EXCLUDED.height, position = EXCLUDED.position`,
			txHash, block.Height, position,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// IndexedHeight returns the highest indexed height, or 0.
func (p *Postgres) IndexedHeight(ctx context.Context) (uint64, error) {
	if err := p.ensureReady(ctx); err != nil {
		return 0, err
	}
	var height *int64
	if err := p.pool.QueryRow(ctx, `SELECT max(height) FROM blocks`).Scan(&height); err != nil {
		return 0, err
	}
	if height == nil {
		return 0, nil
	}
	return uint64(*height), nil
}

// BlockHash returns the indexed hash at a height.
func (p *Postgres) BlockHash(ctx context.Context, height uint64) (string, bool, error) {
	if err := p.ensureReady(ctx); err != nil {
		return "", false, err
	}
	var hash string
	err := p.pool.QueryRow(ctx, `SELECT hash FROM blocks WHERE height = $1`, height).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

// Close releases the connection pool.
func (p *Postgres) Close() { p.pool.Close() }
