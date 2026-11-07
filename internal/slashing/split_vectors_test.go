package slashing

import (
	"encoding/json"
	"os"
	"testing"
)

type splitVectors struct {
	Cases []splitCase `json:"cases"`
}

type splitCase struct {
	Name        string `json:"name"`
	Slashed     uint64 `json:"slashed"`
	ReporterBps uint32 `json:"reporter_bps"`
	ReporterCap uint64 `json:"reporter_cap"`
	BurnBps     uint32 `json:"burn_bps"`
	Expect      Split  `json:"expect"`
}

func TestSplitVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/slashsplit.json")
	if err != nil {
		t.Fatal(err)
	}
	var file splitVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, test := range file.Cases {
		split := SplitPenalty(test.Slashed, Params{
			ReporterBps: test.ReporterBps, ReporterCap: test.ReporterCap,
			BurnBps: test.BurnBps,
		})
		if split != test.Expect {
			t.Fatalf("%s: got %+v, want %+v", test.Name, split, test.Expect)
		}
		if split.Burn+split.Reporter+split.Treasury != test.Slashed {
			t.Fatalf("%s: parts do not sum to %d", test.Name, test.Slashed)
		}
	}
}
