package economics

import (
	"encoding/json"
	"os"
	"testing"
)

type reserveVectors struct {
	Asset   string        `json:"asset"`
	Rate    uint64        `json:"rate"`
	Invalid []reserveCase `json:"invalid"`
	Steps   []reserveStep `json:"steps"`
}

type reserveCase struct {
	Name  string `json:"name"`
	Asset string `json:"asset"`
	Rate  uint64 `json:"rate"`
	Error string `json:"error"`
}

type reserveStep struct {
	Op                  string        `json:"op"`
	Amount              uint64        `json:"amount"`
	AssetAmount         uint64        `json:"asset_amount"`
	MinARCE             uint64        `json:"min_arce"`
	At                  uint64        `json:"at"`
	ObservedReserves    uint64        `json:"observed_reserves"`
	ObservedLiabilities uint64        `json:"observed_liabilities"`
	Digest              string        `json:"digest"`
	Error               string        `json:"error"`
	Expect              reserveExpect `json:"expect"`
}

type reserveExpect struct {
	ARCE     uint64 `json:"arce"`
	Reserves uint64 `json:"reserves"`
	Spare    uint64 `json:"spare"`
	Covered  *bool  `json:"covered"`
	Disabled *bool  `json:"disabled"`
}

func TestReserveVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/reserve.json")
	if err != nil {
		t.Fatal(err)
	}
	var file reserveVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, test := range file.Invalid {
		if _, err := NewReserve(test.Asset, test.Rate); codeOf(err) != test.Error {
			t.Fatalf("%s: want %s, got %v", test.Name, test.Error, err)
		}
	}
	route, err := NewReserve(file.Asset, file.Rate)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range file.Steps {
		var stepErr error
		var arce uint64
		switch step.Op {
		case "set_reserves":
			route.SetReserves(step.Amount)
		case "set_liabilities":
			route.SetLiabilities(step.Amount)
		case "recapitalize":
			route.Recapitalize(step.Amount)
		case "quote":
			arce, stepErr = route.Quote(step.AssetAmount, step.MinARCE)
		case "verify":
			proof := route.Commit(step.At)
			if step.Digest != "" {
				proof.Digest = step.Digest
			}
			stepErr = proof.Verify(step.ObservedReserves, step.ObservedLiabilities)
		default:
			t.Fatalf("unknown op %q", step.Op)
		}
		if step.Error != "" && codeOf(stepErr) != step.Error {
			t.Fatalf("%s %+v: want %s, got %v", step.Op, step, step.Error, stepErr)
		}
		if step.Error == "" && stepErr != nil {
			t.Fatalf("%s %+v: %v", step.Op, step, stepErr)
		}
		if step.Expect.ARCE != 0 && arce != step.Expect.ARCE {
			t.Fatalf("%s: want %d, got %d", step.Op, step.Expect.ARCE, arce)
		}
		if step.Expect.Reserves != 0 && route.Reserves != step.Expect.Reserves {
			t.Fatalf("%s: want reserves %d, got %d", step.Op, step.Expect.Reserves, route.Reserves)
		}
		if step.Expect.Spare != 0 && route.Spare() != step.Expect.Spare {
			t.Fatalf("%s: want spare %d, got %d", step.Op, step.Expect.Spare, route.Spare())
		}
		if step.Expect.Covered != nil && route.Covered() != *step.Expect.Covered {
			t.Fatalf("%s: covered %v", step.Op, route.Covered())
		}
		if step.Expect.Disabled != nil && route.Disabled() != *step.Expect.Disabled {
			t.Fatalf("%s: disabled %v", step.Op, route.Disabled())
		}
	}
}
