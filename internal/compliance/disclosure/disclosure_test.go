package disclosure

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/memo"
)

func seed(n byte) []byte { return bytes.Repeat([]byte{n}, 32) }

// sealedFor seals a memo deterministically so tests need no randomness.
func sealedFor(t *testing.T, recipient, plaintext string) []byte {
	t.Helper()
	pub, err := memo.PublicKey(seed(recipient[0]))
	if err != nil {
		t.Fatal(err)
	}
	rnd := bytes.NewReader(bytes.Repeat([]byte{7}, 44))
	sealed, err := memo.Seal(pub, []byte(plaintext), rnd)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func setup(t *testing.T) (*Manager, string) {
	t.Helper()
	m := NewManager()
	if err := m.AddMemo("tx-a", sealedFor(t, "alice", "memo a")); err != nil {
		t.Fatal(err)
	}
	if err := m.AddMemo("tx-b", sealedFor(t, "alice", "memo b")); err != nil {
		t.Fatal(err)
	}
	return m, "alice"
}

func userGrant(scope ...string) Grant {
	return Grant{
		Kind: User, Subject: "alice", Scope: scope,
		From: 100, Until: 200, Purpose: "invoice review",
		GrantedBy: "alice", GrantedAt: 100,
	}
}

// TestNoUniversalDecryptionPath is the 11.9 check: no caller can turn a
// transfer hash into plaintext without a grant, and a grant only reaches the
// transfers it names.
func TestNoUniversalDecryptionPath(t *testing.T) {
	m, alice := setup(t)
	key := seed('a')

	// Every memo exists, yet an operator with no grant opens nothing.
	for _, tx := range []string{"tx-a", "tx-b"} {
		if plain, err := m.Open("operator", "", tx, key, 150); !errors.Is(err, ErrNoGrant) || plain != nil {
			t.Fatalf("ungranted open of %s = %q, %v", tx, plain, err)
		}
		if plain, err := m.Open("operator", "made-up", tx, key, 150); !errors.Is(err, ErrNoGrant) || plain != nil {
			t.Fatalf("unknown grant open of %s = %q, %v", tx, plain, err)
		}
	}

	g, err := m.Grant(userGrant("tx-a"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := m.Open(alice, g.ID, "tx-a", key, 150)
	if err != nil || string(plain) != "memo a" {
		t.Fatalf("granted open = %q, %v", plain, err)
	}
	// The same grant does not reach a second memo, so it is not a master key.
	if plain, err := m.Open(alice, g.ID, "tx-b", key, 150); !errors.Is(err, ErrOutOfScope) || plain != nil {
		t.Fatalf("out-of-scope open = %q, %v", plain, err)
	}
	// And a different party cannot use someone else's grant.
	if plain, err := m.Open("mallory", g.ID, "tx-a", key, 150); !errors.Is(err, ErrForbidden) || plain != nil {
		t.Fatalf("foreign open = %q, %v", plain, err)
	}
	// A grant is not a decryption key: the wrong seed still fails.
	if plain, err := m.Open(alice, g.ID, "tx-a", seed('z'), 150); !errors.Is(err, ErrDecrypt) || plain != nil {
		t.Fatalf("wrong seed open = %q, %v", plain, err)
	}

	var denied int
	for _, e := range m.Events(0, 100) {
		if e.Result == "denied" {
			denied++
		}
	}
	if denied != 7 {
		t.Fatalf("denied attempts logged = %d, want 7", denied)
	}
}

func TestExceptionalAccessIsRecorded(t *testing.T) {
	m, _ := setup(t)
	g, err := m.Grant(Grant{
		Kind: Exceptional, Subject: "analyst", Scope: []string{"tx-a", "tx-b"},
		From: 100, Until: 500, Purpose: "court order",
		Approvers: []string{"gc-1", "gc-2"}, LegalProcess: "order-2026-17",
		GrantedBy: "gc-1", GrantedAt: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open("analyst", g.ID, "tx-a", seed('a'), 150); err != nil {
		t.Fatal(err)
	}

	events := m.Events(0, 100)
	if len(events) != 2 {
		t.Fatalf("events = %d, want grant + open", len(events))
	}
	granted, opened := events[0], events[1]
	if granted.Result != "granted" || granted.Kind != Exceptional ||
		!reflect.DeepEqual(granted.Scope, []string{"tx-a", "tx-b"}) ||
		!reflect.DeepEqual(granted.Approvers, []string{"gc-1", "gc-2"}) ||
		granted.Purpose != "court order" {
		t.Fatalf("grant event = %+v", granted)
	}
	if opened.Result != "opened" || opened.Transfer != "tx-a" || opened.Actor != "analyst" ||
		opened.Purpose != "court order" || !reflect.DeepEqual(opened.Approvers, []string{"gc-1", "gc-2"}) {
		t.Fatalf("open event = %+v", opened)
	}
}

func TestWindowAndValidation(t *testing.T) {
	m, alice := setup(t)
	g, err := m.Grant(userGrant("tx-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(alice, g.ID, "tx-a", seed('a'), 99); !errors.Is(err, ErrNotActive) {
		t.Fatalf("before window: %v", err)
	}
	if _, err := m.Open(alice, g.ID, "tx-a", seed('a'), 200); !errors.Is(err, ErrExpired) {
		t.Fatalf("at window end: %v", err)
	}
	if _, err := m.Open(alice, g.ID, "tx-missing", seed('a'), 150); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("missing memo outside scope: %v", err)
	}
	missing, err := m.Grant(userGrant("tx-missing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(alice, missing.ID, "tx-missing", seed('a'), 150); !errors.Is(err, ErrNoMemo) {
		t.Fatalf("missing memo in scope: %v", err)
	}

	bad := []Grant{
		{Kind: User, Subject: "", Scope: []string{"t"}, From: 1, Until: 2, Purpose: "p", GrantedBy: "a", GrantedAt: 1},
		{Kind: User, Subject: "a", Scope: nil, From: 1, Until: 2, Purpose: "p", GrantedBy: "a", GrantedAt: 1},
		{Kind: User, Subject: "a", Scope: []string{"t"}, From: 0, Until: 2, Purpose: "p", GrantedBy: "a", GrantedAt: 1},
		{Kind: User, Subject: "a", Scope: []string{"t"}, From: 2, Until: 2, Purpose: "p", GrantedBy: "a", GrantedAt: 1},
		{Kind: User, Subject: "a", Scope: []string{"t"}, From: 1, Until: 2, Purpose: "", GrantedBy: "a", GrantedAt: 1},
		{Kind: User, Subject: "a", Scope: []string{"t"}, From: 1, Until: 2, Purpose: "p", GrantedBy: "a"},
		{Kind: User, Subject: "a", Scope: []string{"t"}, From: 1, Until: 2, Purpose: "p", Approvers: []string{"x", "y"}, GrantedBy: "a", GrantedAt: 1},
		{Kind: Exceptional, Subject: "a", Scope: []string{"t"}, From: 1, Until: 2, Purpose: "p", Approvers: []string{"x"}, LegalProcess: "l", GrantedBy: "a", GrantedAt: 1},
		{Kind: Exceptional, Subject: "a", Scope: []string{"t"}, From: 1, Until: 2, Purpose: "p", Approvers: []string{"x", "y"}, GrantedBy: "a", GrantedAt: 1},
		{Kind: "other", Subject: "a", Scope: []string{"t"}, From: 1, Until: 2, Purpose: "p", GrantedBy: "a", GrantedAt: 1},
	}
	for i, g := range bad {
		if _, err := m.Grant(g); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad grant %d: %v", i, err)
		}
	}
	if err := m.AddMemo("", sealedFor(t, "alice", "x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty transfer: %v", err)
	}
	if err := m.AddMemo("tx-a", sealedFor(t, "alice", "x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate transfer: %v", err)
	}
	if err := m.AddMemo("tx-c", []byte("not a memo")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid memo: %v", err)
	}
}

func TestGrantIDIsDeterministic(t *testing.T) {
	m := NewManager()
	g, err := m.Grant(userGrant("tx-b", "tx-a", "tx-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Scope, []string{"tx-a", "tx-b"}) {
		t.Fatalf("scope not normalized: %v", g.Scope)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "disclosure.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		GrantIDCases []struct {
			Kind      Kind     `json:"kind"`
			Subject   string   `json:"subject"`
			Scope     []string `json:"scope"`
			From      int64    `json:"from"`
			Until     int64    `json:"until"`
			Purpose   string   `json:"purpose"`
			Approvers []string `json:"approvers"`
			ID        string   `json:"id"`
		} `json:"grant_id_cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.GrantIDCases {
		got := grantID(Grant{Kind: c.Kind, Subject: c.Subject, Scope: c.Scope, From: c.From,
			Until: c.Until, Purpose: c.Purpose, Approvers: c.Approvers})
		if got != c.ID {
			t.Fatalf("grant id = %s, want %s", got, c.ID)
		}
	}
}
