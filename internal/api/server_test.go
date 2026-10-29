package api

import (
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

func TestPrunedBlockReturnsAvailableRange(t *testing.T) {
	const network = "arne:devnet"
	funder, _, err := protocol.NewKeyPair(network)
	if err != nil {
		t.Fatal(err)
	}
	faucet, _, err := protocol.NewKeyPair(network)
	if err != nil {
		t.Fatal(err)
	}
	now := uint64(time.Now().Unix())
	genesis := node.DevnetGenesis(network, now, funder, faucet)
	n, err := node.New(node.Config{
		Network: network, Chain: genesis.Chain, Params: ledger.DefaultParams(),
		KeepBlocks: 2,
	}, genesis, storage.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
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
