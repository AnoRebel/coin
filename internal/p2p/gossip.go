package p2p

import (
	"errors"
	"fmt"
)

// ErrGossip is wrapped by every gossip validation failure.
var ErrGossip = errors.New("p2p: gossip rejected")

// Kind is a gossip message class. Blocks are always relayed before
// transactions.
type Kind string

const (
	Block Kind = "block"
	Tx    Kind = "tx"
)

// Decision is what happened to an offered message.
type Decision string

const (
	Queued    Decision = "queued"
	Duplicate Decision = "duplicate"
	Oversized Decision = "oversized"
	Full      Decision = "full"
	Invalid   Decision = "invalid"
)

// Message is one gossip item. ID is the block or transaction hash.
type Message struct {
	Kind Kind   `json:"kind"`
	ID   string `json:"id"`
	Size int    `json:"size"`
	From string `json:"from,omitempty"`
}

// Stats counts what a node has dropped, so operators can see suppression
// working rather than guessing.
type Stats struct {
	Queued    uint64 `json:"queued"`
	Duplicate uint64 `json:"duplicate"`
	Oversized uint64 `json:"oversized"`
	Full      uint64 `json:"full"`
	Invalid   uint64 `json:"invalid"`
	Evicted   uint64 `json:"evicted"`
}

// Gossip is a bounded relay queue: it drops what it has already seen, refuses
// what is too large, and serves blocks before transactions. It is not safe for
// concurrent use.
type Gossip struct {
	maxBlock  int
	maxTx     int
	capacity  int
	seenLimit int
	seen      map[string]bool
	order     []string
	blocks    []Message
	txs       []Message
	stats     Stats
}

// NewGossip sets per-class size limits, the queue capacity, and how many message
// IDs are remembered for deduplication. A zero seen limit disables eviction and
// is refused, because an unbounded set is a memory leak under flood.
func NewGossip(maxBlock, maxTx, capacity, seenLimit int) (*Gossip, error) {
	if maxBlock <= 0 || maxTx <= 0 || capacity <= 0 || seenLimit <= 0 {
		return nil, fmt.Errorf("%w: limits and capacity must be positive", ErrGossip)
	}
	return &Gossip{
		maxBlock:  maxBlock,
		maxTx:     maxTx,
		capacity:  capacity,
		seenLimit: seenLimit,
		seen:      map[string]bool{},
	}, nil
}

// Offer enqueues a message if it is new, small enough, and there is room. Seen
// IDs are remembered only when the message is queued, so a full queue can be
// retried instead of permanently suppressing the message.
func (g *Gossip) Offer(msg Message) Decision {
	if !validKind(msg.Kind) || !validHash(msg.ID) || msg.Size <= 0 {
		g.stats.Invalid++
		return Invalid
	}
	key := string(msg.Kind) + ":" + msg.ID
	if g.seen[key] {
		g.stats.Duplicate++
		return Duplicate
	}
	limit := g.maxTx
	if msg.Kind == Block {
		limit = g.maxBlock
	}
	if msg.Size > limit {
		g.stats.Oversized++
		return Oversized
	}
	if len(g.blocks)+len(g.txs) >= g.capacity {
		g.stats.Full++
		return Full
	}
	g.remember(key)
	if msg.Kind == Block {
		g.blocks = append(g.blocks, msg)
	} else {
		g.txs = append(g.txs, msg)
	}
	g.stats.Queued++
	return Queued
}

// Next returns the next message to relay: every queued block before any
// transaction, oldest first within a class.
func (g *Gossip) Next() (Message, bool) {
	if len(g.blocks) > 0 {
		msg := g.blocks[0]
		g.blocks = g.blocks[1:]
		return msg, true
	}
	if len(g.txs) > 0 {
		msg := g.txs[0]
		g.txs = g.txs[1:]
		return msg, true
	}
	return Message{}, false
}

// Len is the number of queued messages, excluding those already delivered.
func (g *Gossip) Len() int { return len(g.blocks) + len(g.txs) }

// Seen reports whether an ID has been queued recently.
func (g *Gossip) Seen(kind Kind, id string) bool { return g.seen[string(kind)+":"+id] }

// Stats returns a snapshot of the counters.
func (g *Gossip) Stats() Stats { return g.stats }

func (g *Gossip) remember(key string) {
	if len(g.order) >= g.seenLimit {
		oldest := g.order[0]
		g.order = g.order[1:]
		delete(g.seen, oldest)
		g.stats.Evicted++
	}
	g.seen[key] = true
	g.order = append(g.order, key)
}

func validKind(kind Kind) bool { return kind == Block || kind == Tx }

func validHash(id string) bool {
	if len(id) != 64 {
		return false
	}
	return lowerHex(id)
}
