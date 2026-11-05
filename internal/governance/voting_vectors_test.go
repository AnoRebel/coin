package governance

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type votingVectors struct {
	Network string       `json:"network"`
	Params  Params       `json:"params"`
	Cases   []votingCase `json:"cases"`
}

type votingCase struct {
	Name        string       `json:"name"`
	MaxVoterBps uint32       `json:"max_voter_bps"`
	Consortium  bool         `json:"consortium"`
	TotalPower  uint64       `json:"total_power"`
	Steps       []votingStep `json:"steps"`
	Expect      votingExpect `json:"expect"`
}

type votingStep struct {
	Op         string            `json:"op"`
	Member     string            `json:"member"`
	Amount     uint64            `json:"amount"`
	Delegator  string            `json:"delegator"`
	Delegate   string            `json:"delegate"`
	Role       string            `json:"role"`
	Weight     uint64            `json:"weight"`
	Voter      string            `json:"voter"`
	Option     Option            `json:"option"`
	At         uint64            `json:"at"`
	Error      string            `json:"error"`
	Delegators map[string]uint64 `json:"delegators"`
}

type votingExpect struct {
	Status  Status `json:"status"`
	Yes     uint64 `json:"yes"`
	No      uint64 `json:"no"`
	Abstain uint64 `json:"abstain"`
}

func TestSharedVotingVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/governance-voting.json")
	if err != nil {
		t.Fatal(err)
	}
	var file votingVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, test := range file.Cases {
		t.Run(test.Name, func(t *testing.T) {
			power := NewVotingPower()
			power.MaxVoterBps = test.MaxVoterBps
			power.Consortium = test.Consortium
			var state *State
			id := ""

			for _, step := range test.Steps {
				var err error
				switch step.Op {
				case "set_stake":
					err = power.SetStake(step.Member, step.Amount)
				case "set_delegation":
					err = power.SetDelegation(step.Delegator, step.Delegate)
				case "set_role":
					err = power.SetRole(step.Member, step.Role, step.Weight)
				case "ballot":
					var weight uint64
					var role string
					var delegators map[string]uint64
					weight, role, delegators = power.Ballot(step.Voter)
					if weight != step.Weight || role != step.Role {
						t.Fatalf("ballot %s: want %d %q, got %d %q", step.Voter, step.Weight, step.Role, weight, role)
					}
					if len(step.Delegators) != len(delegators) {
						t.Fatalf("ballot %s delegators: want %v, got %v", step.Voter, step.Delegators, delegators)
					}
					for member, amount := range step.Delegators {
						if delegators[member] != amount {
							t.Fatalf("ballot %s delegator %s: want %d, got %d", step.Voter, member, amount, delegators[member])
						}
					}
				case "vote":
					if state == nil {
						state, err = New(file.Network, file.Params)
						if err != nil {
							t.Fatal(err)
						}
						proposal := Proposal{
							Kind: Parameter, Proposer: "arce-devnet1proposer", Nonce: 1,
							Actions:          []Action{{Type: "set_parameter", Params: map[string]any{"name": "x", "value": 1}}},
							Deposit:          100,
							ActivationHeight: 500,
						}
						if err := state.Submit(proposal, test.TotalPower, 100); err != nil {
							t.Fatal(err)
						}
						id = ProposalID(file.Network, proposal.Proposer, proposal.Nonce)
					}
					err = state.CastVote(id, step.Voter, step.Option, power, step.At)
				case "advance":
					if state == nil {
						t.Fatalf("advance without a proposal")
					}
					state.Advance(step.At)
				default:
					t.Fatalf("unknown op %q", step.Op)
				}
				if step.Error != "" {
					if code := votingCode(err); code != step.Error {
						t.Fatalf("%s: want %s, got %v", step.Op, step.Error, err)
					}
				} else if err != nil {
					t.Fatalf("%s %+v: %v", step.Op, step, err)
				}
			}

			if state == nil {
				return
			}
			got := state.Proposals[id]
			if test.Expect.Status != "" && got.Status != test.Expect.Status {
				t.Fatalf("status: want %s, got %s", test.Expect.Status, got.Status)
			}
			if test.Expect.Yes != got.Yes || test.Expect.No != got.No || test.Expect.Abstain != got.Abstain {
				t.Fatalf("tally: want %d/%d/%d, got %d/%d/%d",
					test.Expect.Yes, test.Expect.No, test.Expect.Abstain, got.Yes, got.No, got.Abstain)
			}
		})
	}
}

func votingCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, ErrVoted):
		return "voted"
	case errors.Is(err, ErrPower):
		return "power"
	default:
		return "other"
	}
}
