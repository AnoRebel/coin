package retention

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type vectorDoc struct {
	Policies []Policy `json:"policies"`
	Records  []Record `json:"records"`
	Holds    []struct {
		Record   string `json:"record"`
		Reason   string `json:"reason"`
		PlacedBy string `json:"placed_by"`
		PlacedAt int64  `json:"placed_at"`
	} `json:"holds"`
	HoldIDCases []struct {
		Record   string `json:"record"`
		Reason   string `json:"reason"`
		PlacedBy string `json:"placed_by"`
		PlacedAt int64  `json:"placed_at"`
		ID       string `json:"id"`
	} `json:"hold_id_cases"`
	Now    int64 `json:"now"`
	Expect struct {
		Deleted    []string `json:"deleted"`
		Anonymized []string `json:"anonymized"`
		Retained   []string `json:"retained"`
		Held       []string `json:"held"`
		Tombstone  struct {
			Record      string `json:"record"`
			Class       string `json:"class"`
			LedgerRef   string `json:"ledger_ref"`
			Action      Action `json:"action"`
			DeletedAt   int64  `json:"deleted_at"`
			ContentHash string `json:"content_hash"`
		} `json:"tombstone"`
		AnonymizedFields map[string]string `json:"anonymized_fields"`
		Events           map[string]struct {
			Action string `json:"action"`
			Result string `json:"result"`
		} `json:"events"`
	} `json:"expect"`
}

func load(t *testing.T) vectorDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "retention.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc vectorDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSharedVectors(t *testing.T) {
	doc := load(t)
	m, err := NewManager(doc.Policies)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range doc.Records {
		if err := m.Add(r); err != nil {
			t.Fatalf("add %s: %v", r.ID, err)
		}
	}
	for _, h := range doc.Holds {
		if _, err := m.Hold(Hold{Record: h.Record, Reason: h.Reason, PlacedBy: h.PlacedBy, PlacedAt: h.PlacedAt}); err != nil {
			t.Fatalf("hold %s: %v", h.Record, err)
		}
	}
	for _, c := range doc.HoldIDCases {
		if got := holdID(Hold{Record: c.Record, Reason: c.Reason, PlacedBy: c.PlacedBy, PlacedAt: c.PlacedAt}); got != c.ID {
			t.Fatalf("hold id = %s, want %s", got, c.ID)
		}
	}
	res := m.Apply(doc.Now)
	for name, got := range map[string][]string{
		"deleted": res.Deleted, "anonymized": res.Anonymized,
		"retained": res.Retained, "held": res.Held,
	} {
		var want []string
		switch name {
		case "deleted":
			want = doc.Expect.Deleted
		case "anonymized":
			want = doc.Expect.Anonymized
		case "retained":
			want = doc.Expect.Retained
		case "held":
			want = doc.Expect.Held
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}

	if len(res.Tombstones) != 1 {
		t.Fatalf("tombstones = %d", len(res.Tombstones))
	}
	ts := res.Tombstones[0]
	want := doc.Expect.Tombstone
	if ts.Record != want.Record || ts.Class != want.Class || ts.LedgerRef != want.LedgerRef ||
		ts.Action != want.Action || ts.DeletedAt != want.DeletedAt || ts.ContentHash != want.ContentHash {
		t.Fatalf("tombstone = %+v, want %+v", ts, want)
	}
	if _, ok := m.Record(want.Record); ok {
		t.Fatal("deleted record still present")
	}

	anonymized, ok := m.Record("r-old-support")
	if !ok {
		t.Fatal("anonymized record removed")
	}
	for field, value := range doc.Expect.AnonymizedFields {
		if anonymized.Fields[field] != value {
			t.Fatalf("field %q = %q, want %q", field, anonymized.Fields[field], value)
		}
	}
	if anonymized.LedgerRef != "tx-support" {
		t.Fatalf("ledger ref lost: %+v", anonymized)
	}

	for _, event := range m.Events(0, 100) {
		switch event.Record {
		case "r-old-kyc":
			if event.Action != "delete" || event.Result != "deleted" {
				t.Fatalf("delete event: %+v", event)
			}
		case "r-old-support":
			if event.Action != "anonymize" || event.Result != "anonymized" {
				t.Fatalf("anonymize event: %+v", event)
			}
		case "r-held":
			if event.Action == "hold" {
				continue
			}
			if event.Action != "retain" || event.Result != "held" {
				t.Fatalf("held event: %+v", event)
			}
		case "r-no-policy":
			if event.Action != "retain" || event.Result != "retained" {
				t.Fatalf("no-policy event: %+v", event)
			}
		case "r-new-kyc":
			if event.Action != "retain" || event.Result != "retained" {
				t.Fatalf("within-retention event: %+v", event)
			}
		}
	}
}

func TestReleaseThenDelete(t *testing.T) {
	m, err := NewManager([]Policy{{Class: "logs", Days: 1, Action: Delete}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Add(Record{ID: "r1", Class: "logs", CreatedAt: 1000}); err != nil {
		t.Fatal(err)
	}
	h, err := m.Hold(Hold{Record: "r1", Reason: "case", PlacedBy: "legal", PlacedAt: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Apply(1_000_000).Held) != 1 {
		t.Fatal("hold ignored")
	}
	if err := m.Release(h.ID, "legal", 1_000_001); err != nil {
		t.Fatal(err)
	}
	res := m.Apply(1_000_002)
	if !reflect.DeepEqual(res.Deleted, []string{"r1"}) {
		t.Fatalf("deleted after release: %+v", res)
	}
	var released bool
	for _, e := range m.Events(0, 100) {
		if e.Action == "release" && e.Result == "released" {
			released = true
		}
	}
	if !released {
		t.Fatal("release not logged")
	}
}

func TestValidation(t *testing.T) {
	bad := [][]Policy{
		{{Class: "", Days: 1, Action: Delete}},
		{{Class: "a", Days: 0, Action: Delete}},
		{{Class: "a", Days: 1, Action: "shred"}},
		{{Class: "a", Days: 1, Action: Anonymize}},
		{{Class: "a", Days: 1, Action: Delete, AnonymizeFields: []string{"x"}}},
		{{Class: "a", Days: 1, Action: Delete}, {Class: "a", Days: 2, Action: Delete}},
	}
	for i, policies := range bad {
		if _, err := NewManager(policies); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	m, err := NewManager([]Policy{{Class: "a", Days: 1, Action: Delete}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Add(Record{ID: "r", Class: "a", CreatedAt: 1, OnChain: true}); !errors.Is(err, ErrOnChain) {
		t.Fatalf("on-chain: %v", err)
	}
	if err := m.Add(Record{ID: "", Class: "a", CreatedAt: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing id: %v", err)
	}
	if err := m.Add(Record{ID: "r", Class: "a", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(Record{ID: "r", Class: "a", CreatedAt: 1}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := m.Hold(Hold{Record: "missing", Reason: "x", PlacedBy: "y", PlacedAt: 1}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("hold missing record: %v", err)
	}
	if err := m.Release("missing", "y", 1); !errors.Is(err, ErrUnknown) {
		t.Fatalf("release missing: %v", err)
	}
}
