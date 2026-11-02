// Package indexer maintains an optional Postgres read model of the chain.
//
// It is never a consensus dependency: the node hands blocks to an asynchronous
// sink and keeps producing if the database is down. Writes are idempotent, and
// a block whose predecessor no longer matches replaces the orphaned suffix.
package indexer

import (
	"context"
	"errors"
	"fmt"

	"github.com/anorebel/anorebelcoin/internal/ledger"
)

// Source reads canonical blocks from the node store.
type Source interface {
	Block(height uint64) (ledger.Block, bool, error)
	LastBlock() (ledger.Block, bool, error)
}

// Sink is a durable index target.
type Sink interface {
	Apply(ctx context.Context, block ledger.Block) error
	IndexedHeight(ctx context.Context) (uint64, error)
	BlockHash(ctx context.Context, height uint64) (string, bool, error)
	Close()
}

// ErrPruned means the node no longer has a block the indexer needs.
var ErrPruned = errors.New("indexer: source block is pruned")

// CatchUp indexes every block above the last indexed height. It first walks
// back over any stored blocks that no longer match the source, so a reorg that
// did not change the chain height is repaired too.
func CatchUp(ctx context.Context, sink Sink, source Source) (uint64, error) {
	from, err := sink.IndexedHeight(ctx)
	if err != nil {
		return 0, err
	}
	last, ok, err := source.LastBlock()
	if err != nil {
		return from, err
	}
	if !ok {
		if from > 0 {
			return from, fmt.Errorf("%w: last block", ErrPruned)
		}
		return from, nil
	}
	for from > 0 {
		stored, found, err := sink.BlockHash(ctx, from)
		if err != nil {
			return from, err
		}
		if !found {
			break
		}
		block, found, err := source.Block(from)
		if err != nil {
			return from, err
		}
		if !found {
			return from, fmt.Errorf("%w: height %d", ErrPruned, from)
		}
		hash, err := block.Hash()
		if err != nil {
			return from, err
		}
		if hash == stored {
			break
		}
		from--
	}
	for height := from + 1; height <= last.Height; height++ {
		block, found, err := source.Block(height)
		if err != nil {
			return height - 1, err
		}
		if !found {
			return height - 1, fmt.Errorf("%w: height %d", ErrPruned, height)
		}
		if err := sink.Apply(ctx, block); err != nil {
			return height - 1, err
		}
	}
	return last.Height, nil
}
