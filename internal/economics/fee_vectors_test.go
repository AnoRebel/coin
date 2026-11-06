package economics

import (
	"encoding/json"
	"os"
	"testing"
)

type feeVectors struct {
	FeeMarket struct {
		Base         uint64       `json:"base"`
		Min          uint64       `json:"min"`
		Max          uint64       `json:"max"`
		Target       uint64       `json:"target"`
		MaxChangeBPS uint32       `json:"max_change_bps"`
		Cases        []marketCase `json:"cases"`
	} `json:"fee_market"`
	Fee         []feeCase         `json:"fee"`
	Split       []splitCase       `json:"split"`
	Sponsorship []sponsorshipCase `json:"sponsorship"`
}

type marketCase struct {
	Name string `json:"name"`
	Base uint64 `json:"base"`
	Used uint64 `json:"used"`
	Next uint64 `json:"next"`
}

type feeCase struct {
	Name        string `json:"name"`
	Base        uint64 `json:"base"`
	Priority    uint64 `json:"priority"`
	MaxPriority uint64 `json:"max_priority"`
	Min         uint64 `json:"min"`
	Fee         uint64 `json:"fee"`
	Error       string `json:"error"`
}

type splitCase struct {
	Name         string `json:"name"`
	Fee          uint64 `json:"fee"`
	BurnBPS      uint32 `json:"burn_bps"`
	ValidatorBPS uint32 `json:"validator_bps"`
	Burn         uint64 `json:"burn"`
	Validators   uint64 `json:"validators"`
	Treasury     uint64 `json:"treasury"`
	Error        string `json:"error"`
}

type sponsorshipCase struct {
	Name      string        `json:"name"`
	Sponsor   string        `json:"sponsor"`
	Sponsored string        `json:"sponsored"`
	Asset     string        `json:"asset"`
	Budget    uint64        `json:"budget"`
	ExpiresAt uint64        `json:"expires_at"`
	Steps     []sponsorStep `json:"steps"`
	Expect    sponsorExpect `json:"expect"`
	Error     string        `json:"error"`
}

type sponsorStep struct {
	Op     string `json:"op"`
	Amount uint64 `json:"amount"`
	At     uint64 `json:"at"`
	Expect uint64 `json:"expect"`
	Error  string `json:"error"`
}

type sponsorExpect struct {
	Spent uint64 `json:"spent"`
}

func TestFeeVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/fee.json")
	if err != nil {
		t.Fatal(err)
	}
	var file feeVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, test := range file.FeeMarket.Cases {
		base := file.FeeMarket.Base
		if test.Base != 0 {
			base = test.Base
		}
		market, err := NewFeeMarket(base, file.FeeMarket.Min, file.FeeMarket.Max, file.FeeMarket.Target, file.FeeMarket.MaxChangeBPS)
		if err != nil {
			t.Fatal(err)
		}
		if got := market.Next(test.Used); got != test.Next {
			t.Fatalf("%s: want %d, got %d", test.Name, test.Next, got)
		}
	}
	for _, test := range file.Fee {
		fee, err := EffectiveFee(test.Base, test.Priority, test.MaxPriority, test.Min)
		if test.Error != "" {
			if codeOf(err) != test.Error {
				t.Fatalf("%s: want %s, got %v", test.Name, test.Error, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if fee != test.Fee {
			t.Fatalf("%s: want %d, got %d", test.Name, test.Fee, fee)
		}
	}
	for _, test := range file.Split {
		split, err := SplitFee(test.Fee, test.BurnBPS, test.ValidatorBPS)
		if test.Error != "" {
			if codeOf(err) != test.Error {
				t.Fatalf("%s: want %s, got %v", test.Name, test.Error, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if split.Burn != test.Burn || split.Validators != test.Validators || split.Treasury != test.Treasury {
			t.Fatalf("%s: want %d/%d/%d, got %+v", test.Name, test.Burn, test.Validators, test.Treasury, split)
		}
	}
	for _, test := range file.Sponsorship {
		sponsorship, err := NewSponsorship(test.Sponsor, test.Sponsored, test.Asset, test.Budget, test.ExpiresAt)
		if test.Error != "" {
			if codeOf(err) != test.Error {
				t.Fatalf("%s: want %s, got %v", test.Name, test.Error, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range test.Steps {
			var stepErr error
			switch step.Op {
			case "pay":
				stepErr = sponsorship.Pay(step.Amount, step.At)
			case "remaining":
				if got := sponsorship.Remaining(step.At); got != step.Expect {
					t.Fatalf("%s remaining at %d: want %d, got %d", test.Name, step.At, step.Expect, got)
				}
			default:
				t.Fatalf("unknown op %q", step.Op)
			}
			if step.Error != "" && codeOf(stepErr) != step.Error {
				t.Fatalf("%s %s: want %s, got %v", test.Name, step.Op, step.Error, stepErr)
			}
			if step.Error == "" && stepErr != nil {
				t.Fatalf("%s %s: %v", test.Name, step.Op, stepErr)
			}
		}
		if sponsorship.Spent != test.Expect.Spent {
			t.Fatalf("%s spent: want %d, got %d", test.Name, test.Expect.Spent, sponsorship.Spent)
		}
	}
}
