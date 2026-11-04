package p2p

import (
	"errors"
	"strings"
	"testing"

	cmtcfg "github.com/cometbft/cometbft/config"
)

func TestOutboundOnlyClearsTheAdvertisedAddress(t *testing.T) {
	config := publicConfig(t)
	config.OutboundOnly = true
	config.PersistentPeers = []string{nodeA + "@10.0.0.1:26656"}
	config.AdvertisedAddress = ""

	var cfg cmtcfg.Config
	if err := config.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.P2P.ExternalAddress != "" {
		t.Fatalf("external address = %q", cfg.P2P.ExternalAddress)
	}
	if cfg.P2P.PersistentPeers != nodeA+"@10.0.0.1:26656" {
		t.Fatalf("persistent peers = %q", cfg.P2P.PersistentPeers)
	}
	report := config.Report()
	if !report.OutboundOnly || report.Reachable {
		t.Fatalf("report = %+v", report)
	}
}

func TestOutboundOnlyRejectsAdvertisingAndNoDialTarget(t *testing.T) {
	config := publicConfig(t)
	config.OutboundOnly = true
	config.AdvertisedAddress = "tcp://node.example.com:26656"
	config.PersistentPeers = []string{nodeA + "@10.0.0.1:26656"}
	if err := config.Validate(); !strings.Contains(err.Error(), "must not advertise") {
		t.Fatalf("advertised outbound-only = %v", err)
	}

	config.AdvertisedAddress = ""
	config.PersistentPeers = nil
	if err := config.Validate(); !strings.Contains(err.Error(), "need a seed") {
		t.Fatalf("outbound-only without a dial target = %v", err)
	}
	config.Seeds = []string{nodeA + "@10.0.0.1:26656"}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateAdvertisedAddressIsReportedButKeepsRunning(t *testing.T) {
	config := publicConfig(t)
	config.AdvertisedAddress = "tcp://192.168.1.10:26656"
	if err := config.Validate(); err != nil {
		t.Fatalf("validation rejected a usable config: %v", err)
	}
	report := config.Report()
	if !report.Advertised || report.Reachable || !report.Mismatch {
		t.Fatalf("report = %+v", report)
	}
	if report.OutboundOnly {
		t.Fatal("a private advertised address was treated as configured outbound-only")
	}

	var cfg cmtcfg.Config
	if err := config.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.P2P.ExternalAddress != config.AdvertisedAddress {
		t.Fatalf("external address = %q", cfg.P2P.ExternalAddress)
	}
}

func TestPublicAdvertisedAddressIsReachable(t *testing.T) {
	config := publicConfig(t)
	config.AdvertisedAddress = "tcp://203.0.113.7:26656"
	report := config.Report()
	if !report.Advertised || !report.Reachable || report.Mismatch {
		t.Fatalf("report = %+v", report)
	}
	config.AdvertisedAddress = "tcp://node.example.com:26656"
	report = config.Report()
	if !report.Reachable || report.Mismatch {
		t.Fatalf("hostname report = %+v", report)
	}
}

func TestNoAdvertisedAddressMeansOutboundOnly(t *testing.T) {
	config := publicConfig(t)
	report := config.Report()
	if !report.OutboundOnly || report.Advertised || report.Mismatch {
		t.Fatalf("report = %+v", report)
	}
}

func TestConsortiumPrivateAddressIsExpected(t *testing.T) {
	config := consortiumConfig(t)
	config.AdvertisedAddress = "tcp://10.0.0.1:26656"
	report := config.Report()
	if !report.Advertised || !report.Reachable || report.Mismatch {
		t.Fatalf("report = %+v", report)
	}
}

func TestRelayAndAnchorPeersBecomePersistent(t *testing.T) {
	config := publicConfig(t)
	config.PersistentPeers = []string{nodeA + "@10.0.0.1:26656"}
	relay := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	anchor := "cccccccccccccccccccccccccccccccccccccccc"
	config.RelayPeers = []string{relay + "@10.0.0.2:26656"}
	config.AnchorPeers = []string{anchor + "@10.0.0.3:26656", nodeA + "@10.0.0.1:26656"}

	var cfg cmtcfg.Config
	if err := config.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	peers := cfg.P2P.PersistentPeers
	for _, want := range []string{
		nodeA + "@10.0.0.1:26656",
		relay + "@10.0.0.2:26656",
		anchor + "@10.0.0.3:26656",
	} {
		if !strings.Contains(peers, want) {
			t.Fatalf("persistent peers %q missing %q", peers, want)
		}
	}
	if strings.Count(peers, nodeA) != 1 {
		t.Fatalf("anchor duplicated a persistent peer: %q", peers)
	}
	if cfg.P2P.UnconditionalPeerIDs != anchor+","+nodeA {
		t.Fatalf("unconditional peers = %q", cfg.P2P.UnconditionalPeerIDs)
	}
	if report := config.Report(); report.RelayPeers != 1 || report.AnchorPeers != 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestBadRelayOrAnchorPeerIsRejected(t *testing.T) {
	config := publicConfig(t)
	config.RelayPeers = []string{"not-a-peer"}
	if err := config.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad relay = %v", err)
	}
	config = publicConfig(t)
	config.AnchorPeers = []string{nodeA + "@10.0.0.1:26656", nodeA + "@10.0.0.2:26656"}
	if err := config.Validate(); !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("duplicate anchor = %v", err)
	}
}
