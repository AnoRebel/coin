package arne_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	arne "github.com/anorebel/anorebelcoin/sdk/go/arne"
)

func server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(arne.Status{Network: "arne:devnet", Height: 7, StateRoot: "ab"})
	})
	mux.HandleFunc("/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/accounts/arce-devnet1x" {
			http.Error(w, "{}", 404)
			return
		}
		json.NewEncoder(w).Encode(arne.Account{Address: "arce-devnet1x", Exists: true, Nonce: 3, Balances: map[string]uint64{"arce:devnet": 5}})
	})
	mux.HandleFunc("/v1/transactions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/cbor" {
			http.Error(w, `{"error":{"code":"INVALID_ARGUMENT","message":"content type"}}`, 400)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"hash": "deadbeef", "status": "accepted"})
	})
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": 7})
	})
	mux.HandleFunc("/v1/faucet", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"faucet is only available on devnet"}}`, 403)
	})
	return httptest.NewServer(mux)
}

func TestClientReadsAndWrites(t *testing.T) {
	srv := server(t)
	defer srv.Close()
	client := arne.New(srv.URL)
	ctx := context.Background()

	status, err := client.Status(ctx)
	if err != nil || status.Height != 7 || status.Network != "arne:devnet" {
		t.Fatalf("status: %+v %v", status, err)
	}
	acct, err := client.Account(ctx, "arce-devnet1x")
	if err != nil || acct.Nonce != 3 || acct.Balances["arce:devnet"] != 5 {
		t.Fatalf("account: %+v %v", acct, err)
	}
	hash, err := client.Submit(ctx, []byte{0xa0})
	if err != nil || hash != "deadbeef" {
		t.Fatalf("submit: %q %v", hash, err)
	}
	var height uint64
	if err := client.RPC(ctx, "arne_blockNumber", nil, &height); err != nil || height != 7 {
		t.Fatalf("rpc: %d %v", height, err)
	}
	if _, err := client.Faucet(ctx, "arce-devnet1x"); err == nil || !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Fatalf("faucet error: %v", err)
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		value    uint64
		decimals uint8
		want     string
	}{
		{0, 0, "0"},
		{123, 0, "123"},
		{1_250_000_000, 8, "12.5"},
		{100, 2, "1"},
		{101, 2, "1.01"},
	}
	for _, c := range cases {
		if got := arne.FormatAmount(c.value, c.decimals); got != c.want {
			t.Fatalf("FormatAmount(%d,%d)=%q want %q", c.value, c.decimals, got, c.want)
		}
	}
}
