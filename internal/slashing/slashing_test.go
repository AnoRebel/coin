package slashing

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/staking"
)

type blsVector struct {
	Validators []struct {
		ID        string `json:"id"`
		PublicKey string `json:"public_key"`
		PoP       string `json:"pop"`
	} `json:"validators"`
}

type vectorFile struct {
	Network        string         `json:"network"`
	StakingParams  staking.Params `json:"staking_params"`
	SlashingParams Params         `json:"slashing_params"`
	Cases          []vectorCase   `json:"cases"`
}

type vectorCase struct {
	Name  string        `json:"name"`
	Setup []stakingStep `json:"setup"`
	Steps []slashStep   `json:"steps"`
}

type stakingStep struct {
	Op            string `json:"op"`
	Validator     string `json:"validator"`
	SelfBond      uint64 `json:"self_bond"`
	CommissionBps uint32 `json:"commission_bps"`
	At            uint64 `json:"at"`
}

type slashStep struct {
	Op        string      `json:"op"`
	Validator string      `json:"validator"`
	A         Vote        `json:"a"`
	B         Vote        `json:"b"`
	From      uint64      `json:"from"`
	To        uint64      `json:"to"`
	Missed    uint64      `json:"missed"`
	Total     uint64      `json:"total"`
	At        uint64      `json:"at"`
	Expect    slashExpect `json:"expect"`
}

type slashExpect struct {
	Error     string         `json:"error"`
	Result    *Result        `json:"result"`
	Validator *validatorWant `json:"validator"`
}

type validatorWant struct {
	Status      *staking.Status `json:"status"`
	SelfBond    *uint64         `json:"self_bond"`
	Stake       *uint64         `json:"stake"`
	VotingPower *uint64         `json:"voting_power"`
}

var keyByID map[string]struct{ key, pop string }

func TestVectors(t *testing.T) {
	var keys blsVector
	loadJSON(t, "../../spec/vectors/bls.json", &keys)
	var file vectorFile
	loadJSON(t, "../../spec/vectors/slashing.json", &file)
	keyByID = map[string]struct{ key, pop string }{}
	for _, v := range keys.Validators {
		keyByID[v.ID] = struct{ key, pop string }{v.PublicKey, v.PoP}
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			state, err := staking.New(file.Network, file.StakingParams)
			if err != nil {
				t.Fatal(err)
			}
			engine, err := New(file.Network, file.SlashingParams)
			if err != nil {
				t.Fatal(err)
			}
			for _, setup := range c.Setup {
				switch setup.Op {
				case "register":
					keys := keyByID[setup.Validator]
					if err := state.Register(staking.Validator{
						Address: setup.Validator, ConsensusKey: keys.key, PoP: keys.pop,
						CommissionBps: setup.CommissionBps, SelfBond: setup.SelfBond,
					}, setup.At); err != nil {
						t.Fatal(err)
					}
				case "advance_epoch":
					if err := state.AdvanceEpoch(setup.At); err != nil {
						t.Fatal(err)
					}
				}
			}
			for i, step := range c.Steps {
				var result Result
				switch step.Op {
				case "double_sign":
					result, err = engine.DoubleSign(state, DoubleSignEvidence{
						Validator: step.Validator, A: step.A, B: step.B,
					}, step.At)
				case "downtime":
					result, err = engine.Downtime(state, DowntimeEvidence{
						Validator: step.Validator, From: step.From, To: step.To,
						Missed: step.Missed, Total: step.Total,
					}, step.At)
				}
				if code := slashCode(err); code != step.Expect.Error {
					t.Fatalf("step %d %s: error %q (%v), want %q", i, step.Op, code, err, step.Expect.Error)
				}
				if err != nil {
					continue
				}
				if step.Expect.Result != nil {
					want := *step.Expect.Result
					if result.Kind != want.Kind || result.Slashed != want.Slashed ||
						result.Reporter != want.Reporter || result.Treasury != want.Treasury ||
						result.JailUntil != want.JailUntil {
						t.Fatalf("result = %+v, want %+v", result, want)
					}
					if len(result.Delegators) != len(want.Delegators) {
						t.Fatalf("delegators = %v, want %v", result.Delegators, want.Delegators)
					}
					for address, amount := range want.Delegators {
						if result.Delegators[address] != amount {
							t.Fatalf("delegators = %v, want %v", result.Delegators, want.Delegators)
						}
					}
				}
				if step.Expect.Validator != nil {
					assertValidator(t, state, step.Validator, *step.Expect.Validator)
				}
			}
		})
	}
}

func assertValidator(t *testing.T, state *staking.State, address string, want validatorWant) {
	t.Helper()
	var view staking.ValidatorView
	for _, v := range state.Snapshot().Validators {
		if v.Address == address {
			view = v
		}
	}
	if want.Status != nil && view.Status != *want.Status {
		t.Fatalf("%s status = %s, want %s", address, view.Status, *want.Status)
	}
	if want.SelfBond != nil && view.SelfBond != *want.SelfBond {
		t.Fatalf("%s self bond = %d, want %d", address, view.SelfBond, *want.SelfBond)
	}
	if want.Stake != nil && view.Stake != *want.Stake {
		t.Fatalf("%s stake = %d, want %d", address, view.Stake, *want.Stake)
	}
	if want.VotingPower != nil && view.VotingPower != *want.VotingPower {
		t.Fatalf("%s power = %d, want %d", address, view.VotingPower, *want.VotingPower)
	}
}

func slashCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrStale):
		return "stale"
	case errors.Is(err, ErrForged):
		return "forged"
	case errors.Is(err, ErrDuplicate):
		return "duplicate"
	case errors.Is(err, ErrInsufficient):
		return "insufficient"
	case errors.Is(err, ErrNotEligible):
		return "not-eligible"
	default:
		return "invalid"
	}
}

func loadJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}
