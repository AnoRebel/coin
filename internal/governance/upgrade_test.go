package governance

import (
	"errors"
	"strings"
	"testing"
)

const (
	approvedHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherHash    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rollbackHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func upgradeAction() Action {
	return Action{Type: "code_upgrade", Params: map[string]any{
		"target":        "binary",
		"hash":          approvedHash,
		"previous_hash": rollbackHash,
		"migration":     "export state and replay from height 900",
		"rollback":      "run the previous release against the snapshot",
		"version":       "v2.0.0",
	}}
}

func passedUpgrade(t *testing.T) (*State, string) {
	t.Helper()
	s := newTestState(t)
	p := Proposal{
		Kind: CodeUpgrade, Proposer: "arce-devnet1proposer", Nonce: 7,
		Title: "upgrade", Actions: []Action{upgradeAction()},
		Deposit: 100, ActivationHeight: 750,
	}
	if err := s.Submit(p, 1000, 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", p.Proposer, 7)
	if err := s.Vote(id, "voter-a", Yes, 600, 110); err != nil {
		t.Fatal(err)
	}
	s.Advance(200)
	if err := s.Queue(id, 200); err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestUpgradeActivatesOnMatchingHash(t *testing.T) {
	s, id := passedUpgrade(t)
	upgrader := &Upgrader{Running: func() (string, error) { return approvedHash, nil }}
	r := NewRegistry()
	if err := upgrader.Register(r); err != nil {
		t.Fatal(err)
	}
	if err := s.Execute(id, 250, r); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(id)
	if got.Status != Executed {
		t.Fatalf("status %s", got.Status)
	}
	if upgrader.Applied == nil {
		t.Fatal("no applied plan")
	}
	if upgrader.Applied.Proposal != id || upgrader.Applied.Activation != 750 ||
		upgrader.Applied.Previous != rollbackHash || upgrader.Applied.Version != "v2.0.0" {
		t.Fatalf("applied: %+v", upgrader.Applied)
	}
}

func TestUpgradeHashMismatchBlocksActivation(t *testing.T) {
	s, id := passedUpgrade(t)
	upgrader := &Upgrader{Running: func() (string, error) { return otherHash, nil }}
	r := NewRegistry()
	if err := upgrader.Register(r); err != nil {
		t.Fatal(err)
	}
	err := s.Execute(id, 250, r)
	if !errors.Is(err, ErrHash) {
		t.Fatalf("want hash error, got %v", err)
	}
	got, _ := s.Get(id)
	if got.Status != Failed || !strings.Contains(got.ExecutionError, "hash") {
		t.Fatalf("status %s error %q", got.Status, got.ExecutionError)
	}
	if upgrader.Applied != nil {
		t.Fatalf("applied %+v", upgrader.Applied)
	}
}

func TestCodeUpgradeValidation(t *testing.T) {
	s := newTestState(t)
	notUpgrade := Proposal{
		Kind: CodeUpgrade, Proposer: "p", Nonce: 1, Deposit: 100, ActivationHeight: 1,
		Actions: []Action{{Type: "set", Params: map[string]any{"name": "x", "value": 1}}},
	}
	if err := s.Submit(notUpgrade, 1000, 100); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing upgrade action: %v", err)
	}
	parameter := Proposal{
		Kind: Parameter, Proposer: "p", Nonce: 1, Deposit: 100, ActivationHeight: 1,
		Actions: []Action{upgradeAction()},
	}
	if err := s.Submit(parameter, 1000, 100); !errors.Is(err, ErrInvalid) {
		t.Fatalf("upgrade action on parameter proposal: %v", err)
	}
}
