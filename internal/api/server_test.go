package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

const testNetwork = "arne:devnet"

// newTestNode builds an in-memory node with funded funder and faucet keys.
func newTestNode(t *testing.T, keepBlocks uint64) (*node.Node, *protocol.KeyPair) {
	t.Helper()
	funder, _, err := protocol.NewKeyPair(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	faucet, _, err := protocol.NewKeyPair(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	genesis := node.DevnetGenesis(testNetwork, uint64(time.Now().Unix()), funder, faucet)
	n, err := node.New(node.Config{
		Network: testNetwork, Chain: genesis.Chain, Params: ledger.DefaultParams(),
		KeepBlocks: keepBlocks,
	}, genesis, storage.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	return n, funder
}

func TestPrunedBlockReturnsAvailableRange(t *testing.T) {
	n, _ := newTestNode(t, 2)
	now := uint64(time.Now().Unix())
	for i := 0; i < 3; i++ {
		if _, err := n.ProduceBlock(now + uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	New(n, Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/blocks/1", nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "PRUNED" || body.Error.Message != "block 1 pruned; available from 2" {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestUnknownEnvelopeFieldReturnsInvalidSchema(t *testing.T) {
	n, signer := newTestNode(t, 0)
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: testNetwork, Chain: "arne-devnet-1",
		Type: protocol.TxTransfer, Signer: signer.Address, Nonce: 0,
		CreatedAt: uint64(time.Now().Unix()), ExpiresAt: uint64(time.Now().Unix()) + 3600,
		FeeAsset: protocol.NativeAsset(testNetwork), MaxFee: 1_000,
		Payload: map[string]any{"asset": protocol.NativeAsset(testNetwork), "to": signer.Address, "amount": uint64(1)},
	}
	body := tx.ToCBOR()
	body["crit"] = []any{"extension"}
	raw, err := protocol.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	New(n, Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/transactions", bytes.NewReader(raw)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	var bodyErr struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bodyErr); err != nil {
		t.Fatal(err)
	}
	if bodyErr.Error.Code != "INVALID_SCHEMA" {
		t.Fatalf("body = %s", rec.Body)
	}
}
