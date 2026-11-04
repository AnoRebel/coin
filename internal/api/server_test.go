package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		KeepBlocks: keepBlocks, Proposer: funder.Address,
	}, genesis, storage.NewMemory())
	if err != nil {
		t.Fatal(err)
	}
	return n, funder
}

func TestInvalidIdempotencyHeaders(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{})
	for _, keys := range [][]string{{""}, {"has space"}, {strings.Repeat("x", 129)}, {"a,b"}, {"one", "two"}} {
		r := httptest.NewRequest(http.MethodPost, "/v1/transactions", bytes.NewReader([]byte{0xa0}))
		for _, key := range keys {
			r.Header.Add("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		var result struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusBadRequest || result.Error.Code != "INVALID_ARGUMENT" {
			t.Fatalf("keys %q: %d %s", keys, w.Code, w.Body)
		}
	}
}

func TestSubmitIdempotency(t *testing.T) {
	n, signer := newTestNode(t, 0)
	e := New(n, Config{})
	now := uint64(time.Now().Unix())
	tx := protocol.Tx{
		Version: 1, Network: testNetwork, Chain: "arne-devnet-1", Type: protocol.TxTransfer,
		Signer: signer.Address, CreatedAt: now, ExpiresAt: now + 3600,
		FeeAsset: protocol.NativeAsset(testNetwork), MaxFee: 1000,
		Payload: map[string]any{"asset": protocol.NativeAsset(testNetwork), "to": signer.Address, "amount": uint64(42)},
	}
	if err := tx.Sign(signer); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	post := func(body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/transactions", bytes.NewReader(body))
		r.Header.Set("Idempotency-Key", "invoice-42")
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	first := post(raw)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	wrapper, _ := json.Marshal(map[string]string{"tx": base64.StdEncoding.EncodeToString(raw)})
	retry := post(wrapper)
	if retry.Code != first.Code || retry.Body.String() != first.Body.String() {
		t.Fatalf("retry: %d %s; first: %s", retry.Code, retry.Body, first.Body)
	}
	if _, err := n.ProduceBlock(now + 1); err != nil {
		t.Fatal(err)
	}
	finalized := post(raw)
	if finalized.Code != http.StatusOK {
		t.Fatalf("finalized retry: %d %s", finalized.Code, finalized.Body)
	}
	var result struct {
		Hash    string         `json:"hash"`
		Receipt ledger.Receipt `json:"receipt"`
	}
	if err := json.Unmarshal(finalized.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	hash, _ := tx.Hash()
	if result.Hash != hash || result.Receipt.Hash != hash || !result.Receipt.Success {
		t.Fatalf("not original receipt: %s", finalized.Body)
	}
	tx.Nonce = 1
	tx.Signatures = nil
	if err := tx.Sign(signer); err != nil {
		t.Fatal(err)
	}
	different, _ := protocol.Encode(tx.ToCBOR())
	conflict := post(different)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict: %d %s", conflict.Code, conflict.Body)
	}
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

func TestIntentQuery(t *testing.T) {
	n, signer := newTestNode(t, 0)
	e := New(n, Config{})
	id := protocol.IntentID(testNetwork, signer.Address, "invoice_42")
	missing := httptest.NewRecorder()
	e.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/intents/"+id, nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing: %d %s", missing.Code, missing.Body)
	}
	now := uint64(time.Now().Unix())
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: testNetwork, Chain: "arne-devnet-1",
		Type: protocol.TxCreateIntent, Signer: signer.Address, Nonce: 0,
		CreatedAt: now, ExpiresAt: now + 3600, FeeAsset: protocol.NativeAsset(testNetwork), MaxFee: 1000,
		Payload: map[string]any{"asset": protocol.NativeAsset(testNetwork), "amount": uint64(42), "reference": "invoice_42"},
	}
	if err := tx.Sign(signer); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	submit := httptest.NewRecorder()
	e.ServeHTTP(submit, httptest.NewRequest(http.MethodPost, "/v1/transactions", bytes.NewReader(raw)))
	if submit.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", submit.Code, submit.Body)
	}
	if _, err := n.ProduceBlock(now + 1); err != nil {
		t.Fatal(err)
	}
	query := httptest.NewRecorder()
	e.ServeHTTP(query, httptest.NewRequest(http.MethodGet, "/v1/intents/"+id, nil))
	var intent protocol.Intent
	if err := json.Unmarshal(query.Body.Bytes(), &intent); err != nil {
		t.Fatal(err)
	}
	if query.Code != http.StatusOK || intent.ID != id || intent.Amount != 42 || intent.Status != protocol.IntentOpen {
		t.Fatalf("query: %d %s", query.Code, query.Body)
	}
}

