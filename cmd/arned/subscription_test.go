package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

func TestSubscriptionCLIFlow(t *testing.T) {
	const phrase = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	const payee = "arce-devnet1q8t23v9987ns9qmtrxqxs4cqqsqjll0pz5n0e0dx"
	const network = "arne:devnet"
	kp, err := protocol.KeyPairFromMnemonic(phrase, "", 0, 0, 0, network)
	if err != nil {
		t.Fatal(err)
	}
	subID := protocol.SubscriptionID(network, kp.Address, payee, "arce:devnet", 4)
	var submitted []protocol.Tx
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			m, err := protocol.DecodeMap(body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			tx, err := protocol.TxFromCBOR(m)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if err := tx.Verify(); err != nil {
				t.Error(err)
			}
			submitted = append(submitted, tx)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"hash": "accepted"})
			return
		}
		switch {
		case r.URL.Path == "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]string{"network": network})
		case r.URL.Path == "/v1/accounts/"+kp.Address:
			_ = json.NewEncoder(w).Encode(map[string]any{"address": kp.Address, "nonce": 4})
		case r.URL.Path == "/v1/subscriptions/"+subID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": subID, "payer": kp.Address, "payee": payee, "asset": "arce:devnet",
				"amount_per_period": uint64(500_000_000), "period": uint64(3600),
				"total_cap": uint64(5_000_000_000), "charged_period": uint64(100_000_000),
				"charged_total": uint64(200_000_000), "period_start": uint64(time.Now().Unix()) - 10,
				"expires_at": uint64(0), "status": protocol.SubscriptionActive, "created_at": uint64(1),
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "arce:devnet", "symbol": "ARCE", "decimals": 8})
		}
	}))
	defer srv.Close()
	t.Setenv("ARNE_API", srv.URL)

	run := func(args ...string) {
		t.Helper()
		cmd := subscriptionCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	run("create", "--mnemonic", phrase, "--payee", payee, "--asset", "arce:devnet",
		"--amount-per-period", "10", "--period", "3600", "--total-cap", "50")
	run("charge", "--mnemonic", phrase, "--id", subID)
	run("show", "--id", subID)
	run("cancel", "--mnemonic", phrase, "--id", subID)

	if len(submitted) != 3 {
		t.Fatalf("submitted %d transactions", len(submitted))
	}
	create := submitted[0]
	if create.Type != protocol.TxCreateSub || create.Nonce != 4 ||
		create.Payload["payee"] != payee ||
		create.Payload["amount_per_period"] != uint64(1_000_000_000) ||
		create.Payload["total_cap"] != uint64(5_000_000_000) ||
		create.Payload["period"] != uint64(3600) {
		t.Fatalf("create payload: %+v", create.Payload)
	}
	charge := submitted[1]
	if charge.Type != protocol.TxChargeSub || charge.Payload["id"] != subID ||
		charge.Payload["amount"] != uint64(400_000_000) {
		t.Fatalf("charge payload: %+v", charge.Payload)
	}
	cancel := submitted[2]
	if cancel.Type != protocol.TxCancelSub || cancel.Payload["id"] != subID {
		t.Fatalf("cancel payload: %+v", cancel.Payload)
	}
}
