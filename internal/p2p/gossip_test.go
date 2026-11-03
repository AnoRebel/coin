package p2p

import (
	"errors"
	"strings"
	"testing"
)

func hash(seed byte) string {
	return strings.Repeat(string("0123456789abcdef"[seed%16]), 64)
}

func TestBlocksRelayBeforeTransactions(t *testing.T) {
	gossip, err := NewGossip(1<<20, 1<<16, 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := gossip.Offer(Message{Kind: Tx, ID: hash(1), Size: 200}); got != Queued {
		t.Fatalf("tx offer = %s", got)
	}
	if got := gossip.Offer(Message{Kind: Block, ID: hash(2), Size: 5000}); got != Queued {
		t.Fatalf("block offer = %s", got)
	}
	if got := gossip.Offer(Message{Kind: Tx, ID: hash(3), Size: 300}); got != Queued {
		t.Fatalf("second tx offer = %s", got)
	}
	want := []Kind{Block, Tx, Tx}
	for i, kind := range want {
		msg, ok := gossip.Next()
		if !ok {
			t.Fatalf("message %d missing", i)
		}
		if msg.Kind != kind {
			t.Fatalf("message %d kind = %s, want %s", i, msg.Kind, kind)
		}
	}
	if _, ok := gossip.Next(); ok {
		t.Fatal("queue not empty")
	}
}

func TestDuplicatesAreSuppressedPerKind(t *testing.T) {
	gossip, _ := NewGossip(1<<20, 1<<16, 10, 100)
	message := Message{Kind: Tx, ID: hash(1), Size: 100}
	if got := gossip.Offer(message); got != Queued {
		t.Fatalf("first offer = %s", got)
	}
	if got := gossip.Offer(message); got != Duplicate {
		t.Fatalf("repeat offer = %s", got)
	}
	if _, ok := gossip.Next(); !ok {
		t.Fatal("queued message missing")
	}
	if got := gossip.Offer(message); got != Duplicate {
		t.Fatalf("repeat after delivery = %s", got)
	}
	block := Message{Kind: Block, ID: hash(1), Size: 100}
	if got := gossip.Offer(block); got != Queued {
		t.Fatalf("same hash as a block = %s", got)
	}
	if !gossip.Seen(Tx, hash(1)) || gossip.Seen(Tx, hash(2)) {
		t.Fatal("seen set is wrong")
	}
	if stats := gossip.Stats(); stats.Duplicate != 2 {
		t.Fatalf("duplicate stats = %d", stats.Duplicate)
	}
}

func TestSizeLimitsArePerClass(t *testing.T) {
	gossip, _ := NewGossip(5000, 500, 10, 100)
	if got := gossip.Offer(Message{Kind: Tx, ID: hash(1), Size: 501}); got != Oversized {
		t.Fatalf("oversized tx = %s", got)
	}
	if got := gossip.Offer(Message{Kind: Block, ID: hash(2), Size: 5000}); got != Queued {
		t.Fatalf("block at the limit = %s", got)
	}
	if got := gossip.Offer(Message{Kind: Block, ID: hash(3), Size: 5001}); got != Oversized {
		t.Fatalf("oversized block = %s", got)
	}
}

func TestFullQueueDoesNotSuppressTheMessage(t *testing.T) {
	gossip, _ := NewGossip(1<<20, 1<<16, 1, 100)
	first := Message{Kind: Tx, ID: hash(1), Size: 100}
	second := Message{Kind: Tx, ID: hash(2), Size: 100}
	if got := gossip.Offer(first); got != Queued {
		t.Fatalf("first = %s", got)
	}
	if got := gossip.Offer(second); got != Full {
		t.Fatalf("second = %s", got)
	}
	if gossip.Seen(Tx, second.ID) {
		t.Fatal("a message refused for capacity was marked seen")
	}
	if _, ok := gossip.Next(); !ok {
		t.Fatal("first message missing")
	}
	if got := gossip.Offer(second); got != Queued {
		t.Fatalf("retry after space = %s", got)
	}
}

func TestInvalidMessagesAreRejected(t *testing.T) {
	gossip, _ := NewGossip(1<<20, 1<<16, 10, 100)
	cases := []Message{
		{Kind: "header", ID: hash(1), Size: 10},
		{Kind: Tx, ID: "short", Size: 10},
		{Kind: Tx, ID: strings.Repeat("A", 64), Size: 10},
		{Kind: Tx, ID: hash(1), Size: 0},
		{Kind: Tx, ID: hash(1), Size: -1},
	}
	for _, message := range cases {
		if got := gossip.Offer(message); got != Invalid {
			t.Fatalf("offer %+v = %s, want invalid", message, got)
		}
	}
	if gossip.Stats().Invalid != uint64(len(cases)) {
		t.Fatalf("invalid stats = %d", gossip.Stats().Invalid)
	}
}

func TestSeenSetEvictsOldest(t *testing.T) {
	gossip, _ := NewGossip(1<<20, 1<<16, 10, 2)
	first := Message{Kind: Tx, ID: hash(1), Size: 10}
	second := Message{Kind: Tx, ID: hash(2), Size: 10}
	third := Message{Kind: Tx, ID: hash(3), Size: 10}
	for _, message := range []Message{first, second, third} {
		if got := gossip.Offer(message); got != Queued {
			t.Fatalf("offer = %s", got)
		}
	}
	if gossip.Seen(Tx, first.ID) {
		t.Fatal("oldest seen ID was not evicted")
	}
	if !gossip.Seen(Tx, second.ID) || !gossip.Seen(Tx, third.ID) {
		t.Fatal("recent seen IDs missing")
	}
	if gossip.Stats().Evicted != 1 {
		t.Fatalf("evicted = %d", gossip.Stats().Evicted)
	}
}

func TestGossipRequiresLimits(t *testing.T) {
	if _, err := NewGossip(0, 1, 1, 1); !errors.Is(err, ErrGossip) {
		t.Fatalf("zero block limit = %v", err)
	}
	if _, err := NewGossip(1, 1, 0, 1); !errors.Is(err, ErrGossip) {
		t.Fatalf("zero capacity = %v", err)
	}
	if _, err := NewGossip(1, 1, 1, 0); !errors.Is(err, ErrGossip) {
		t.Fatalf("zero seen limit = %v", err)
	}
}