func TestSubscriptionQuery(t *testing.T) {
	n, payer := newTestNode(t, 0)
	e := New(n, Config{})
	payee, _, err := protocol.NewKeyPair(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	native := protocol.NativeAsset(testNetwork)
	id := protocol.SubscriptionID(testNetwork, payer.Address, payee.Address, native, 0)
	missing := httptest.NewRecorder()
	e.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/subscriptions/"+id, nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing: %d %s", missing.Code, missing.Body)
	}
	now := uint64(time.Now().Unix())
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: testNetwork, Chain: "arne-devnet-1",
		Type: protocol.TxCreateSub, Signer: payer.Address, Nonce: 0,
		CreatedAt: now, ExpiresAt: now + 3600, FeeAsset: native, MaxFee: 1000,
		Payload: map[string]any{"payee": payee.Address, "asset": native,
			"amount_per_period": uint64(500), "period": uint64(3600), "total_cap": uint64(2000)},
	}
	if err := tx.Sign(payer); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	submit := httptest.NewRecorder()
	e.ServeHTTP(submit, httptest.NewRequest(http.MethodPost, "/v1/transactions", bytes.NewReader(raw)))
	if submit.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", submit.Code, submit.Body)
	}
	if _, err := n.ProduceBlock(now + 1); err != nil {
		t.Fatal(err)
	}
	query := httptest.NewRecorder()
	e.ServeHTTP(query, httptest.NewRequest(http.MethodGet, "/v1/subscriptions/"+id, nil))
	var sub protocol.Subscription
	if err := json.Unmarshal(query.Body.Bytes(), &sub); err != nil {
		t.Fatal(err)
	}
	if query.Code != http.StatusOK || sub.ID != id || sub.Payer != payer.Address ||
		sub.Payee != payee.Address || sub.AmountPerPeriod != 500 || sub.TotalCap != 2000 ||
		sub.Status != protocol.SubscriptionActive {
		t.Fatalf("query: %d %s", query.Code, query.Body)
	}
}

func TestTransferQuery(t *testing.T) {
	n, signer := newTestNode(t, 0)
	e := New(n, Config{})
	missing := httptest.NewRecorder()
	e.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/transfers/"+strings.Repeat("0", 64), nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing: %d %s", missing.Code, missing.Body)
	}
	now := uint64(time.Now().Unix())
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: testNetwork, Chain: "arne-devnet-1",
		Type: protocol.TxTransfer, Signer: signer.Address, Nonce: 0,
		CreatedAt: now, ExpiresAt: now + 3600,
		FeeAsset: protocol.NativeAsset(testNetwork), MaxFee: 1000,
		Payload: map[string]any{"asset": protocol.NativeAsset(testNetwork), "to": signer.Address, "amount": uint64(42)},
	}
	if err := tx.Sign(signer); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	submit := httptest.NewRecorder()
	e.ServeHTTP(submit, httptest.NewRequest(http.MethodPost, "/v1/transactions", bytes.NewReader(raw)))
	if submit.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", submit.Code, submit.Body)
	}
	if _, err := n.ProduceBlock(now + 1); err != nil {
		t.Fatal(err)
	}
	hash, _ := tx.Hash()
	query := httptest.NewRecorder()
	e.ServeHTTP(query, httptest.NewRequest(http.MethodGet, "/v1/transfers/"+hash, nil))
	var body struct {
		Hash       string `json:"hash"`
		From       string `json:"from"`
		To         string `json:"to"`
		Asset      string `json:"asset"`
		Amount     uint64 `json:"amount"`
		RefundOf   string `json:"refund_of"`
		Refunded   uint64 `json:"refunded"`
		Refundable uint64 `json:"refundable"`
	}
	if err := json.Unmarshal(query.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if query.Code != http.StatusOK || body.Hash != hash || body.From != signer.Address ||
		body.To != signer.Address || body.Asset != protocol.NativeAsset(testNetwork) ||
		body.Amount != 42 || body.Refunded != 0 || body.Refundable != 42 || body.RefundOf != "" {
		t.Fatalf("query: %d %s", query.Code, query.Body)
	}
}
