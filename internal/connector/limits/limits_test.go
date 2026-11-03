package limits

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type vectorFile struct {
	Policy Policy `json:"policy"`
	Cases  []struct {
		Name         string `json:"name"`
		Kind         string `json:"kind"`
		Asset        string `json:"asset"`
		Counterparty string `json:"counterparty"`
		Amount       uint64 `json:"amount"`
		Now          int64  `json:"now"`
		Policy       Policy `json:"policy"`
		Actor        string `json:"actor"`
		Reason       string `json:"reason"`
		ID           string `json:"id"`
		Expect       struct {
			Error                string `json:"error"`
			Asset                string `json:"asset"`
			Actor                string `json:"actor"`
			Reason               string `json:"reason"`
			At                   int64  `json:"at"`
			HasPrior             *bool  `json:"has_prior"`
			PriorExposure        uint64 `json:"prior_exposure"`
			NextExposure         uint64 `json:"next_exposure"`
			Ordinal              int    `json:"ordinal"`
			Amount               uint64 `json:"amount"`
			Exposure             uint64 `json:"exposure"`
			CounterpartyExposure uint64 `json:"counterparty_exposure"`
			Released             *bool  `json:"released"`
			Changes              []struct {
				Actor         string `json:"actor"`
				Reason        string `json:"reason"`
				At            int64  `json:"at"`
				HasPrior      bool   `json:"has_prior"`
				PriorExposure uint64 `json:"prior_exposure"`
				NextExposure  uint64 `json:"next_exposure"`
			} `json:"changes"`
		} `json:"expect"`
	} `json:"cases"`
}

func load(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/vectors/connector_limits.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestVectors(t *testing.T) {
	file := load(t)
	book := New()

	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Kind {
			case "govern":
				change, err := book.Govern(c.Policy, c.Actor, c.Reason, c.Now)
				if c.Expect.Error != "" {
					expectError(t, err, c.Expect.Error)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if change.Asset != c.Expect.Asset || change.Actor != c.Expect.Actor ||
					change.Reason != c.Expect.Reason || change.At != c.Expect.At {
					t.Fatalf("change = %+v", change)
				}
				if c.Expect.HasPrior != nil && (change.Prior != Policy{}) != *c.Expect.HasPrior {
					t.Fatalf("has_prior = %v, want %v", change.Prior != Policy{}, *c.Expect.HasPrior)
				}
				if change.Prior.MaxExposure != c.Expect.PriorExposure {
					t.Fatalf("prior exposure = %d, want %d", change.Prior.MaxExposure, c.Expect.PriorExposure)
				}
				if change.Next.MaxExposure != c.Expect.NextExposure {
					t.Fatalf("next exposure = %d, want %d", change.Next.MaxExposure, c.Expect.NextExposure)
				}
			case "submit":
				reservation, err := book.Submit(c.Asset, c.Counterparty, c.Amount, c.Now)
				if c.Expect.Error != "" {
					expectError(t, err, c.Expect.Error)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if reservation.Amount != c.Expect.Amount {
					t.Fatalf("amount = %d, want %d", reservation.Amount, c.Expect.Amount)
				}
				if got := book.Exposure(c.Asset); got != c.Expect.Exposure {
					t.Fatalf("exposure = %d, want %d", got, c.Expect.Exposure)
				}
				if got := book.CounterpartyExposure(c.Asset, c.Counterparty); got != c.Expect.CounterpartyExposure {
					t.Fatalf("counterparty exposure = %d, want %d", got, c.Expect.CounterpartyExposure)
				}
			case "release":
				reservation, err := book.Release(c.ID)
				if c.Expect.Error != "" {
					expectError(t, err, c.Expect.Error)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if !reservation.Released {
					t.Fatal("released = false")
				}
				if reservation.Amount != c.Expect.Amount {
					t.Fatalf("amount = %d, want %d", reservation.Amount, c.Expect.Amount)
				}
			case "exposure":
				if got := book.Exposure(c.Asset); got != c.Expect.Exposure {
					t.Fatalf("exposure = %d, want %d", got, c.Expect.Exposure)
				}
			case "counterparty_exposure":
				if got := book.CounterpartyExposure(c.Asset, c.Counterparty); got != c.Expect.CounterpartyExposure {
					t.Fatalf("counterparty exposure = %d, want %d", got, c.Expect.CounterpartyExposure)
				}
			case "history":
				history := book.History()
				if len(history) != len(c.Expect.Changes) {
					t.Fatalf("history = %d changes, want %d", len(history), len(c.Expect.Changes))
				}
				for i, want := range c.Expect.Changes {
					got := history[i]
					if got.Actor != want.Actor || got.Reason != want.Reason || got.At != want.At {
						t.Fatalf("change %d = %+v, want %+v", i, got, want)
					}
					if (got.Prior != Policy{}) != want.HasPrior {
						t.Fatalf("change %d has_prior = %v, want %v", i, got.Prior != Policy{}, want.HasPrior)
					}
					if got.Prior.MaxExposure != want.PriorExposure || got.Next.MaxExposure != want.NextExposure {
						t.Fatalf("change %d caps = %d/%d, want %d/%d", i,
							got.Prior.MaxExposure, got.Next.MaxExposure,
							want.PriorExposure, want.NextExposure)
					}
				}
			default:
				t.Fatalf("unknown kind %q", c.Kind)
			}
		})
	}
}

func expectError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestRejectedSubmissionConsumesNoBudget(t *testing.T) {
	book := New()
	policy := Policy{
		Asset: "arce:testnet/arce", MaxExposure: 1000, MaxCounterpartyExposure: 1000,
		MaxTransfer: 100, RateCount: 1, RateWindow: 10,
	}
	if _, err := book.Govern(policy, "ops", "initial", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := book.Submit(policy.Asset, "peer", 100, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := book.Submit(policy.Asset, "peer", 100, 105); err == nil {
		t.Fatal("second submission inside the window was accepted")
	}
	if got := book.Exposure(policy.Asset); got != 100 {
		t.Fatalf("exposure = %d after a rejected submission, want 100", got)
	}
	if _, err := book.Submit(policy.Asset, "peer", 100, 110); err != nil {
		t.Fatalf("submission at the window edge: %v", err)
	}
}

func TestLoweredCapStopsNewReservationsOnly(t *testing.T) {
	book := New()
	policy := Policy{
		Asset: "arce:testnet/arce", MaxExposure: 1000, MaxCounterpartyExposure: 1000,
		MaxTransfer: 1000, RateCount: 10, RateWindow: 60,
	}
	if _, err := book.Govern(policy, "ops", "initial", 0); err != nil {
		t.Fatal(err)
	}
	reservation, err := book.Submit(policy.Asset, "peer", 800, 0)
	if err != nil {
		t.Fatal(err)
	}
	lower := policy
	lower.MaxExposure = 500
	lower.MaxCounterpartyExposure = 500
	lower.MaxTransfer = 500
	if _, err := book.Govern(lower, "risk", "review", 1); err != nil {
		t.Fatalf("lowering below current exposure: %v", err)
	}
	if _, err := book.Submit(policy.Asset, "other", 100, 2); err == nil {
		t.Fatal("submission under a lowered cap was accepted")
	}
	if _, err := book.Release(reservation.ID); err != nil {
		t.Fatalf("releasing an existing reservation: %v", err)
	}
	if got := book.Exposure(policy.Asset); got != 0 {
		t.Fatalf("exposure = %d after release, want 0", got)
	}
}
