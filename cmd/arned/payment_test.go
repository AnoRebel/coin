package main

import (
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

func TestPaymentURIKeepsIntent(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	r := paymentRequest{network: "arne:devnet"}
	uri := "arne:arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t?network=arne%3Adevnet&asset=arce%3Adevnet&amount=1&intent=" + id
	if err := r.applyURI(uri, false); err != nil {
		t.Fatal(err)
	}
	if r.intent != id {
		t.Fatal("URI intent lost before signing")
	}
	r.intent = "ffffffffffffffffffffffffffffffff"
	if err := r.applyURI(uri, false); err == nil {
		t.Fatal("conflicting intent accepted")
	}
	r.intent = id
	r.asset = "usd:devnet"
	if err := r.applyURI(uri, false); err == nil {
		t.Fatal("conflicting asset accepted")
	}
}

func TestIntentQRDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intent.png")
	uri := "arne:arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t?amount=1&asset=arce%3Adevnet&intent=0123456789abcdef0123456789abcdef&network=arne%3Adevnet"
	if err := writeIntentQR(path, uri); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := png.Decode(f); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIntentQR(path, "replacement"); !os.IsExist(err) {
		t.Fatalf("overwrite: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("existing QR changed")
	}
}

func TestResolveIntent(t *testing.T) {
	const payee = "arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t"
	id := protocol.IntentID("arne:devnet", payee, "invoice_42")
	intent := protocol.Intent{ID: id, Payee: payee, Reference: "invoice_42", Asset: "arce:devnet", Amount: 1250000000, Status: protocol.IntentOpen}
	network := "arne:devnet"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]string{"network": network})
		case strings.HasPrefix(r.URL.Path, "/v1/intents/"):
			_ = json.NewEncoder(w).Encode(intent)
		default:
			_ = json.NewEncoder(w).Encode(assetInfo{ID: "arce:devnet", Decimals: 8})
		}
	}))
	defer srv.Close()
	t.Setenv("ARNE_API", srv.URL)
	r := paymentRequest{network: network, intent: id}
	if err := r.resolveIntent(); err != nil {
		t.Fatal(err)
	}
	if r.to != payee || r.asset != "arce:devnet" || r.amount != "12.5" || r.uri.Intent != id {
		t.Fatalf("resolved: %+v", r)
	}
	r.amount = "13"
	if err := r.resolveIntent(); err == nil {
		t.Fatal("mismatched amount accepted")
	}
	r.amount = "12.5"
	intent.Status = protocol.IntentPaid
	if err := r.resolveIntent(); err == nil {
		t.Fatal("paid intent accepted")
	}
	intent.Status = protocol.IntentOpen
	network = "arne:mainnet"
	if err := r.resolveIntent(); err == nil {
		t.Fatal("wrong API network accepted")
	}
}

func TestURIBuildIntent(t *testing.T) {
	cmd := uriCmd()
	cmd.SetArgs([]string{"build", "--to", "arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t",
		"--asset", "arce:devnet", "--amount", "1", "--intent", "0123456789abcdef0123456789abcdef"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in       string
		decimals uint8
		want     uint64
		ok       bool
	}{
		{"12.5", 8, 1_250_000_000, true},
		{"1", 2, 100, true},
		{"0.00000001", 8, 1, true},
		{"1.230000000000000000", 8, 123_000_000, true},
		{" 7.25 ", 2, 725, true},
		{"", 8, 0, false},
		{"-1", 8, 0, false},
		{"+1", 8, 0, false},
		{"1e3", 8, 0, false},
		{"1.234", 2, 0, false},
		{"99999999999999999999", 8, 0, false},
	}
	for _, tc := range cases {
		got, err := parseAmount(tc.in, tc.decimals)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Fatalf("parseAmount(%q, %d) = %d, %v", tc.in, tc.decimals, got, err)
		}
	}
}

func TestPayRefundAutoFills(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	kp, err := protocol.KeyPairFromMnemonic("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", "", 0, 0, 0, "arne:devnet")
	if err != nil {
		t.Fatal(err)
	}
	payer := "arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t"
	submitted := make(chan protocol.Tx, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			m, err := protocol.DecodeMap(raw)
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
			submitted <- tx
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"hash": "accepted"})
			return
		}
		switch {
		case r.URL.Path == "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]string{"network": "arne:devnet"})
		case strings.HasPrefix(r.URL.Path, "/v1/transfers/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hash": hash, "from": payer, "to": kp.Address, "asset": "arce:devnet",
				"amount": uint64(500_000_000), "refund_of": "",
				"refunded": uint64(200_000_000), "refundable": uint64(300_000_000),
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "arce:devnet", "symbol": "ARCE", "decimals": 8})
		}
	}))
	defer srv.Close()
	t.Setenv("ARNE_API", srv.URL)
	cmd := payCmd()
	cmd.SetArgs([]string{"--mnemonic", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", "--refund-of", hash})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	select {
	case tx := <-submitted:
		if tx.Payload["refund_of"] != hash || tx.Payload["to"] != payer ||
			tx.Payload["asset"] != "arce:devnet" || tx.Payload["amount"] != uint64(300_000_000) {
			t.Fatalf("signed payload: %+v", tx.Payload)
		}
		if err := tx.Verify(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("refund not submitted")
	}
}

func TestPayURISubmitsIntent(t *testing.T) {
	const payee = "arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t"
	id := protocol.IntentID("arne:devnet", payee, "invoice_42")
	submitted := make(chan protocol.Tx, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			switch {
			case r.URL.Path == "/v1/status":
				_ = json.NewEncoder(w).Encode(map[string]string{"network": "arne:devnet"})
			case strings.HasPrefix(r.URL.Path, "/v1/intents/"):
				_ = json.NewEncoder(w).Encode(protocol.Intent{ID: id, Payee: payee, Reference: "invoice_42", Asset: "arce:devnet", Amount: 100000000, Status: protocol.IntentOpen, ExpiresAt: uint64(time.Now().Unix()) + 365*24*3600})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"nonce": 0, "decimals": 8, "symbol": "ARCE"})
			}
		case http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			m, err := protocol.DecodeMap(raw)
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
			submitted <- tx
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"hash": "accepted"})
		}
	}))
	defer srv.Close()
	t.Setenv("ARNE_API", srv.URL)
	cmd := payCmd()
	cmd.SetArgs([]string{"--mnemonic", "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
		"--uri", "arne:arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t?network=arne%3Adevnet&asset=arce%3Adevnet&amount=1&intent=" + id, "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	select {
	case tx := <-submitted:
		if tx.Payload["intent"] != id || tx.Payload["amount"] != uint64(100_000_000) {
			t.Fatalf("signed payload: %+v", tx.Payload)
		}
		if err := tx.Verify(); err != nil {
			t.Fatal(err)
		}
		if tx.ExpiresAt > tx.CreatedAt+3600 {
			t.Fatal("long-lived intent extended transaction validity")
		}
	default:
		t.Fatal("payment not submitted")
	}
}
