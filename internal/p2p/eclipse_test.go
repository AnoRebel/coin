package p2p

import (
	"errors"
	"testing"
)

const (
	nodeC = "cccccccccccccccccccccccccccccccccccccccc"
	nodeD = "dddddddddddddddddddddddddddddddddddddddd"
)

func TestDiversityRejectsConcentration(t *testing.T) {
	diversity, err := NewDiversity(5000)
	if err != nil {
		t.Fatal(err)
	}
	balanced := []PeerGroup{
		{NodeID: nodeA, Group: "operator-a"},
		{NodeID: nodeB, Group: "operator-b"},
		{NodeID: nodeC, Group: "operator-a"},
		{NodeID: nodeD, Group: "operator-b"},
	}
	if err := diversity.Check(balanced); err != nil {
		t.Fatal(err)
	}

	concentrated := []PeerGroup{
		{NodeID: nodeA, Group: "operator-a"},
		{NodeID: nodeB, Group: "operator-a"},
		{NodeID: nodeC, Group: "operator-b"},
	}
	if err := diversity.Check(concentrated); !errors.Is(err, ErrEclipse) {
		t.Fatalf("concentrated set = %v, want ErrEclipse", err)
	}
}

func TestDiversityRejectsBadSets(t *testing.T) {
	diversity, err := NewDiversity(5000)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]PeerGroup{
		"empty":     {},
		"duplicate": {{NodeID: nodeA, Group: "a"}, {NodeID: nodeA, Group: "b"}},
		"no group":  {{NodeID: nodeA}},
		"no id":     {{Group: "a"}},
		"bad id":    {{NodeID: "short", Group: "a"}},
	}
	for name, peers := range cases {
		t.Run(name, func(t *testing.T) {
			if err := diversity.Check(peers); !errors.Is(err, ErrDiversity) && !errors.Is(err, ErrIdentity) {
				t.Fatalf("check = %v", err)
			}
		})
	}
	if _, err := NewDiversity(0); !errors.Is(err, ErrDiversity) {
		t.Fatalf("zero share = %v", err)
	}
	if _, err := NewDiversity(10001); !errors.Is(err, ErrDiversity) {
		t.Fatalf("share above 10000 = %v", err)
	}
}

func TestFinalityIsRefusedWithoutQuorum(t *testing.T) {
	partition, err := NewPartition(6667, 60)
	if err != nil {
		t.Fatal(err)
	}
	if err := partition.CheckFinality(3, 4); err != nil {
		t.Fatalf("three of four: %v", err)
	}
	if err := partition.CheckFinality(2, 3); !errors.Is(err, ErrNoQuorum) {
		t.Fatalf("two of three = %v, want ErrNoQuorum", err)
	}
	if err := partition.CheckFinality(4, 4); err != nil {
		t.Fatalf("all validators: %v", err)
	}
}

func TestPartitionReportsAfterThresholdAndRecovers(t *testing.T) {
	partition, err := NewPartition(6667, 60)
	if err != nil {
		t.Fatal(err)
	}
	// Three of four validators is a quorum.
	status, err := partition.Observe(0, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !status.CanFinalize || status.Partitioned {
		t.Fatalf("quorum status = %+v", status)
	}

	// Losing a validator leaves two of four, below quorum. Finality stops at
	// once but a partition is not reported until the threshold.
	status, err = partition.Observe(10, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if status.CanFinalize || status.Partitioned {
		t.Fatalf("early loss status = %+v", status)
	}
	status, err = partition.Observe(69, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if status.Partitioned || status.CanFinalize {
		t.Fatalf("just before threshold = %+v", status)
	}
	status, err = partition.Observe(70, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Partitioned || status.CanFinalize || status.Since != 10 {
		t.Fatalf("threshold status = %+v", status)
	}

	// An anchor peer reconnects and restores quorum.
	status, err = partition.Observe(80, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if status.Partitioned || !status.CanFinalize || status.Since != 0 || status.PowerBPS != 10000 {
		t.Fatalf("recovered status = %+v", status)
	}
}

func TestPartitionRejectsBadInput(t *testing.T) {
	if _, err := NewPartition(0, 10); !errors.Is(err, ErrDiversity) {
		t.Fatalf("zero quorum = %v", err)
	}
	if _, err := NewPartition(6667, 0); !errors.Is(err, ErrDiversity) {
		t.Fatalf("zero threshold = %v", err)
	}
	partition, err := NewPartition(6667, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partition.Observe(0, 0, 0); !errors.Is(err, ErrDiversity) {
		t.Fatalf("zero total power = %v", err)
	}
	if _, err := partition.Observe(0, 5, 4); !errors.Is(err, ErrDiversity) {
		t.Fatalf("signed above total = %v", err)
	}
	if _, err := partition.Observe(-1, 1, 1); !errors.Is(err, ErrDiversity) {
		t.Fatalf("negative time = %v", err)
	}
}
