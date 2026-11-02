package indexer

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/anorebel/anorebelcoin/internal/ledger"
)

// Async keeps the index up to date without blocking block production.
//
// Submit only signals the worker; the worker catches up from the sink's own
// height. A dropped notification, a database outage, or a reorg therefore all
// converge on the next sync instead of losing writes.
type Async struct {
	sink   Sink
	source Source
	log    *slog.Logger

	notify chan struct{}
	stop   chan struct{}
	wg     sync.WaitGroup
}

// NewAsync wraps a sink. Call Start to run the worker.
func NewAsync(sink Sink, source Source, logger *slog.Logger) *Async {
	if logger == nil {
		logger = slog.Default()
	}
	return &Async{
		sink: sink, source: source, log: logger,
		notify: make(chan struct{}, 1),
		stop:   make(chan struct{}),
	}
}

// Start runs catch-up and then follows new blocks.
func (a *Async) Start() {
	a.wg.Add(1)
	go a.run()
}

// Submit asks the worker to sync. It never blocks.
func (a *Async) Submit(ledger.Block) {
	select {
	case a.notify <- struct{}{}:
	default:
	}
}

func (a *Async) run() {
	defer a.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	a.sync()
	for {
		select {
		case <-a.stop:
			return
		case <-a.notify:
			a.sync()
		case <-ticker.C:
			a.sync()
		}
	}
}

func (a *Async) sync() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	height, err := CatchUp(ctx, a.sink, a.source)
	if err != nil {
		a.log.Warn("indexer sync failed; node continues", "indexed", height, "error", err)
		return
	}
	a.log.Debug("indexer synced", "height", height)
}

// Close stops the worker and closes the sink.
func (a *Async) Close() {
	close(a.stop)
	a.wg.Wait()
	a.sink.Close()
}
