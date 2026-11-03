package reconcile

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type vectorFile struct {
	Policy Policy `json:"policy"`
	Cases  []struct {
		Name         string       `json:"name"`
		Kind         string       `json:"kind"`
		Now          int64        `json:"now"`
		Operation    Operation    `json:"operation"`
		Confirmation Confirmation `json:"confirmation"`
		Correlation  string       `json:"correlation"`
		Expect       struct {
			Outcome        string   `json:"outcome"`
			Status         string   `json:"status"`
			Amount         uint64   `json:"amount"`
			Reference      string   `json:"reference"`
			Correlation    string   `json:"correlation"`
			DetailContains string   `json:"detail_contains"`
			Matched        int      `json:"matched"`
			Unmatched      int      `json:"unmatched"`
			Conflicting    int      `json:"conflicting"`
			Totals         []Total  `json:"totals"`
			Exceptions     []string `json:"exceptions"`
			Found          *bool    `json:"found"`
		} `json:"expect"`
	} `json:"cases"`
}

func load(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/vectors/reconcile.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestVectors(t *testing.T) {
	file := load(t)
	ledger := New(file.Policy)
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Kind {
			case "submit":
				state, outcome := ledger.Submit(c.Operation)
				if string(outcome) != c.Expect.Outcome {
					t.Fatalf("outcome = %s, want %s", outcome, c.Expect.Outcome)
				}
				if c.Expect.Status != "" && string(state.Status) != c.Expect.Status {
					t.Fatalf("status = %s, want %s", state.Status, c.Expect.Status)
				}
				if c.Expect.Amount != 0 && state.Operation.Amount != c.Expect.Amount {
					t.Fatalf("amount = %d, want %d", state.Operation.Amount, c.Expect.Amount)
				}
				if c.Expect.Correlation != "" && state.Operation.Correlation != c.Expect.Correlation {
					t.Fatalf("correlation = %s, want %s", state.Operation.Correlation, c.Expect.Correlation)
				}
			case "confirm":
				state, outcome := ledger.Confirm(c.Confirmation, c.Now)
				if string(outcome) != c.Expect.Outcome {
					t.Fatalf("outcome = %s, want %s", outcome, c.Expect.Outcome)
				}
				if c.Expect.Status != "" && string(state.Status) != c.Expect.Status {
					t.Fatalf("status = %s, want %s", state.Status, c.Expect.Status)
				}
				if c.Expect.Reference != "" && state.Reference != c.Expect.Reference {
					t.Fatalf("reference = %s, want %s", state.Reference, c.Expect.Reference)
				}
				if c.Expect.DetailContains != "" && !strings.Contains(state.ConflictDetail, c.Expect.DetailContains) {
					t.Fatalf("detail = %q, want substring %q", state.ConflictDetail, c.Expect.DetailContains)
				}
			case "reconcile":
				report := ledger.Reconcile(c.Now)
				if report.Matched != c.Expect.Matched || report.Unmatched != c.Expect.Unmatched ||
					report.Conflicting != c.Expect.Conflicting {
					t.Fatalf("counts = %d/%d/%d, want %d/%d/%d",
						report.Matched, report.Unmatched, report.Conflicting,
						c.Expect.Matched, c.Expect.Unmatched, c.Expect.Conflicting)
				}
				if !reflect.DeepEqual(report.Totals, c.Expect.Totals) {
					t.Fatalf("totals = %+v, want %+v", report.Totals, c.Expect.Totals)
				}
				kinds := make([]string, 0, len(report.Exceptions))
				for _, exception := range report.Exceptions {
					kinds = append(kinds, exception.Kind)
				}
				if !reflect.DeepEqual(kinds, c.Expect.Exceptions) {
					t.Fatalf("exceptions = %v, want %v", kinds, c.Expect.Exceptions)
				}
			case "state":
				state, ok := ledger.State(c.Correlation)
				if c.Expect.Found != nil && *c.Expect.Found != ok {
					t.Fatalf("found = %v, want %v", ok, *c.Expect.Found)
				}
				if !ok {
					return
				}
				if string(state.Status) != c.Expect.Status {
					t.Fatalf("status = %s, want %s", state.Status, c.Expect.Status)
				}
				if state.Operation.Amount != c.Expect.Amount {
					t.Fatalf("amount = %d, want %d", state.Operation.Amount, c.Expect.Amount)
				}
				if state.Reference != c.Expect.Reference {
					t.Fatalf("reference = %s, want %s", state.Reference, c.Expect.Reference)
				}
			default:
				t.Fatalf("unknown kind %q", c.Kind)
			}
		})
	}
}

func TestReconcileKeepsPendingWhenPolicyAllows(t *testing.T) {
	ledger := New(Policy{FailOnTimeout: false})
	operation := Operation{
		Key: "k", Correlation: "c", Asset: "arce:testnet/arce",
		Amount: 10, CreatedAt: 100, Timeout: 10,
	}
	if _, outcome := ledger.Submit(operation); outcome != Accepted {
		t.Fatalf("submit = %s", outcome)
	}
	report := ledger.Reconcile(200)
	if report.Unmatched != 1 || report.Totals[0].Pending != 10 {
		t.Fatalf("report = %+v, want one pending operation", report)
	}
	state, ok := ledger.State("c")
	if !ok || state.Status != Pending {
		t.Fatalf("status = %s, want pending", state.Status)
	}
	if _, outcome := ledger.Confirm(Confirmation{
		Correlation: "c", Asset: "arce:testnet/arce", Amount: 10,
		Reference: "0xaa", At: 201,
	}, 201); outcome != Accepted {
		t.Fatalf("late confirmation under a keep-pending policy = %s, want accepted", outcome)
	}
}

func TestConflictFreezesOnce(t *testing.T) {
	ledger := New(Policy{FailOnTimeout: true})
	operation := Operation{
		Key: "k", Correlation: "c", Asset: "arce:testnet/arce",
		Amount: 10, CreatedAt: 0, Timeout: 100,
	}
	if _, outcome := ledger.Submit(operation); outcome != Accepted {
		t.Fatalf("submit = %s", outcome)
	}
	if _, outcome := ledger.Confirm(Confirmation{
		Correlation: "c", Asset: "arce:testnet/arce", Amount: 9,
		Reference: "0xaa", At: 1,
	}, 1); outcome != Conflict {
		t.Fatalf("mismatch = %s, want conflict", outcome)
	}
	if _, outcome := ledger.Confirm(Confirmation{
		Correlation: "c", Asset: "arce:testnet/arce", Amount: 10,
		Reference: "0xaa", At: 2,
	}, 2); outcome != Conflict {
		t.Fatalf("matching confirmation after conflict = %s, want conflict", outcome)
	}
	report := ledger.Reconcile(3)
	if report.Conflicting != 1 || report.Matched != 0 {
		t.Fatalf("report = %+v", report)
	}
}
