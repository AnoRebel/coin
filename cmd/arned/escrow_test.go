package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

func escrowServer(t *testing.T, network string, escrow protocol.Escrow) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			json.NewEncoder(w).Encode(map[string]string{"network": network})
		case "/v1/escrows/" + escrow.ID:
			json.NewEncoder(w).Encode(escrow)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("ARNE_API", server.URL)
}

func TestFetchEscrowChecksIDAndNetwork(t *testing.T) {
	escrow := protocol.Escrow{
		ID: "esc_" + "0123456789abcdef0123456789abcdef", Payer: "arce-devnet1payer",
		Beneficiary: "arce-devnet1payee", Asset: "arce:devnet", Amount: 7,
		Status: protocol.EscrowOpen,
	}
	escrowServer(t, "arne:devnet", escrow)

	for _, id := range []string{"", "esc_", "esc_short", "ESC_" + escrow.ID[4:]} {
		if _, err := fetchEscrow("arne:devnet", id); err == nil {
			t.Fatalf("accepted malformed escrow ID %q", id)
		}
	}
	if _, err := fetchEscrow("arne:testnet", escrow.ID); err == nil {
		t.Fatal("accepted an escrow from another network")
	}
	got, err := fetchEscrow("arne:devnet", escrow.ID)
	if err != nil || got.Amount != escrow.Amount {
		t.Fatalf("fetch: %+v %v", got, err)
	}
}

func TestFetchEscrowRejectsForeignAsset(t *testing.T) {
	escrow := protocol.Escrow{
		ID: "esc_" + "0123456789abcdef0123456789abcdef", Asset: "arce:testnet",
		Status: protocol.EscrowOpen,
	}
	escrowServer(t, "arne:devnet", escrow)
	if _, err := fetchEscrow("arne:devnet", escrow.ID); err == nil {
		t.Fatal("accepted an escrow paying another network's asset")
	}
}
