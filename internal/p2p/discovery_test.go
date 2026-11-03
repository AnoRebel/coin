package p2p

import (
	"errors"
	"testing"
)

func TestPublicDiscoveryJoinsAndBacksOff(t *testing.T) {
	policy := publicConfig(t)
	discovery, err := NewDiscovery(policy, 2, 5, 20)
	if err != nil {
		t.Fatal(err)
	}

	status, err := discovery.Observe(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateDegraded || status.Peers != 0 || status.RetryAt != 5 {
		t.Fatalf("empty bootstrap = %+v", status)
	}

	// Before the retry time nothing is dialled, so a peer that came back early
	// is not counted yet.
	status, err = discovery.Observe(4, []string{nodeA})
	if err != nil {
		t.Fatal(err)
	}
	if status.Peers != 0 || status.RetryAt != 5 {
		t.Fatalf("early observe = %+v", status)
	}

	status, err = discovery.Observe(5, []string{nodeA})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateDegraded || status.Peers != 1 || status.RetryAt != 15 {
		t.Fatalf("first retry = %+v", status)
	}

	status, err = discovery.Observe(15, []string{nodeB})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateJoined || status.Peers != 2 || status.RetryAt != 0 {
		t.Fatalf("joined = %+v", status)
	}
	if discovery.RetryAt() != 0 {
		t.Fatalf("retry at = %d after joining", discovery.RetryAt())
	}
}

func TestBackoffIsCapped(t *testing.T) {
	discovery, err := NewDiscovery(publicConfig(t), 1, 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{5, 15, 25, 35}
	now := int64(0)
	for i, retryAt := range want {
		status, err := discovery.Observe(now, nil)
		if err != nil {
			t.Fatal(err)
		}
		if status.RetryAt != retryAt {
			t.Fatalf("attempt %d retry at %d, want %d", i, status.RetryAt, retryAt)
		}
		now = status.RetryAt
	}
}

func TestConsortiumDiscoveryIgnoresUnlistedPeers(t *testing.T) {
	policy := consortiumConfig(t)
	discovery, err := NewDiscovery(policy, 1, 5, 20)
	if err != nil {
		t.Fatal(err)
	}
	unknown := "cccccccccccccccccccccccccccccccccccccccc"
	status, err := discovery.Observe(0, []string{unknown})
	if err != nil {
		t.Fatal(err)
	}
	if status.Peers != 0 || status.State != StateDegraded {
		t.Fatalf("unlisted peer counted = %+v", status)
	}
	status, err = discovery.Observe(5, []string{nodeA})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateJoined || status.Peers != 1 {
		t.Fatalf("allowlisted peer = %+v", status)
	}
}

func TestAdmitEnforcesAllowlistAndCapacity(t *testing.T) {
	policy, err := Default("arne:consortium/acme")
	if err != nil {
		t.Fatal(err)
	}
	policy.MaxInbound = 2
	policy.MaxOutbound = 1
	extra := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	policy.Allowlist = []string{nodeA, nodeB, "cccccccccccccccccccccccccccccccccccccccc", extra}
	discovery, err := NewDiscovery(policy, 1, 5, 20)
	if err != nil {
		t.Fatal(err)
	}

	offer := func(nodeID string) Offer {
		return Offer{
			Network: "arne:consortium/acme", Chain: "arne-consortium-acme-1",
			NodeID: nodeID, Versions: []uint16{MinVersion},
		}
	}
	if _, err := discovery.Admit(offer("dddddddddddddddddddddddddddddddddddddddd")); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("unlisted admit = %v", err)
	}
	for _, id := range policy.Allowlist[:3] {
		if _, err := discovery.Admit(offer(id)); err != nil {
			t.Fatalf("admit %s: %v", id, err)
		}
	}
	if _, err := discovery.Admit(offer(extra)); !errors.Is(err, ErrFull) {
		t.Fatalf("admit over capacity = %v", err)
	}
}

func TestDisconnectDegrades(t *testing.T) {
	discovery, err := NewDiscovery(publicConfig(t), 2, 5, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := discovery.Observe(0, []string{nodeA, nodeB}); err != nil {
		t.Fatal(err)
	}
	discovery.Disconnect(nodeB)
	if got := discovery.Peers(); len(got) != 1 || got[0] != nodeA {
		t.Fatalf("peers = %v", got)
	}
	status, err := discovery.Observe(5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateDegraded || status.Peers != 1 {
		t.Fatalf("after disconnect = %+v", status)
	}
}

func TestDiscoveryRejectsBadConfiguration(t *testing.T) {
	if _, err := NewDiscovery(publicConfig(t), 0, 5, 20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero min peers = %v", err)
	}
	if _, err := NewDiscovery(publicConfig(t), 100, 5, 20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("min peers above the cap = %v", err)
	}
	if _, err := NewDiscovery(publicConfig(t), 1, 0, 20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero backoff = %v", err)
	}
	if _, err := NewDiscovery(publicConfig(t), 1, 10, 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("max backoff below backoff = %v", err)
	}
	discovery, err := NewDiscovery(publicConfig(t), 1, 5, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := discovery.Observe(0, []string{"bad"}); !errors.Is(err, ErrIdentity) {
		t.Fatalf("bad peer id = %v", err)
	}
}
