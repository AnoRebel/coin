package indexer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/ledger"
)

type record struct {
	hash     string
	prevRoot string
	txs      []string
}

type fakeSink struct {
	mu      sync.Mutex
	blocks  map[uint64]record
	applied int
}

func newFakeSink() *fakeSink { return &fakeSink{blocks: map[uint64]record{}} }

func (f *fakeSink) Apply(_ context.Context, block ledger.Block) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	hash, err := block.Hash()
	if err != nil {
		return err
	}
	orphan := uint64(0)
	if block.Height > 1 {
		if prev, ok := f.blocks[block.Height-1]; ok && prev.prevRoot != block.PrevRoot {
			orphan = block.Height - 1
		}
	}
	if existing, ok := f.blocks[block.Height]; ok && existing.hash != hash && (orphan == 0 || block.Height < orphan) {
		orphan = block.Height
	}
	if orphan > 0 {
		for height := range f.blocks {
			if height >= orphan {
				delete(f.blocks, height)
			}
		}
	}
	f.blocks[block.Height] = record{hash: hash, prevRoot: block.StateRoot, txs: block.TxHashes}
	f.applied++
	return nil
}

func (f *fakeSink) IndexedHeight(context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var highest uint64
	for height := range f.blocks {
		if height > highest {
			highest = height
		}
	}
	return highest, nil
}

func (f *fakeSink) BlockHash(_ context.Context, height uint64) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blocks[height]
	return b.hash, ok, nil
}

func (f *fakeSink) Close() {}

type fakeSource struct {
	blocks map[uint64]ledger.Block
	last   uint64
}

func (f *fakeSource) Block(height uint64) (ledger.Block, bool, error) {
	b, ok := f.blocks[height]
	return b, ok, nil
}

func (f *fakeSource) LastBlock() (ledger.Block, bool, error) {
	b, ok := f.blocks[f.last]
	return b, ok, nil
}

func block(height uint64, stateRoot string, txs ...string) ledger.Block {
	prev := "root-0"
	if height > 1 {
		prev = "root-" + string(rune('0'+height-1))
	}
	return ledger.Block{
		Height: height, Time: height, Proposer: "p",
		PrevRoot: prev, StateRoot: stateRoot, TxRoot: "tx-root", TxHashes: txs,
	}
}

func source(blocks ...ledger.Block) *fakeSource {
	s := &fakeSource{blocks: map[uint64]ledger.Block{}}
	for _, b := range blocks {
		s.blocks[b.Height] = b
		if b.Height > s.last {
			s.last = b.Height
		}
	}
	return s
}

func TestCatchUpIsIdempotent(t *testing.T) {
	sink := newFakeSink()
	src := source(block(1, "root-1", "a"), block(2, "root-2", "b"), block(3, "root-3", "c"))
	height, err := CatchUp(context.Background(), sink, src)
	if err != nil || height != 3 {
		t.Fatalf("catch up = %d, %v", height, err)
	}
	if _, err := CatchUp(context.Background(), sink, src); err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks) != 3 || sink.blocks[2].hash == "" {
		t.Fatalf("blocks = %v", sink.blocks)
	}
}

func TestReorgRemovesOrphans(t *testing.T) {
	sink := newFakeSink()
	original := source(block(1, "root-1", "a"), block(2, "root-2", "b"), block(3, "root-3", "c"))
	if _, err := CatchUp(context.Background(), sink, original); err != nil {
		t.Fatal(err)
	}
	oldTwo := sink.blocks[2].hash

	// Replace height 2 and 3; the replacement at 2 keeps the same height but a
	// different block hash, and 3 builds on the new state root.
	replacement := source(
		block(1, "root-1", "a"),
		block(2, "root-X", "x"),
		ledger.Block{Height: 3, Time: 3, Proposer: "p", PrevRoot: "root-X", StateRoot: "root-Y", TxRoot: "t", TxHashes: []string{"y"}},
	)
	height, err := CatchUp(context.Background(), sink, replacement)
	if err != nil || height != 3 {
		t.Fatalf("reorg catch up = %d, %v", height, err)
	}
	if sink.blocks[2].hash == oldTwo {
		t.Fatal("orphaned block 2 was kept")
	}
	if len(sink.blocks[2].txs) != 1 || sink.blocks[2].txs[0] != "x" {
		t.Fatalf("replacement tx not indexed: %v", sink.blocks[2].txs)
	}
	if len(sink.blocks[3].txs) != 1 || sink.blocks[3].txs[0] != "y" {
		t.Fatalf("replacement block 3 not indexed: %v", sink.blocks[3].txs)
	}
}

func TestReorgThatKeepsHeightIsRepaired(t *testing.T) {
	sink := newFakeSink()
	src := source(block(1, "root-1", "a"), block(2, "root-2", "b"))
	if _, err := CatchUp(context.Background(), sink, src); err != nil {
		t.Fatal(err)
	}
	// Same height, different block at 2.
	src.blocks[2] = block(2, "root-Z", "z")
	if _, err := CatchUp(context.Background(), sink, src); err != nil {
		t.Fatal(err)
	}
	if sink.blocks[2].txs[0] != "z" {
		t.Fatalf("divergent block not repaired: %v", sink.blocks[2].txs)
	}
}

func TestPrunedSourceIsAReorgError(t *testing.T) {
	sink := newFakeSink()
	src := source(block(1, "root-1", "a"), block(2, "root-2", "b"))
	if _, err := CatchUp(context.Background(), sink, src); err != nil {
		t.Fatal(err)
	}
	delete(src.blocks, 2)
	if _, err := CatchUp(context.Background(), sink, src); !errors.Is(err, ErrPruned) {
		t.Fatalf("pruned = %v", err)
	}
}

type blockingSink struct {
	fakeSink
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingSink) Apply(ctx context.Context, block ledger.Block) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.fakeSink.Apply(ctx, block)
}

func TestSubmitDoesNotBlockWhenDatabaseIsDown(t *testing.T) {
	sink := &blockingSink{fakeSink: *newFakeSink(), entered: make(chan struct{}), release: make(chan struct{})}
	async := NewAsync(sink, source(block(1, "root-1", "a")), slog.New(slog.NewTextHandler(io.Discard, nil)))
	async.Start()
	select {
	case <-sink.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("indexer never started")
	}
	done := make(chan struct{})
	go func() {
		for range 100 {
			async.Submit(ledger.Block{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Submit blocked while the database was down")
	}
	close(sink.release)
	async.Close()
}
