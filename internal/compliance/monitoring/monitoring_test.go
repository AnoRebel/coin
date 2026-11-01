package monitoring

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type vectorDoc struct {
	FreezeAuthorized []string      `json:"freeze_authorized"`
	Rules            []Rule        `json:"rules"`
	Transactions     []Transaction `json:"transactions"`
	ExpectAlerts     []struct {
		ID   string `json:"id"`
		Rule string `json:"rule"`
		TxID string `json:"tx_id"`
	} `json:"expect_alerts"`
	ExpectFreeze    map[string]bool `json:"expect_freeze"`
	ExpectEscalated []string        `json:"expect_escalated"`
	Disposition     struct {
		Alert    string `json:"alert"`
		Action   string `json:"action"`
		Reason   string `json:"reason"`
		Reviewer string `json:"reviewer"`
		At       int64  `json:"at"`
	} `json:"disposition"`
}

func load(t *testing.T) vectorDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "monitoring.json"))
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
	m, err := NewMonitor(doc.Rules, doc.FreezeAuthorized)
	if err != nil {
		t.Fatal(err)
	}
	var got []Alert
	for _, tx := range doc.Transactions {
		alerts, err := m.Evaluate(tx)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, alerts...)
	}
	if len(got) != len(doc.ExpectAlerts) {
		t.Fatalf("alerts = %d, want %d", len(got), len(doc.ExpectAlerts))
	}
	escalated := map[string]bool{}
	for _, key := range doc.ExpectEscalated {
		escalated[key] = true
	}
	for i, want := range doc.ExpectAlerts {
		a := got[i]
		if a.ID != want.ID || a.Rule != want.Rule || a.TxID != want.TxID {
			t.Fatalf("alert %d = %+v, want %+v", i, a, want)
		}
		key := a.Rule + "/" + a.TxID
		if a.FreezeApplied != doc.ExpectFreeze[key] {
			t.Fatalf("%s freeze_applied = %v", key, a.FreezeApplied)
		}
		if a.Escalated != escalated[key] {
			t.Fatalf("%s escalated = %v", key, a.Escalated)
		}
		if a.FreezeApplied && a.Escalated {
			t.Fatalf("%s both froze and escalated", key)
		}
	}

	closed, err := m.Dispose(doc.Disposition.Alert, doc.Disposition.Action, doc.Disposition.Reason, doc.Disposition.Reviewer, doc.Disposition.At)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != "closed" || closed.Disposition != Report ||
		closed.Reviewer != doc.Disposition.Reviewer || closed.ClosedAt != doc.Disposition.At {
		t.Fatalf("disposition = %+v", closed)
	}
	if _, err := m.Dispose(doc.Disposition.Alert, Report, "again", "ana", 2001); !errors.Is(err, ErrClosed) {
		t.Fatalf("second disposition: %v", err)
	}
}

func TestFreezeNeedsPolicyAndEscalates(t *testing.T) {
	rules := []Rule{
		{ID: "big", Type: Threshold, Amount: 100, Severity: High, Freeze: true, Enabled: true},
		{ID: "allowed", Type: Threshold, Amount: 100, Severity: High, Freeze: true, Enabled: true},
	}
	m, err := NewMonitor(rules, []string{"allowed"})
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := m.Evaluate(Transaction{ID: "t1", From: "a", Asset: "arce:devnet", Amount: 500, At: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 {
		t.Fatalf("alerts = %d", len(alerts))
	}
	byRule := map[string]Alert{}
	for _, a := range alerts {
		byRule[a.Rule] = a
	}
	if byRule["big"].FreezeApplied || !byRule["big"].Escalated {
		t.Fatalf("unauthorized freeze: %+v", byRule["big"])
	}
	if !byRule["allowed"].FreezeApplied || byRule["allowed"].Escalated {
		t.Fatalf("authorized freeze: %+v", byRule["allowed"])
	}
	if _, err := NewMonitor([]Rule{{ID: "x", Type: Threshold, Amount: 1, Severity: Low, Enabled: true}}, []string{"y"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown freeze authority: %v", err)
	}
}

func TestEvaluateIsIdempotentAndVelocityCounts(t *testing.T) {
	m, err := NewMonitor([]Rule{
		{ID: "v", Type: Velocity, Count: 2, Window: 60, Severity: Medium, Enabled: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if alerts, err := m.Evaluate(Transaction{ID: "t1", From: "a", Asset: "a", Amount: 1, At: 100}); err != nil || len(alerts) != 0 {
		t.Fatalf("first = %v, %v", alerts, err)
	}
	again, err := m.Evaluate(Transaction{ID: "t1", From: "a", Asset: "a", Amount: 1, At: 100})
	if err != nil || len(again) != 0 {
		t.Fatalf("duplicate = %v, %v", again, err)
	}
	alerts, err := m.Evaluate(Transaction{ID: "t2", From: "a", Asset: "a", Amount: 1, At: 130})
	if err != nil || len(alerts) != 1 {
		t.Fatalf("second = %v, %v", alerts, err)
	}
	if alerts[0].Reason != "2 transfers in 60s" {
		t.Fatalf("reason = %q", alerts[0].Reason)
	}
	// A transfer outside the window does not count toward the burst.
	alerts, err = m.Evaluate(Transaction{ID: "t3", From: "b", Asset: "a", Amount: 1, At: 500})
	if err != nil || len(alerts) != 0 {
		t.Fatalf("outside window = %v, %v", alerts, err)
	}
}

func TestValidation(t *testing.T) {
	bad := [][]Rule{
		{{ID: "", Type: Threshold, Amount: 1, Severity: Low, Enabled: true}},
		{{ID: "a", Type: "other", Amount: 1, Severity: Low, Enabled: true}},
		{{ID: "a", Type: Threshold, Amount: 0, Severity: Low, Enabled: true}},
		{{ID: "a", Type: Threshold, Amount: 1, Severity: "critical", Enabled: true}},
		{{ID: "a", Type: Velocity, Count: 1, Window: 10, Severity: Low, Enabled: true}},
		{{ID: "a", Type: Velocity, Count: 2, Window: 0, Severity: Low, Enabled: true}},
		{{ID: "a", Type: Threshold, Amount: 1, Severity: Low, Enabled: true},
			{ID: "a", Type: Threshold, Amount: 1, Severity: Low, Enabled: true}},
	}
	for i, rules := range bad {
		if _, err := NewMonitor(rules, nil); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrDuplicate) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	m, err := NewMonitor(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Evaluate(Transaction{ID: "", From: "a", Asset: "a", Amount: 1, At: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty tx id: %v", err)
	}
	if _, err := m.Dispose("missing", Report, "r", "ana", 1); !errors.Is(err, ErrUnknown) {
		t.Fatalf("missing alert: %v", err)
	}
	if _, err := m.Dispose("x", "shred", "r", "ana", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown action: %v", err)
	}
}
