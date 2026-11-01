package access

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type vectorCase struct {
	Auditor string `json:"auditor"`
	Asset   string `json:"asset"`
	At      int64  `json:"at"`
	Result  string `json:"result"`
	Grant   string `json:"grant"`
}

func loadVectors(t *testing.T) ([]Grant, []vectorCase, []Grant) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "audit-grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Grants  []Grant      `json:"grants"`
		Cases   []vectorCase `json:"cases"`
		IDCases []Grant      `json:"id_cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Grants, doc.Cases, doc.IDCases
}

func TestSharedVectors(t *testing.T) {
	grants, cases, idCases := loadVectors(t)
	for _, g := range idCases {
		if got := g.id(); got != g.ID {
			t.Fatalf("grant id = %s, want %s", got, g.ID)
		}
	}
	r := New("")
	for _, g := range grants {
		if err := g.validate(); err != nil {
			t.Fatalf("grant %s: %v", g.ID, err)
		}
		r.grants[g.ID] = g
	}
	for _, c := range cases {
		g, err := r.Check(c.Auditor, c.Asset, c.At)
		if c.Result == "allowed" {
			if err != nil {
				t.Fatalf("%s %s at %d: %v", c.Auditor, c.Asset, c.At, err)
			}
			if g.ID != c.Grant {
				t.Fatalf("%s %s at %d: grant = %s, want %s", c.Auditor, c.Asset, c.At, g.ID, c.Grant)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s %s at %d: allowed, want %s", c.Auditor, c.Asset, c.At, c.Result)
		}
		if c.Grant != "" && g.ID != "" && g.ID != c.Grant {
			t.Fatalf("%s %s at %d: wrong grant %s", c.Auditor, c.Asset, c.At, g.ID)
		}
	}
	// Every check is logged, including denials.
	events := r.Events(0, 1000)
	if len(events) != len(cases) {
		t.Fatalf("events = %d, want %d", len(events), len(cases))
	}
	for _, e := range events {
		if e.Action != "access" || e.Seq == 0 || e.Time == 0 {
			t.Fatalf("bad event: %+v", e)
		}
	}
}

func TestGrantLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := r.Grant(Grant{
		Auditor: " dave ", Assets: []string{"usd:devnet", "usd:devnet", " eur:devnet "},
		From: 100, Until: 200, Purpose: "quarterly audit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == "" || len(g.ID) != 32 {
		t.Fatalf("id = %q", g.ID)
	}
	if len(g.Assets) != 2 || g.Assets[0] != "eur:devnet" {
		t.Fatalf("assets not normalized: %v", g.Assets)
	}
	if again, err := r.Grant(g); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate grant: %v", err)
	} else if again.ID != "" {
		t.Fatal("duplicate returned a grant")
	}
	if _, err := r.Check("dave", "usd:devnet", 150); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.List("dave")) != 1 {
		t.Fatalf("grant not persisted: %+v", reloaded.List(""))
	}
	if len(reloaded.List("erin")) != 0 {
		t.Fatal("filter ignored")
	}
	if len(reloaded.Events(0, 100)) != 2 { // created + allowed access
		t.Fatalf("log not reloaded: %+v", reloaded.Events(0, 100))
	}

	if err := reloaded.Revoke("missing"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("revoke unknown: %v", err)
	}
	if err := reloaded.Revoke(g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Check("dave", "usd:devnet", 150); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("check after revoke: %v", err)
	}
	after, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.List("")) != 0 {
		t.Fatal("revoke not persisted")
	}
}

func TestGrantValidation(t *testing.T) {
	base := Grant{Auditor: "dave", Assets: []string{"usd:devnet"}, From: 1, Until: 2, Purpose: "audit"}
	cases := []Grant{
		{Auditor: "", Assets: base.Assets, From: 1, Until: 2, Purpose: "audit"},
		{Auditor: "dave", Assets: nil, From: 1, Until: 2, Purpose: "audit"},
		{Auditor: "dave", Assets: base.Assets, From: 2, Until: 2, Purpose: "audit"},
		{Auditor: "dave", Assets: base.Assets, From: -1, Until: 2, Purpose: "audit"},
		{Auditor: "dave", Assets: base.Assets, From: 1, Until: 0, Purpose: "audit"},
		{Auditor: "dave", Assets: base.Assets, From: 1, Until: 2, Purpose: ""},
	}
	for i, c := range cases {
		if _, err := New("").Grant(c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	if _, err := New("").Grant(base); err != nil {
		t.Fatal(err)
	}
}
