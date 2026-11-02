package node

import (
	"strings"
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

func testNode(t *testing.T, store storage.Store) (*Node, Config, ledger.Genesis) {
	t.Helper()
	const network = "arne:devnet"
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
		Proposer: funder.Address, Faucet: faucet, FaucetAmount: 1_000_000,
		Devnet: true,
	}
	n, err := New(cfg, genesis, store)
	if err != nil {
		t.Fatal(err)
	}
	return n, cfg, genesis
}

func TestHaltOnStateRootDivergence(t *testing.T) {
	store := storage.NewMemory()
	n, cfg, genesis := testNode(t, store)
	produced, err := n.ProduceBlock(1_700_000_001)
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Status().StateRoot; got != produced.StateRoot {
		t.Fatalf("status root %s != block root %s", got, produced.StateRoot)
	}
	block, ok, err := store.Block(1)
	if err != nil || !ok {
		t.Fatalf("block 1: ok=%v err=%v", ok, err)
	}
	block.StateRoot = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := store.SaveBlock(block); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(cfg, genesis, store)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reopened.Status().Halted, "state-root divergence") {
		t.Fatalf("halted = %q", reopened.Status().Halted)
	}
	if _, err := reopened.ProduceBlock(1_700_000_002); err == nil {
		t.Fatal("halted node produced a block")
	}
}

type recordingSink struct{ blocks []ledger.Block }

func (s *recordingSink) Submit(b ledger.Block) { s.blocks = append(s.blocks, b) }

func TestBlockSinkReceivesCommittedBlocks(t *testing.T) {
	now := uint64(1_700_000_001)
	n, cfg, _ := testNode(t, storage.NewMemory())
	sink := &recordingSink{}
	n.SetBlockSink(sink)
	produced, err := n.ProduceBlock(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks) != 1 || sink.blocks[0].Height != produced.Height {
		t.Fatalf("produced sink = %v", sink.blocks)
	}
	if last, ok := n.LastBlock(); !ok || last.Height != produced.Height {
		t.Fatalf("last block = %v, %v", last.Height, ok)
	}

	committed, _, err := n.FinalizeTxs(nil, 2, now+1, cfg.Proposer)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.CommitBlock(); err != nil {
		t.Fatal(err)
	}
	if len(sink.blocks) != 2 || sink.blocks[1].Height != committed.Height {
		t.Fatalf("committed sink = %v", sink.blocks)
	}
}

func TestHaltOnConsensusInvariant(t *testing.T) {
	n, _, _ := testNode(t, storage.NewMemory())
	if _, _, err := n.FinalizeTxs(nil, 5, 1_700_000_001, ""); err == nil {
		t.Fatal("invalid height accepted")
	}
	if !strings.Contains(n.Status().Halted, "invariant") {
		t.Fatalf("halted = %q", n.Status().Halted)
	}
	if _, err := n.ProduceBlock(1_700_000_002); err == nil {
		t.Fatal("halted node produced a block")
	}
}
