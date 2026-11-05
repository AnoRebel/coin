package governance

import (
	"errors"
	"testing"
)

func TestStakeCapAndDelegation(t *testing.T) {
	power := NewVotingPower()
	_ = power.SetStake("a", 600)
	_ = power.SetStake("b", 500)
	power.MaxVoterBps = 5000
	if total := power.Total(); total != 1100 {
		t.Fatalf("total %d", total)
	}
	weight, role, delegators := power.Ballot("a")
	if weight != 550 || role != "" || len(delegators) != 0 {
		t.Fatalf("capped ballot: %d %q %v", weight, role, delegators)
	}

	power = NewVotingPower()
	_ = power.SetStake("a", 300)
	_ = power.SetStake("b", 200)
	if err := power.SetDelegation("a", "b"); err != nil {
		t.Fatal(err)
	}
	weight, _, delegators = power.Ballot("b")
	if weight != 500 || delegators["a"] != 300 || len(delegators) != 1 {
		t.Fatalf("delegated ballot: %d %v", weight, delegators)
	}
	if weight, _, _ := power.Ballot("a"); weight != 0 {
		t.Fatalf("delegator still votes with %d", weight)
	}
}

func TestDelegatedDoubleVoteRejected(t *testing.T) {
	power := NewVotingPower()
	_ = power.SetStake("a", 300)
	_ = power.SetStake("b", 100)
	_ = power.SetStake("c", 100)
	if err := power.SetDelegation("a", "b"); err != nil {
		t.Fatal(err)
	}
	s := newTestState(t)
	if err := s.Submit(proposal(Parameter, 1), power.Total(), 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", "arce-devnet1proposer", 1)
	if err := s.CastVote(id, "b", Yes, power, 110); err != nil {
		t.Fatal(err)
	}
	if err := power.SetDelegation("a", "c"); err != nil {
		t.Fatal(err)
	}
	if err := s.CastVote(id, "c", Yes, power, 112); !errors.Is(err, ErrVoted) {
		t.Fatalf("second delegate: %v", err)
	}
}

func TestRoleWeightedVote(t *testing.T) {
	power := NewVotingPower()
	_ = power.SetRole("a", "operator", 3)
	_ = power.SetRole("b", "observer", 1)
	if total := power.Total(); total != 4 {
		t.Fatalf("total %d", total)
	}
	weight, role, _ := power.Ballot("a")
	if weight != 3 || role != "operator" {
		t.Fatalf("ballot: %d %q", weight, role)
	}
	s := newTestState(t)
	if err := s.Submit(proposal(Parameter, 1), power.Total(), 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", "arce-devnet1proposer", 1)
	if err := s.CastVote(id, "a", Yes, power, 110); err != nil {
		t.Fatal(err)
	}
	if err := s.CastVote(id, "b", No, power, 111); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Get(id)
	if p.Votes["a"].Role != "operator" || p.Yes != 3 || p.No != 1 {
		t.Fatalf("role vote: %+v", p)
	}
	s.Advance(200)
	if p.Status != Passed {
		t.Fatalf("status %s", p.Status)
	}
}

func TestDelegationCycleAndUnknownRole(t *testing.T) {
	power := NewVotingPower()
	_ = power.SetStake("a", 100)
	_ = power.SetStake("b", 100)
	if err := power.SetDelegation("a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := power.SetDelegation("b", "a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle: %v", err)
	}
	if err := power.SetDelegation("a", "a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self: %v", err)
	}
	power.RoleOf["a"] = "guest"
	if weight, role, _ := power.Ballot("a"); weight != 0 || role != "guest" {
		t.Fatalf("unknown role: %d %q", weight, role)
	}
}
