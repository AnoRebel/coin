package governance

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type governanceVectors struct {
	Network string       `json:"network"`
	Params  Params       `json:"params"`
	Cases   []vectorCase `json:"cases"`
}

type vectorCase struct {
	Name        string       `json:"name"`
	SubmitError string       `json:"submit_error"`
	Submit      vectorSubmit `json:"submit"`
	Steps       []vectorStep `json:"steps"`
	Expect      vectorExpect `json:"expect"`
}

type vectorSubmit struct {
	Kind             Kind     `json:"kind"`
	Proposer         string   `json:"proposer"`
	Nonce            uint64   `json:"nonce"`
	Title            string   `json:"title"`
	Actions          []Action `json:"actions"`
	Deposit          uint64   `json:"deposit"`
	ActivationHeight uint64   `json:"activation_height"`
	TotalPower       uint64   `json:"total_power"`
	At               uint64   `json:"at"`
}

type vectorStep struct {
	Op     string `json:"op"`
	Voter  string `json:"voter"`
	Option Option `json:"option"`
	Weight uint64 `json:"weight"`
	At     uint64 `json:"at"`
	Actor  string `json:"actor"`
	Error  string `json:"error"`
}

type vectorExpect struct {
	Status           Status         `json:"status"`
	Yes              uint64         `json:"yes"`
	No               uint64         `json:"no"`
	Abstain          uint64         `json:"abstain"`
	TimelockEnd      uint64         `json:"timelock_end"`
	ActivationHeight uint64         `json:"activation_height"`
	VotingStart      uint64         `json:"voting_start"`
	VotingEnd        uint64         `json:"voting_end"`
	Refunded         bool           `json:"refunded"`
	ExecutionError   string         `json:"execution_error"`
	Applied          map[string]any `json:"applied"`
}

func TestSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/governance.json")
	if err != nil {
		t.Fatal(err)
	}
	var file governanceVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, test := range file.Cases {
		t.Run(test.Name, func(t *testing.T) {
			state, err := New(file.Network, file.Params)
			if err != nil {
				t.Fatal(err)
			}
			proposal := Proposal{
				Kind: test.Submit.Kind, Proposer: test.Submit.Proposer,
				Nonce: test.Submit.Nonce, Title: test.Submit.Title,
				Actions: test.Submit.Actions, Deposit: test.Submit.Deposit,
				ActivationHeight: test.Submit.ActivationHeight,
			}
			err = state.Submit(proposal, test.Submit.TotalPower, test.Submit.At)
			if test.SubmitError != "" {
				if codeOf(err) != test.SubmitError {
					t.Fatalf("submit: want %s, got %v", test.SubmitError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			id := ProposalID(file.Network, test.Submit.Proposer, test.Submit.Nonce)
			applied := map[string]any{}
			for _, step := range test.Steps {
				var stepErr error
				switch step.Op {
				case "advance":
					state.Advance(step.At)
				case "vote":
					stepErr = state.Vote(id, step.Voter, step.Option, step.Weight, step.At)
				case "queue":
					stepErr = state.Queue(id, step.At)
				case "execute":
					stepErr = state.Execute(id, step.At, vectorRegistry(applied))
				case "cancel":
					stepErr = state.Cancel(id, step.Actor, step.At)
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
			got := state.Proposals[id]
			if test.Expect.Status != "" && got.Status != test.Expect.Status {
				t.Fatalf("status: want %s, got %s", test.Expect.Status, got.Status)
			}
			for name, pair := range map[string][2]uint64{
				"yes": {test.Expect.Yes, got.Yes}, "no": {test.Expect.No, got.No},
				"abstain":           {test.Expect.Abstain, got.Abstain},
				"timelock_end":      {test.Expect.TimelockEnd, got.TimelockEnd},
				"activation_height": {test.Expect.ActivationHeight, got.ActivationHeight},
				"voting_start":      {test.Expect.VotingStart, got.VotingStart},
				"voting_end":        {test.Expect.VotingEnd, got.VotingEnd},
			} {
				if pair[0] != 0 && pair[0] != pair[1] {
					t.Fatalf("%s: want %d, got %d", name, pair[0], pair[1])
				}
			}
			if test.Expect.Refunded != got.Refunded {
				t.Fatalf("refunded: want %v", test.Expect.Refunded)
			}
			if test.Expect.ExecutionError != "" && !strings.Contains(got.ExecutionError, test.Expect.ExecutionError) {
				t.Fatalf("execution_error: %q", got.ExecutionError)
			}
			if test.Expect.Applied != nil && !reflect.DeepEqual(test.Expect.Applied, applied) {
				t.Fatalf("applied: want %v, got %v", test.Expect.Applied, applied)
			}
		})
	}
}

func vectorRegistry(applied map[string]any) *Registry {
	r := NewRegistry()
	_ = r.Register("set_parameter", func(a Action, p *Proposal) (func(), error) {
		name, _ := a.Params["name"].(string)
		applied[name] = a.Params["value"]
		return func() { delete(applied, name) }, nil
	})
	_ = r.Register("fail", func(a Action, p *Proposal) (func(), error) {
		applied["fail"] = true
		return func() { delete(applied, "fail") }, errors.New("boom")
	})
	return r
}

func codeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, ErrDuplicate):
		return "duplicate"
	case errors.Is(err, ErrUnknown):
		return "unknown"
	case errors.Is(err, ErrStatus):
		return "status"
	case errors.Is(err, ErrVoted):
		return "voted"
	case errors.Is(err, ErrPower):
		return "power"
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, ErrAction):
		return "action"
	case strings.Contains(err.Error(), "boom"):
		return "boom"
	default:
		return "other"
	}
}
