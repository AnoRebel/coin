package staking

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type blsVector struct {
	Validators []struct {
		ID        string `json:"id"`
		PublicKey string `json:"public_key"`
		PoP       string `json:"pop"`
	} `json:"validators"`
}

type vectorFile struct {
	Network string       `json:"network"`
	Cases   []vectorCase `json:"cases"`
}

type vectorCase struct {
	Name   string       `json:"name"`
	Params Params       `json:"params"`
	Steps  []vectorStep `json:"steps"`
}

type vectorStep struct {
	Op            string       `json:"op"`
	Validator     string       `json:"validator"`
	Delegator     string       `json:"delegator"`
	KeyFrom       string       `json:"key_from"`
	Amount        uint64       `json:"amount"`
	SelfBond      uint64       `json:"self_bond"`
	CommissionBps uint32       `json:"commission_bps"`
	At            uint64       `json:"at"`
	Until         uint64       `json:"until"`
	Expect        vectorExpect `json:"expect"`
}

type vectorExpect struct {
	Error      string                `json:"error"`
	Epoch      *uint64               `json:"epoch"`
	Height     *uint64               `json:"height"`
	Active     []string              `json:"active"`
	Validators map[string]viewExpect `json:"validators"`
	Unbonding  *[]UnbondingEntry     `json:"unbonding"`
}

type viewExpect struct {
	Status        *Status `json:"status"`
	SelfBond      *uint64 `json:"self_bond"`
	Delegated     *uint64 `json:"delegated"`
	Stake         *uint64 `json:"stake"`
	VotingPower   *uint64 `json:"voting_power"`
	CommissionBps *uint32 `json:"commission_bps"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	var keys blsVector
	loadJSON(t, "../../spec/vectors/bls.json", &keys)
	var file vectorFile
	loadJSON(t, "../../spec/vectors/staking.json", &file)
	keyByID = map[string]struct{ key, pop string }{}
	for _, v := range keys.Validators {
		keyByID[v.ID] = struct{ key, pop string }{v.PublicKey, v.PoP}
	}
	return file
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

var keyByID map[string]struct{ key, pop string }

func codeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDuplicate):
		return "duplicate"
	case errors.Is(err, ErrUnknown):
		return "unknown"
	case errors.Is(err, ErrInsufficient):
		return "insufficient"
	case errors.Is(err, ErrSelfBond):
		return "self-bond"
	case errors.Is(err, ErrCommission):
		return "commission"
	case errors.Is(err, ErrPoP):
		return "pop"
	case errors.Is(err, ErrEpoch):
		return "epoch"
	case errors.Is(err, ErrStatus):
		return "status"
	default:
		return "invalid"
	}
}

func TestVectors(t *testing.T) {
	file := loadVectors(t)
	if len(file.Cases) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			state, err := New(file.Network, c.Params)
			if err != nil {
				t.Fatal(err)
			}
			for i, step := range c.Steps {
				err := apply(state, step)
				if code := codeOf(err); code != step.Expect.Error {
					t.Fatalf("step %d %s: error %q (%v), want %q", i, step.Op, code, err, step.Expect.Error)
				}
				if err != nil {
					assertUnchanged(t, state, step.Expect)
					continue
				}
				assertState(t, state, step.Expect)
			}
		})
	}
}

func apply(s *State, step vectorStep) error {
	switch step.Op {
	case "register":
		keys := keyByID[step.Validator]
		if step.KeyFrom != "" {
			keys = keyByID[step.KeyFrom]
		}
		return s.Register(Validator{
			Address: step.Validator, ConsensusKey: keys.key, PoP: keys.pop,
			CommissionBps: step.CommissionBps, SelfBond: step.SelfBond,
		}, step.At)
	case "delegate":
		return s.Delegate(step.Validator, step.Delegator, step.Amount, step.At)
	case "undelegate":
		return s.Undelegate(step.Validator, step.Delegator, step.Amount, step.At)
	case "begin_unbonding":
		return s.BeginUnbonding(step.Validator, step.At)
	case "jail":
		return s.Jail(step.Validator, step.At, step.Until)
	case "unjail":
		return s.Unjail(step.Validator, step.At)
	case "advance_epoch":
		return s.AdvanceEpoch(step.At)
	default:
		return errors.New("unknown op " + step.Op)
	}
}

func assertUnchanged(t *testing.T, s *State, want vectorExpect) {
	t.Helper()
	if want.Height != nil && s.Height != *want.Height {
		t.Fatalf("height = %d, want %d", s.Height, *want.Height)
	}
}

func assertState(t *testing.T, s *State, want vectorExpect) {
	t.Helper()
	snap := s.Snapshot()
	if want.Epoch != nil && snap.Epoch != *want.Epoch {
		t.Fatalf("epoch = %d, want %d", snap.Epoch, *want.Epoch)
	}
	if want.Height != nil && snap.Height != *want.Height {
		t.Fatalf("height = %d, want %d", snap.Height, *want.Height)
	}
	if want.Active != nil {
		if len(snap.Active) != len(want.Active) {
			t.Fatalf("active = %v, want %v", snap.Active, want.Active)
		}
		for i := range want.Active {
			if snap.Active[i] != want.Active[i] {
				t.Fatalf("active = %v, want %v", snap.Active, want.Active)
			}
		}
	}
	byAddress := map[string]ValidatorView{}
	for _, v := range snap.Validators {
		byAddress[v.Address] = v
	}
	for address, expected := range want.Validators {
		got, ok := byAddress[address]
		if !ok {
			t.Fatalf("validator %s missing", address)
		}
		if expected.Status != nil && got.Status != *expected.Status {
			t.Fatalf("%s status = %s, want %s", address, got.Status, *expected.Status)
		}
		if expected.SelfBond != nil && got.SelfBond != *expected.SelfBond {
			t.Fatalf("%s self bond = %d, want %d", address, got.SelfBond, *expected.SelfBond)
		}
		if expected.Delegated != nil && got.Delegated != *expected.Delegated {
			t.Fatalf("%s delegated = %d, want %d", address, got.Delegated, *expected.Delegated)
		}
		if expected.Stake != nil && got.Stake != *expected.Stake {
			t.Fatalf("%s stake = %d, want %d", address, got.Stake, *expected.Stake)
		}
		if expected.VotingPower != nil && got.VotingPower != *expected.VotingPower {
			t.Fatalf("%s power = %d, want %d", address, got.VotingPower, *expected.VotingPower)
		}
		if expected.CommissionBps != nil && got.CommissionBps != *expected.CommissionBps {
			t.Fatalf("%s commission = %d, want %d", address, got.CommissionBps, *expected.CommissionBps)
		}
	}
	if want.Unbonding != nil {
		if len(snap.Unbonding) != len(*want.Unbonding) {
			t.Fatalf("unbonding = %v, want %v", snap.Unbonding, *want.Unbonding)
		}
		for i := range *want.Unbonding {
			if snap.Unbonding[i] != (*want.Unbonding)[i] {
				t.Fatalf("unbonding = %v, want %v", snap.Unbonding, *want.Unbonding)
			}
		}
	}
}
