package node

import (
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

func TestPruneDropsBlocksAndReceipts(t *testing.T) {
	const network = "arne:devnet"
	store, err := storage.OpenPebble(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	funder, _, err := protocol.NewKeyPair(network)
	if err != nil {
		t.Fatal(err)
	}
	faucet, _, err := protocol.NewKeyPair(network)
	if err != nil {
		t.Fatal(err)
	}
	genesis := DevnetGenesis(network, 1_700_000_000, funder, faucet)
	cfg := Config{
		Network: network, Chain: genesis.Chain, Params: ledger.DefaultParams(),
		Faucet: faucet, FaucetAmount: 1_000_000, Devnet: true, KeepBlocks: 2,
	}
	n, err := New(cfg, genesis, store)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := n.Faucet(funder.Address)
	if err != nil {
		t.Fatal(err)
	}
	now := uint64(time.Now().Unix())
	if _, err := n.ProduceBlock(now); err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 2; i++ {
		if _, err := n.ProduceBlock(now + i); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := n.Block(1); ok {
		t.Fatal("block 1 survived the retention window")
	}
	if oldest := n.OldestBlock(); oldest != 2 {
		t.Fatalf("oldest = %d", oldest)
	}
	if _, ok := n.Receipt(hash); ok {
		t.Fatal("receipt for pruned block survived")
	}
	for h := uint64(2); h <= 3; h++ {
		if _, ok := n.Block(h); !ok {
			t.Fatalf("block %d missing", h)
		}
	}
	// A restart must reopen at the last height even with older blocks gone.
	restarted, err := New(cfg, genesis, store)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Status().Height; got != 3 {
		t.Fatalf("restarted height = %d", got)
	}
	if _, err := restarted.ProduceBlock(now + 10); err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.Block(4); !ok {
		t.Fatal("restarted node cannot extend the chain")
	}
}
