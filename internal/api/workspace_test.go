package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/workspace"
)

func newWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	w, err := workspace.New(
		map[string]workspace.Role{
			"alice": workspace.RoleOwner, "bob": workspace.RoleAdmin,
			"carol": workspace.RoleMember, "dave": workspace.RoleAuditor,
		},
		map[string]string{
			"key-alice": "alice", "key-bob": "bob", "key-carol": "carol", "key-dave": "dave",
		},
		filepath.Join(t.TempDir(), "audit.jsonl"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func wsRequest(t *testing.T, e http.Handler, method, path, key string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	return w
}

func TestWorkspaceRoleEnforcement(t *testing.T) {
	n, funder := newTestNode(t, 0)
	ws := newWorkspace(t)
	e := New(n, Config{Workspace: ws})

	if w := wsRequest(t, e, http.MethodGet, "/v1/workspace", "key-carol", nil); w.Code != http.StatusOK {
		t.Fatalf("self view: %d %s", w.Code, w.Body)
	} else {
		var view struct {
			Actor string `json:"actor"`
			Role  string `json:"role"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.Actor != "carol" || view.Role != "member" {
			t.Fatalf("self view = %s", w.Body)
		}
	}
	if w := wsRequest(t, e, http.MethodGet, "/v1/workspace", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", w.Code, w.Body)
	}

	body := []byte(`{"role":"admin"}`)
	if w := wsRequest(t, e, http.MethodPut, "/v1/workspace/members/bob", "key-carol", body); w.Code != http.StatusForbidden {
		t.Fatalf("member changed role: %d %s", w.Code, w.Body)
	}
	if w := wsRequest(t, e, http.MethodPut, "/v1/workspace/members/bob", "key-alice", body); w.Code != http.StatusOK {
		t.Fatalf("owner changed role: %d %s", w.Code, w.Body)
	}
	if w := wsRequest(t, e, http.MethodDelete, "/v1/workspace/members/bob", "key-carol", nil); w.Code != http.StatusForbidden {
		t.Fatalf("member removed admin: %d %s", w.Code, w.Body)
	}
	if w := wsRequest(t, e, http.MethodDelete, "/v1/workspace/members/carol", "key-bob", nil); w.Code != http.StatusNoContent {
		t.Fatalf("admin removed member: %d %s", w.Code, w.Body)
	}

	// Auditor can read payments and the audit log but cannot pay.
	if w := wsRequest(t, e, http.MethodGet, "/v1/transfers/missing", "key-dave", nil); w.Code != http.StatusNotFound {
		t.Fatalf("auditor read: %d %s", w.Code, w.Body)
	}
	if w := wsRequest(t, e, http.MethodGet, "/v1/workspace/audit", "key-dave", nil); w.Code != http.StatusOK {
		t.Fatalf("auditor audit: %d %s", w.Code, w.Body)
	}
	if w := wsRequest(t, e, http.MethodPost, "/v1/transactions", "key-dave", []byte{0xa0}); w.Code != http.StatusForbidden {
		t.Fatalf("auditor paid: %d %s", w.Code, w.Body)
	}

	// Member payment is accepted and audited with the member ID, not the key.
	now := uint64(time.Now().Unix())
	native := protocol.NativeAsset(testNetwork)
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: testNetwork, Chain: "arne-devnet-1",
		Type: protocol.TxTransfer, Signer: funder.Address, CreatedAt: now, ExpiresAt: now + 3600,
		FeeAsset: native, MaxFee: 10_000,
		Payload: map[string]any{"asset": native, "to": funder.Address, "amount": uint64(42)},
	}
	if err := tx.Sign(funder); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	if w := wsRequest(t, e, http.MethodPost, "/v1/transactions", "key-bob", raw); w.Code != http.StatusAccepted {
		t.Fatalf("admin payment: %d %s", w.Code, w.Body)
	}

	entries := ws.Entries(0, 100)
	var denied, paid bool
	for _, entry := range entries {
		if entry.Actor == "carol" && entry.Target == "bob" && entry.Result == "denied" {
			denied = true
		}
		if entry.Actor == "bob" && entry.Action == workspace.ActionPaymentWrite && entry.Result == "accepted" {
			paid = true
		}
		if entry.Actor == "key-bob" || entry.Actor == "key-carol" {
			t.Fatalf("audit leaked an API key: %+v", entry)
		}
		if entry.Time == 0 {
			t.Fatalf("audit entry without a time: %+v", entry)
		}
	}
	if !denied || !paid {
		t.Fatalf("audit entries missing: %+v", entries)
	}
}

func TestWorkspaceEndpointsDisabledWithoutConfig(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{})
	if w := wsRequest(t, e, http.MethodGet, "/v1/workspace", "any", nil); w.Code != http.StatusNotFound {
		t.Fatalf("workspace without config: %d %s", w.Code, w.Body)
	}
}
