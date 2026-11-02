package admission

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/staking"
)

type vectorFile struct {
	Cases []vectorCase `json:"cases"`
}

type vectorCase struct {
	Name   string       `json:"name"`
	Policy Policy       `json:"policy"`
	Steps  []vectorStep `json:"steps"`
}

type vectorStep struct {
	Op        string       `json:"op"`
	Validator string       `json:"validator"`
	Approver  string       `json:"approver"`
	At        uint64       `json:"at"`
	Expect    vectorExpect `json:"expect"`
}

type vectorExpect struct {
	Error     string   `json:"error"`
	Allowed   *bool    `json:"allowed"`
	Status    string   `json:"status"`
	Approvers []string `json:"approvers"`
	Height    *uint64  `json:"height"`
}

func TestVectors(t *testing.T) {
	var file vectorFile
	loadJSON(t, "../../spec/vectors/admission.json", &file)
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			state, err := New(c.Policy)
			if err != nil {
				t.Fatal(err)
			}
			for i, step := range c.Steps {
				err = nil
				switch step.Op {
				case "allowed":
					if got := state.Allowed(step.Validator); step.Expect.Allowed != nil && got != *step.Expect.Allowed {
						t.Fatalf("step %d allowed = %v, want %v", i, got, *step.Expect.Allowed)
					}
				case "approve":
					err = state.Approve(step.Validator, step.Approver, step.At)
				}
				if code := errorCode(err); code != step.Expect.Error {
					t.Fatalf("step %d error %q (%v), want %q", i, code, err, step.Expect.Error)
				}
				if step.Expect.Status != "" && state.Status(step.Validator) != step.Expect.Status {
					t.Fatalf("step %d status = %s, want %s", i, state.Status(step.Validator), step.Expect.Status)
				}
				if step.Expect.Approvers != nil {
					record := state.Approvals[step.Validator]
					if record == nil || len(record.Approvers) != len(step.Expect.Approvers) {
						t.Fatalf("step %d approvers = %v, want %v", i, record, step.Expect.Approvers)
					}
					for j := range step.Expect.Approvers {
						if record.Approvers[j] != step.Expect.Approvers[j] {
							t.Fatalf("step %d approvers = %v, want %v", i, record.Approvers, step.Expect.Approvers)
						}
					}
					if step.Expect.Height != nil && record.Height != *step.Expect.Height {
						t.Fatalf("step %d height = %d, want %d", i, record.Height, *step.Expect.Height)
					}
				}
				if step.Expect.Allowed != nil && step.Op == "approve" {
					if got := state.Allowed(step.Validator); got != *step.Expect.Allowed {
						t.Fatalf("step %d allowed = %v, want %v", i, got, *step.Expect.Allowed)
					}
				}
			}
		})
	}
}

func TestInvalidPolicies(t *testing.T) {
	cases := []Policy{
		{Mode: "open"},
		{Mode: Consortium, Threshold: 1},
		{Mode: Consortium, Approvers: []string{"a1"}, Threshold: 0},
		{Mode: Consortium, Approvers: []string{"a1", "a1"}, Threshold: 1},
		{Mode: Public, Approvers: []string{"a1"}},
	}
	for _, policy := range cases {
		if _, err := New(policy); !errors.Is(err, ErrInvalid) {
			t.Fatalf("policy %+v = %v", policy, err)
		}
	}
}

func TestConsortiumAdmissionGatesEpoch(t *testing.T) {
	network := "arce:devnet"
	keys := loadKeys(t)
	params := staking.Params{
		MinSelfBond: 500, MinStake: 1000, EpochBlocks: 10, UnbondingBlocks: 20,
	}
	state, err := staking.New(network, params)
	if err != nil {
		t.Fatal(err)
	}
	register(t, state, keys["v1"], "v1", 1)
	register(t, state, keys["v2"], "v2", 2)

	adm, err := New(Policy{Mode: Consortium, Approvers: []string{"a1", "a2"}, Threshold: 2})
	if err != nil {
		t.Fatal(err)
	}
	state.SetAdmission(adm.Approved())
	if err := state.AdvanceEpoch(10); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"v1", "v2"} {
		if state.Validators[address].Status != staking.Pending {
			t.Fatalf("%s status = %s, want pending", address, state.Validators[address].Status)
		}
	}

	if err := adm.Approve("v1", "a1", 11); err != nil {
		t.Fatal(err)
	}
	if err := adm.Approve("v1", "a2", 12); err != nil {
		t.Fatal(err)
	}
	state.SetAdmission(adm.Approved())
	if err := state.AdvanceEpoch(20); err != nil {
		t.Fatal(err)
	}
	if state.Validators["v1"].Status != staking.Active {
		t.Fatalf("v1 status = %s, want active", state.Validators["v1"].Status)
	}
	if state.Validators["v2"].Status != staking.Pending {
		t.Fatalf("v2 status = %s, want pending", state.Validators["v2"].Status)
	}
}

func TestPublicAdmissionActivatesWithoutApproval(t *testing.T) {
	network := "arce:devnet"
	keys := loadKeys(t)
	params := staking.Params{
		MinSelfBond: 500, MinStake: 1000, EpochBlocks: 10, UnbondingBlocks: 20,
	}
	state, err := staking.New(network, params)
	if err != nil {
		t.Fatal(err)
	}
	register(t, state, keys["v1"], "v1", 1)
	adm, err := New(Policy{Mode: Public})
	if err != nil {
		t.Fatal(err)
	}
	if !adm.Allowed("v1") {
		t.Fatal("public validator not allowed")
	}
	if err := state.AdvanceEpoch(10); err != nil {
		t.Fatal(err)
	}
	if state.Validators["v1"].Status != staking.Active {
		t.Fatalf("status = %s, want active", state.Validators["v1"].Status)
	}
}

func register(t *testing.T, state *staking.State, keys keyPair, address string, at uint64) {
	t.Helper()
	if err := state.Register(staking.Validator{
		Address: address, ConsensusKey: keys.key, PoP: keys.pop,
		CommissionBps: 100, SelfBond: 1000,
	}, at); err != nil {
		t.Fatal(err)
	}
}

type keyPair struct{ key, pop string }

func loadKeys(t *testing.T) map[string]keyPair {
	t.Helper()
	var doc struct {
		Validators []struct {
			ID        string `json:"id"`
			PublicKey string `json:"public_key"`
			PoP       string `json:"pop"`
		} `json:"validators"`
	}
	loadJSON(t, "../../spec/vectors/bls.json", &doc)
	out := map[string]keyPair{}
	for _, v := range doc.Validators {
		out[v.ID] = keyPair{v.PublicKey, v.PoP}
	}
	return out
}

func errorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrMode):
		return "wrong mode"
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized approver"
	case errors.Is(err, ErrDuplicate):
		return "duplicate approval"
	case errors.Is(err, ErrAlreadyApproved):
		return "already approved"
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
