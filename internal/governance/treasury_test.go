package governance

import (
	"errors"
	"testing"
)

func TestTreasurySpendAction(t *testing.T) {
	treasury := NewTreasuryBook()
	if err := treasury.SetBudget("grants", "arce", 500); err != nil {
		t.Fatal(err)
	}
	if _, err := treasury.Deposit("arce", 1000, "fees", "fee income", 10); err != nil {
		t.Fatal(err)
	}
	now := uint64(30)
	s := newTestState(t)
	p := Proposal{
		Kind: Treasury, Proposer: "arce-devnet1proposer", Nonce: 9,
		Title: "grant", Deposit: 100, ActivationHeight: 600,
		Actions: []Action{{Type: "treasury_spend", Params: map[string]any{
			"asset": "arce", "amount": float64(250), "recipient": "arce1grant",
			"purpose": "grant", "category": "grants",
		}}},
	}
	if err := s.Submit(p, 1000, 100); err != nil {
		t.Fatal(err)
	}
	id := ProposalID("arne:devnet", p.Proposer, 9)
	if err := s.Vote(id, "voter-a", Yes, 600, 110); err != nil {
		t.Fatal(err)
	}
	s.Advance(200)
	if err := s.Queue(id, 200); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	if err := treasury.Register(r, func() uint64 { return now }); err != nil {
		t.Fatal(err)
	}
	if err := s.Execute(id, 250, r); err != nil {
		t.Fatal(err)
	}
	if treasury.Balances["arce"] != 750 {
		t.Fatalf("balance %d", treasury.Balances["arce"])
	}
	if last := treasury.Entries[len(treasury.Entries)-1]; last.Proposal != id || last.Amount != 250 {
		t.Fatalf("entry %+v", last)
	}
	report, err := treasury.Report(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if report.Outflows["arce"] != 250 || report.Categories["grants"] != 250 {
		t.Fatalf("report %+v", report)
	}
}

func TestTreasuryRejectsNonTreasuryProposal(t *testing.T) {
	treasury := NewTreasuryBook()
	if err := treasury.SetBudget("grants", "arce", 500); err != nil {
		t.Fatal(err)
	}
	if _, err := treasury.Deposit("arce", 1000, "fees", "fee income", 10); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	if err := treasury.Register(r, func() uint64 { return 20 }); err != nil {
		t.Fatal(err)
	}
	_, err := r.handlers["treasury_spend"](Action{Params: map[string]any{
		"asset": "arce", "amount": uint64(100), "recipient": "r",
		"purpose": "grant", "category": "grants",
	}}, &Proposal{Kind: Parameter, ID: "p"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want invalid, got %v", err)
	}
	if treasury.Balances["arce"] != 1000 {
		t.Fatalf("balance %d", treasury.Balances["arce"])
	}
}
