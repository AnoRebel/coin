package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/anorebel/anorebelcoin/internal/indexer"
	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/node"
)

// nodeSource adapts the node to the indexer's Source interface.
type nodeSource struct{ n *node.Node }

func (s nodeSource) Block(height uint64) (ledger.Block, bool, error) {
	b, ok := s.n.Block(height)
	return b, ok, nil
}

func (s nodeSource) LastBlock() (ledger.Block, bool, error) {
	b, ok := s.n.LastBlock()
	return b, ok, nil
}

// startIndexer opens the optional Postgres read model. It never fails the
// node: a database that is down is retried by the async worker.
func startIndexer(url string, n *node.Node) *indexer.Async {
	if url == "" {
		return nil
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	pg, err := indexer.OpenPostgres(context.Background(), url)
	if err != nil {
		logger.Warn("indexer disabled; node continues", "error", err)
		return nil
	}
	async := indexer.NewAsync(pg, nodeSource{n}, logger)
	async.Start()
	return async
}
