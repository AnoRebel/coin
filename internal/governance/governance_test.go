package governance

import (
	"errors"
	"testing"
)

func testParams() Params {
	return Params{
		MinDeposit: 100, DepositPeriod: 0, VotingPeriod: 100, Timelock: 50,
		QuorumBps: 5000, ThresholdBps: 5000,
		ConstitutionalThresholdBps: 6667, ConstitutionalTimelock: 500,
		MaxActions: 8,
	}
}

func newTestState(t *testing.T) *State {
	t.Helper()
	s, err := New("arne:devnet", testParams())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func proposal(kind Kind, nonce uint64) Proposal {
	return Proposal{
		Kind: kind, Proposer: "arce-devnet1proposer", Nonce: nonce,
		Title: "test", Actions: []Action{{Type: "set", Params: map[string]any{"name": "x", "value": 1}}},
		Deposit: 100, ActivationHeight: 500,
	}
}

func registry(applied map[string]any, fail bool) *Registry {
	r := NewRegistry()
	_ = r.Register("set", func(a Action, p *Proposal) (func(), error) {
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

func TestLifecycleExecute(t *testing.T) {
	s := newTestState(t)
	p := proposal(Parameter, 1)
	if err := s.Submit(p, 1000, 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", p.Proposer, 1)
	if err := s.Vote(id, "voter-a", Yes, 600, 110); err != nil {
		t.Fatal(err)
	}
	if err := s.Vote(id, "voter-b", No, 100, 120); err != nil {
		t.Fatal(err)
	}
	s.Advance(200)
	got, _ := s.Get(id)
	if got.Status != Passed {
		t.Fatalf("status %s", got.Status)
	}
	if err := s.Queue(id, 200); err != nil {
		t.Fatal(err)
	}
	if err := s.Execute(id, 249, registry(map[string]any{}, false)); !errors.Is(err, ErrStatus) {
		t.Fatalf("early execute: %v", err)
	}
	applied := map[string]any{}
	if err := s.Execute(id, 250, registry(applied, false)); err != nil {
		t.Fatal(err)
	}
	if got.Status != Executed || got.ActivationHeight != 500 || applied["x"] != 1 {
		t.Fatalf("executed: %+v applied=%v", got, applied)
	}
}

func TestRejectedDoesNotExecute(t *testing.T) {
	for name, votes := range map[string]func(*State, string){
		"quorum": func(s *State, id string) { _ = s.Vote(id, "a", Yes, 100, 110) },
		"threshold": func(s *State, id string) {
			_ = s.Vote(id, "a", No, 600, 110)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestState(t)
			if err := s.Submit(proposal(Parameter, 1), 1000, 100); err != nil {
				t.Fatal(err)
			}
			id := ProposalID("arne:devnet", "arce-devnet1proposer", 1)
			votes(s, id)
			s.Advance(200)
			if got, _ := s.Get(id); got.Status != Rejected {
				t.Fatalf("status %s", got.Status)
			}
			if err := s.Queue(id, 200); !errors.Is(err, ErrStatus) {
				t.Fatalf("queue: %v", err)
			}
		})
	}
}

func TestDoubleVoteRejected(t *testing.T) {
	s := newTestState(t)
	if err := s.Submit(proposal(Parameter, 1), 1000, 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", "arce-devnet1proposer", 1)
	if err := s.Vote(id, "a", Yes, 100, 110); err != nil {
		t.Fatal(err)
	}
	if err := s.Vote(id, "a", No, 100, 111); !errors.Is(err, ErrVoted) {
		t.Fatalf("second vote: %v", err)
	}
	if err := s.Vote(id, "b", Yes, 901, 112); !errors.Is(err, ErrPower) {
		t.Fatalf("over power: %v", err)
	}
}

func TestCancel(t *testing.T) {
	s := newTestState(t)
	if err := s.Submit(proposal(Parameter, 1), 1000, 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", "arce-devnet1proposer", 1)
	if err := s.Cancel(id, "someone-else", 101); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("non-proposer cancel: %v", err)
	}
	if err := s.Cancel(id, "arce-devnet1proposer", 101); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(id)
	if got.Status != Cancelled || !got.Refunded {
		t.Fatalf("cancel: %+v", got)
	}
	if err := s.Execute(id, 1000, registry(map[string]any{}, false)); !errors.Is(err, ErrStatus) {
		t.Fatalf("execute cancelled: %v", err)
	}
}

func TestDepositThenVoting(t *testing.T) {
	params := testParams()
	params.DepositPeriod = 10
	s, err := New("arne:devnet", params)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(proposal(Parameter, 1), 1000, 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", "arce-devnet1proposer", 1)
	got, _ := s.Get(id)
	if got.Status != Deposit || got.VotingStart != 110 || got.VotingEnd != 210 {
		t.Fatalf("submitted: %+v", got)
	}
	if err := s.Vote(id, "a", Yes, 100, 109); !errors.Is(err, ErrStatus) {
		t.Fatalf("vote before start: %v", err)
	}
	s.Advance(110)
	if got.Status != Voting {
		t.Fatalf("status %s", got.Status)
	}
	if err := s.Vote(id, "a", Yes, 100, 110); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicFailureUndoesEarlierActions(t *testing.T) {
	s := newTestState(t)
	p := proposal(Parameter, 1)
	p.Actions = []Action{
		{Type: "set", Params: map[string]any{"name": "x", "value": 1}},
		{Type: "fail"},
	}
	if err := s.Submit(p, 1000, 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", "arce-devnet1proposer", 1)
	if err := s.Vote(id, "a", Yes, 600, 110); err != nil {
		t.Fatal(err)
	}
	s.Advance(200)
	if err := s.Queue(id, 200); err != nil {
		t.Fatal(err)
	}
	applied := map[string]any{}
	if err := s.Execute(id, 250, registry(applied, false)); err == nil {
		t.Fatal("expected failure")
	}
	got, _ := s.Get(id)
	if got.Status != Failed || got.ExecutionError == "" {
		t.Fatalf("failed: %+v", got)
	}
	if len(applied) != 0 {
		t.Fatalf("actions not undone: %v", applied)
	}
}

func TestConstitutionalNeedsSupermajorityAndLongerTimelock(t *testing.T) {
	s := newTestState(t)
	p := proposal(Constitutional, 1)
	if err := s.Submit(p, 1000, 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", "arce-devnet1proposer", 1)
	if err := s.Vote(id, "a", Yes, 600, 110); err != nil {
		t.Fatal(err)
	}
	if err := s.Vote(id, "b", No, 400, 111); err != nil {
		t.Fatal(err)
	}
	s.Advance(200)
	if got, _ := s.Get(id); got.Status != Rejected {
		t.Fatalf("60%% should fail a 66.67%% threshold: %s", got.Status)
	}

	s = newTestState(t)
	if err := s.Submit(p, 1000, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.Vote(id, "a", Yes, 700, 110); err != nil {
		t.Fatal(err)
	}
	if err := s.Vote(id, "b", No, 300, 111); err != nil {
		t.Fatal(err)
	}
	s.Advance(200)
	if err := s.Queue(id, 200); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(id)
	if got.TimelockEnd != 700 || got.Status != Timelocked {
		t.Fatalf("constitutional timelock: %+v", got)
	}
}

func TestSubmitValidation(t *testing.T) {
	s := newTestState(t)
	cases := map[string]Proposal{
		"low deposit":   func() Proposal { p := proposal(Parameter, 1); p.Deposit = 99; return p }(),
		"no actions":    func() Proposal { p := proposal(Parameter, 1); p.Actions = nil; return p }(),
		"bad kind":      func() Proposal { p := proposal("bogus", 1); return p }(),
		"no proposer":   func() Proposal { p := proposal(Parameter, 1); p.Proposer = ""; return p }(),
		"no activation": func() Proposal { p := proposal(Parameter, 1); p.ActivationHeight = 0; return p }(),
	}
	for name, p := range cases {
		if err := s.Submit(p, 1000, 100); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := s.Submit(proposal(Parameter, 1), 0, 100); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero power: %v", err)
	}
	if err := s.Submit(proposal(Parameter, 1), 1000, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(proposal(Parameter, 1), 1000, 100); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
}
