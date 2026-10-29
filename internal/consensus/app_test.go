package consensus

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"

	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

const testNet = "arne:devnet"

func newApp(t *testing.T) (*App, *protocol.KeyPair) {
	t.Helper()
	funder, _, err := protocol.NewKeyPair(testNet)
	if err != nil {
		t.Fatal(err)
	}
	faucet, _, err := protocol.NewKeyPair(testNet)
	if err != nil {
		t.Fatal(err)
	}
	genesis := node.DevnetGenesis(testNet, 1_700_000_000, funder, faucet)
	n, err := node.New(node.Config{
		Network: genesis.Network, Chain: genesis.Chain, Params: genesis.Params,
		Devnet: true,
	}, genesis, storage.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	return NewApp(n, genesis.Network, genesis.Chain, 500), funder
}

func encodeTx(t *testing.T, kp *protocol.KeyPair, to string) []byte {
	t.Helper()
	now := uint64(time.Now().Unix())
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: testNet, Chain: "arne-devnet-1",
		Type: protocol.TxTransfer, Signer: kp.Address, Nonce: 0,
		CreatedAt: now, ExpiresAt: now + 3_600,
		FeeAsset: protocol.NativeAsset(testNet), MaxFee: 10_000,
		Payload: map[string]any{
			"asset": protocol.NativeAsset(testNet), "to": to, "amount": uint64(1_000_000),
		},
	}
	if err := tx.Sign(kp); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestABCIBlockLifecycle drives the app through the ABCI calls CometBFT makes
// for one block and checks that state advances exactly once.
func TestABCIBlockLifecycle(t *testing.T) {
	ctx := context.Background()
	app, funder := newApp(t)
	to, _, err := protocol.NewKeyPair(testNet)
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeTx(t, funder, to.Address)

	info, err := app.Info(ctx, &abci.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 0 {
		t.Fatalf("height = %d", info.LastBlockHeight)
	}

	if res, _ := app.CheckTx(ctx, &abci.CheckTxRequest{Tx: raw}); res.Code != 0 {
		t.Fatalf("check failed: %s", res.Log)
	}
	if res, _ := app.CheckTx(ctx, &abci.CheckTxRequest{Tx: []byte{0xff}}); res.Code == 0 {
		t.Fatal("garbage transaction accepted")
	}

	prop, err := app.PrepareProposal(ctx, &abci.PrepareProposalRequest{
		MaxTxBytes: 1 << 20, Txs: [][]byte{raw},
	})
	if err != nil || len(prop.Txs) != 1 {
		t.Fatalf("proposal = %d txs, err %v", len(prop.Txs), err)
	}
	if res, _ := app.ProcessProposal(ctx, &abci.ProcessProposalRequest{Txs: prop.Txs}); res.Status != abci.PROCESS_PROPOSAL_STATUS_ACCEPT {
		t.Fatalf("proposal rejected: %v", res.Status)
	}
	if res, _ := app.ProcessProposal(ctx, &abci.ProcessProposalRequest{Txs: [][]byte{{0xff}}}); res.Status != abci.PROCESS_PROPOSAL_STATUS_REJECT {
		t.Fatal("malformed proposal accepted")
	}

	final, err := app.FinalizeBlock(ctx, &abci.FinalizeBlockRequest{
		Txs: prop.Txs, Height: 1, Time: time.Now(),
		ProposerAddress: []byte{1, 2, 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(final.TxResults) != 1 || final.TxResults[0].Code != 0 {
		t.Fatalf("results = %+v", final.TxResults)
	}
	if len(final.AppHash) != 32 {
		t.Fatalf("app hash = %x", final.AppHash)
	}

	if _, err := app.Commit(ctx, &abci.CommitRequest{}); err != nil {
		t.Fatal(err)
	}
	info, err = app.Info(ctx, &abci.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 1 {
		t.Fatalf("height = %d", info.LastBlockHeight)
	}
	if hex.EncodeToString(info.LastBlockAppHash) != hex.EncodeToString(final.AppHash) {
		t.Fatalf("committed hash %x != finalized %x", info.LastBlockAppHash, final.AppHash)
	}
	if bal := app.node.State().Balance(to.Address, protocol.NativeAsset(testNet)); bal != 1_000_000 {
		t.Fatalf("recipient balance = %d", bal)
	}
	data, _ := json.Marshal(map[string]string{"address": to.Address, "asset": protocol.NativeAsset(testNet)})
	q, err := app.Query(ctx, &abci.QueryRequest{Path: "/balance", Data: data})
	if err != nil || q.Code != 0 || !strings.Contains(string(q.Value), "1000000") {
		t.Fatalf("query = %+v, err %v", q, err)
	}
	if q, _ := app.Query(ctx, &abci.QueryRequest{Path: "/nope"}); q.Code == 0 {
		t.Fatal("unknown query path accepted")
	}

	// A second block at the same height must be refused, not silently applied.
	if _, err := app.FinalizeBlock(ctx, &abci.FinalizeBlockRequest{Height: 1, Time: time.Now()}); err == nil {
		t.Fatal("duplicate height accepted")
	}
}

func TestInitChainRejectsWrongChain(t *testing.T) {
	app, _ := newApp(t)
	_, err := app.InitChain(context.Background(), &abci.InitChainRequest{ChainId: "other-1"})
	if err == nil {
		t.Fatal("mismatched chain id accepted")
	}
	if _, err := app.InitChain(context.Background(), &abci.InitChainRequest{ChainId: "arne-devnet-1"}); err != nil {
		t.Fatal(err)
	}
}
