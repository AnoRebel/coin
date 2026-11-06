package governance

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type treasuryVectors struct {
	Limits []treasuryLimit `json:"limits"`
	Steps  []treasuryStep  `json:"steps"`
}

type treasuryLimit struct {
	Category string `json:"category"`
	Asset    string `json:"asset"`
	Limit    uint64 `json:"limit"`
}

type treasuryStep struct {
	Op        string         `json:"op"`
	Proposal  string         `json:"proposal"`
	Asset     string         `json:"asset"`
	Amount    uint64         `json:"amount"`
	Source    string         `json:"source"`
	Recipient string         `json:"recipient"`
	Purpose   string         `json:"purpose"`
	Category  string         `json:"category"`
	At        uint64         `json:"at"`
	From      uint64         `json:"from"`
	To        uint64         `json:"to"`
	Error     string         `json:"error"`
	Expect    treasuryReport `json:"expect"`
}

type treasuryReport struct {
	Inflows    map[string]uint64 `json:"inflows"`
	Outflows   map[string]uint64 `json:"outflows"`
	Categories map[string]uint64 `json:"categories"`
	Opening    map[string]uint64 `json:"opening"`
	Closing    map[string]uint64 `json:"closing"`
}

func TestTreasuryVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/treasury.json")
	if err != nil {
		t.Fatal(err)
	}
	var file treasuryVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	treasury := NewTreasuryBook()
	for _, limit := range file.Limits {
		if err := treasury.SetBudget(limit.Category, limit.Asset, limit.Limit); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range file.Steps {
		var stepErr error
		switch step.Op {
		case "deposit":
			_, stepErr = treasury.Deposit(step.Asset, step.Amount, step.Source, step.Purpose, step.At)
		case "spend":
			_, stepErr = treasury.Spend(step.Proposal, step.Asset, step.Amount, step.Recipient, step.Purpose, step.Category, step.At)
		case "report":
			var got TreasuryReport
			got, stepErr = treasury.Report(step.From, step.To)
			if stepErr == nil {
				want := TreasuryReport{
					Inflows: step.Expect.Inflows, Outflows: step.Expect.Outflows,
					Categories: step.Expect.Categories,
					Opening:    step.Expect.Opening, Closing: step.Expect.Closing,
				}
				if !reflect.DeepEqual(want, TreasuryReport{
					Inflows: got.Inflows, Outflows: got.Outflows,
					Categories: got.Categories,
					Opening:    got.Opening, Closing: got.Closing,
				}) {
					t.Fatalf("%s: want %+v, got %+v", step.Op, want, got)
				}
			}
		default:
			t.Fatalf("unknown op %q", step.Op)
		}
		if step.Error != "" && codeOf(stepErr) != step.Error {
			t.Fatalf("%s %+v: want %s, got %v", step.Op, step, step.Error, stepErr)
		}
		if step.Error == "" && stepErr != nil {
			t.Fatalf("%s %+v: %v", step.Op, step, stepErr)
		}
	}
}
