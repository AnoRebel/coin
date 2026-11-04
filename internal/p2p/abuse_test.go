package p2p

import (
	"errors"
	"testing"
)

func abusePolicy() AbusePolicy {
	return AbusePolicy{
		RateCount:        3,
		RateWindow:       10,
		InitialScore:     5,
		InvalidPenalty:   2,
		OversizedPenalty: 3,
		ThrottlePenalty:  1,
		BanDuration:      60,
	}
}

func newGuard(t *testing.T) *Guard {
	t.Helper()
	guard, err := NewGuard(abusePolicy())
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

func TestFloodIsThrottledAndPenalized(t *testing.T) {
	guard := newGuard(t)
	for i := 0; i < 3; i++ {
		if err := guard.Allow(nodeA, int64(i)); err != nil {
			t.Fatalf("allowed message %d: %v", i, err)
		}
	}
	if err := guard.Allow(nodeA, 3); !errors.Is(err, ErrThrottled) {
		t.Fatalf("fourth message = %v, want ErrThrottled", err)
	}
	view, err := guard.View(nodeA, 3)
	if err != nil {
		t.Fatal(err)
	}
	if view.Score != 4 || view.Messages != 3 {
		t.Fatalf("view after throttle = %+v", view)
	}

	// The window is half-open: a message exactly one window later is admitted.
	if err := guard.Allow(nodeA, 13); err != nil {
		t.Fatalf("message at the window edge: %v", err)
	}
}

func TestInvalidMessagesLeadToBan(t *testing.T) {
	guard := newGuard(t)
	for i := 0; i < 2; i++ {
		if _, err := guard.Record(nodeA, OutcomeInvalid, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	view, err := guard.Record(nodeA, OutcomeInvalid, 2)
	if err != nil {
		t.Fatal(err)
	}
	if view.Score != 0 || !view.Banned || view.BannedUntil != 62 {
		t.Fatalf("view after ban = %+v", view)
	}
	if err := guard.Admit(nodeA, 3); !errors.Is(err, ErrBanned) {
		t.Fatalf("admit during ban = %v, want ErrBanned", err)
	}
	if err := guard.Allow(nodeA, 4); !errors.Is(err, ErrBanned) {
		t.Fatalf("allow during ban = %v, want ErrBanned", err)
	}
}

func TestBanExpiresAndRestoresScore(t *testing.T) {
	guard := newGuard(t)
	for _, outcome := range []Outcome{OutcomeOversized, OutcomeInvalid, OutcomeInvalid} {
		if _, err := guard.Record(nodeA, outcome, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := guard.Admit(nodeA, 59); !errors.Is(err, ErrBanned) {
		t.Fatalf("admit just before expiry = %v", err)
	}
	if err := guard.Admit(nodeA, 60); err != nil {
		t.Fatalf("admit at expiry: %v", err)
	}
	view, err := guard.View(nodeA, 60)
	if err != nil {
		t.Fatal(err)
	}
	if view.Score != abusePolicy().InitialScore || view.Banned || view.Messages != 0 {
		t.Fatalf("view after expiry = %+v", view)
	}
}

func TestOversizedAndUnknownOutcomes(t *testing.T) {
	guard := newGuard(t)
	view, err := guard.Record(nodeA, OutcomeOversized, 0)
	if err != nil {
		t.Fatal(err)
	}
	if view.Score != 2 {
		t.Fatalf("score after oversized = %d, want 2", view.Score)
	}
	if _, err := guard.Record(nodeA, Outcome("bogus"), 1); !errors.Is(err, ErrAbuse) {
		t.Fatalf("unknown outcome = %v, want ErrAbuse", err)
	}
}

func TestValidMessagesRecoverScoreUpToInitial(t *testing.T) {
	guard := newGuard(t)
	if _, err := guard.Record(nodeA, OutcomeInvalid, 0); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		view, err := guard.Record(nodeA, OutcomeValid, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		if view.Score > abusePolicy().InitialScore {
			t.Fatalf("score %d above initial", view.Score)
		}
	}
	view, err := guard.View(nodeA, 6)
	if err != nil {
		t.Fatal(err)
	}
	if view.Score != abusePolicy().InitialScore {
		t.Fatalf("score = %d, want %d", view.Score, abusePolicy().InitialScore)
	}
}

func TestGuardRejectsBadPolicyAndIdentity(t *testing.T) {
	policy := abusePolicy()
	policy.RateCount = 0
	if _, err := NewGuard(policy); !errors.Is(err, ErrAbuse) {
		t.Fatalf("zero rate count = %v", err)
	}
	policy = abusePolicy()
	policy.InitialScore = 0
	if _, err := NewGuard(policy); !errors.Is(err, ErrAbuse) {
		t.Fatalf("zero score = %v", err)
	}
	policy = abusePolicy()
	policy.BanDuration = 0
	if _, err := NewGuard(policy); !errors.Is(err, ErrAbuse) {
		t.Fatalf("zero ban duration = %v", err)
	}
	guard := newGuard(t)
	if err := guard.Allow("not-a-node", 0); !errors.Is(err, ErrIdentity) {
		t.Fatalf("bad identity = %v", err)
	}
	if err := guard.Allow(nodeA, -1); !errors.Is(err, ErrAbuse) {
		t.Fatalf("negative time = %v", err)
	}
}
