package indexer

import (
	"context"
	"os"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/ledger"
)

func TestPostgresCatchUpIdempotentAndReorg(t *testing.T) {
	url := os.Getenv("ARNE_POSTGRES_URL")
	if url == "" {
		t.Skip("set ARNE_POSTGRES_URL to run the Postgres indexer test")
	}
	ctx := context.Background()
	pg, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, err := pg.pool.Exec(ctx, `DROP TABLE IF EXISTS transactions, blocks, indexer_schema CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := pg.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	src := source(block(1, "root-1", "a"), block(2, "root-2", "b"), block(3, "root-3", "c"))
	if height, err := CatchUp(ctx, pg, src); err != nil || height != 3 {
		t.Fatalf("catch up = %d, %v", height, err)
	}
	assertCounts(t, pg, 3, 3)

	// Reapplying the same chain must not duplicate rows.
	if _, err := CatchUp(ctx, pg, src); err != nil {
		t.Fatal(err)
	}
	assertCounts(t, pg, 3, 3)

	// Replace heights 2 and 3. The orphaned suffix and its transactions go away.
	src = source(
		block(1, "root-1", "a"),
		block(2, "root-X", "x"),
		ledger.Block{Height: 3, Time: 3, Proposer: "p", PrevRoot: "root-X", StateRoot: "root-Y", TxRoot: "t", TxHashes: []string{"y"}},
	)
	if height, err := CatchUp(ctx, pg, src); err != nil || height != 3 {
		t.Fatalf("reorg catch up = %d, %v", height, err)
	}
	assertCounts(t, pg, 3, 3)

	var orphaned int
	if err := pg.pool.QueryRow(ctx, `SELECT count(*) FROM transactions WHERE hash IN ('b', 'c')`).Scan(&orphaned); err != nil {
		t.Fatal(err)
	}
	if orphaned != 0 {
		t.Fatalf("orphaned transactions kept: %d", orphaned)
	}
	var height uint64
	if err := pg.pool.QueryRow(ctx, `SELECT height FROM transactions WHERE hash = 'y'`).Scan(&height); err != nil {
		t.Fatal(err)
	}
	if height != 3 {
		t.Fatalf("replacement tx height = %d", height)
	}
}

func assertCounts(t *testing.T, pg *Postgres, blocks, txs int) {
	t.Helper()
	ctx := context.Background()
	var gotBlocks, gotTxs int
	if err := pg.pool.QueryRow(ctx, `SELECT count(*) FROM blocks`).Scan(&gotBlocks); err != nil {
		t.Fatal(err)
	}
	if err := pg.pool.QueryRow(ctx, `SELECT count(*) FROM transactions`).Scan(&gotTxs); err != nil {
		t.Fatal(err)
	}
	if gotBlocks != blocks || gotTxs != txs {
		t.Fatalf("blocks=%d txs=%d, want %d/%d", gotBlocks, gotTxs, blocks, txs)
	}
}
