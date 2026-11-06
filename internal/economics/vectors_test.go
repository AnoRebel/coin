package economics

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

type economicsVectors struct {
	Genesis []genesisCase `json:"genesis"`
	Supply  []supplyCase  `json:"supply"`
	Vesting []vestingCase `json:"vesting"`
}

type genesisCase struct {
	Name      string     `json:"name"`
	Cap       uint64     `json:"cap"`
	Schedules []Schedule `json:"schedules"`
	Total     uint64     `json:"total"`
	Error     string     `json:"error"`
}

type supplyCase struct {
	Name   string       `json:"name"`
	Cap    uint64       `json:"cap"`
	Steps  []supplyStep `json:"steps"`
	Expect supplyExpect `json:"expect"`
}

type supplyStep struct {
	Op      string `json:"op"`
	Amount  uint64 `json:"amount"`
	Epoch   uint64 `json:"epoch"`
	At      uint64 `json:"at"`
	BPS     uint32 `json:"bps"`
	Purpose string `json:"purpose"`
	Error   string `json:"error"`
}

type supplyExpect struct {
	Total   uint64 `json:"total"`
	Minted  uint64 `json:"minted"`
	Burned  uint64 `json:"burned"`
	Tail    uint64 `json:"tail"`
	Entries int    `json:"entries"`
}

type vestingCase struct {
	Name     string        `json:"name"`
	Schedule Schedule      `json:"schedule"`
	Steps    []vestingStep `json:"steps"`
	Expect   vestingExpect `json:"expect"`
}

type vestingStep struct {
	Op        string         `json:"op"`
	Amount    uint64         `json:"amount"`
	At        uint64         `json:"at"`
	Expect    uint64         `json:"expect"`
	Completed *bool          `json:"completed"`
	Events    []VestingEvent `json:"events"`
	Error     string         `json:"error"`
}

type vestingExpect struct {
	Transferred uint64 `json:"transferred"`
	Completed   bool   `json:"completed"`
}

func TestEconomicsVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/economics.json")
	if err != nil {
		t.Fatal(err)
	}
	var file economicsVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, test := range file.Genesis {
		t.Run(test.Name, func(t *testing.T) {
			total, err := ValidateGenesis(test.Cap, test.Schedules)
			if test.Error != "" {
				if codeOf(err) != test.Error {
					t.Fatalf("want %s, got %v", test.Error, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if total != test.Total {
				t.Fatalf("total: want %d, got %d", test.Total, total)
			}
		})
	}
	for _, test := range file.Supply {
		t.Run(test.Name, func(t *testing.T) {
			supply, err := NewSupply(test.Cap)
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range test.Steps {
				var stepErr error
				switch step.Op {
				case "mint":
					stepErr = supply.Mint(step.Amount, step.Purpose, step.At)
				case "burn":
					stepErr = supply.Burn(step.Amount, step.At)
				case "set_tail_rate":
					stepErr = supply.SetTailRate(step.BPS)
				case "tail":
					stepErr = supply.MintTail(step.Amount, step.Epoch, step.At)
				case "tail_for":
					stepErr = supply.MintTailFor(step.Amount, step.Epoch, step.At, step.Purpose)
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
			got := supplyExpect{
				Total: supply.Total(), Minted: supply.Minted,
				Burned: supply.Burned, Tail: supply.Tail, Entries: len(supply.Entries),
			}
			if test.Expect.Entries != 0 && got.Entries != test.Expect.Entries {
				t.Fatalf("entries: want %d, got %d", test.Expect.Entries, got.Entries)
			}
			test.Expect.Entries = got.Entries
			if !reflect.DeepEqual(got, test.Expect) {
				t.Fatalf("supply: want %+v, got %+v", test.Expect, got)
			}
		})
	}
	for _, test := range file.Vesting {
		t.Run(test.Name, func(t *testing.T) {
			vesting, err := NewVesting(test.Schedule)
			if err != nil {
				t.Fatal(err)
			}
			for _, step := range test.Steps {
				var stepErr error
				switch step.Op {
				case "unlocked":
					if got := vesting.Unlocked(step.At); got != step.Expect {
						t.Fatalf("unlocked at %d: want %d, got %d", step.At, step.Expect, got)
					}
				case "transferable":
					if got := vesting.Transferable(step.At); got != step.Expect {
						t.Fatalf("transferable at %d: want %d, got %d", step.At, step.Expect, got)
					}
				case "transfer":
					stepErr = vesting.Transfer(step.Amount, step.At)
				case "advance":
					if got := vesting.Advance(step.At); step.Completed != nil && got != *step.Completed {
						t.Fatalf("advance at %d: want %v, got %v", step.At, *step.Completed, got)
					}
					if step.Events != nil && !reflect.DeepEqual(vesting.Events, step.Events) {
						t.Fatalf("events: want %v, got %v", step.Events, vesting.Events)
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
			if vesting.Transferred != test.Expect.Transferred {
				t.Fatalf("transferred: want %d, got %d", test.Expect.Transferred, vesting.Transferred)
			}
			if vesting.Completed != test.Expect.Completed {
				t.Fatalf("completed: want %v", test.Expect.Completed)
			}
		})
	}
}

func codeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, ErrCap):
		return "cap"
	case errors.Is(err, ErrLocked):
		return "locked"
	case errors.Is(err, ErrPurpose):
		return "purpose"
	case errors.Is(err, ErrRate):
		return "rate"
	case errors.Is(err, ErrFee):
		return "fee"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrBudget):
		return "budget"
	case errors.Is(err, ErrProof):
		return "proof"
	case errors.Is(err, ErrUnbacked):
		return "unbacked"
	case errors.Is(err, ErrSlippage):
		return "slippage"
	default:
		return "other"
	}
}
