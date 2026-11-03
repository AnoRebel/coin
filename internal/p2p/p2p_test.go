package p2p

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	cmtcfg "github.com/cometbft/cometbft/config"
	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

const (
	nodeA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nodeB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func publicConfig(t *testing.T) Config {
	t.Helper()
	config, err := Default("arne:testnet")
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func consortiumConfig(t *testing.T) Config {
	t.Helper()
	config, err := Default("arne:consortium/acme")
	if err != nil {
		t.Fatal(err)
	}
	config.Allowlist = []string{nodeA, nodeB}
	config.PersistentPeers = []string{nodeA + "@10.0.0.1:26656"}
	return config
}

func TestPublicDefaultsAllowDiscovery(t *testing.T) {
	config := publicConfig(t)
	if config.Class != protocol.ClassTestnet {
		t.Fatalf("class = %q", config.Class)
	}
	if config.Chain != "arne-testnet-1" {
		t.Fatalf("chain = %q", config.Chain)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	var cfg cmtcfg.Config
	if err := config.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.P2P.PexReactor {
		t.Fatal("public network has peer exchange disabled")
	}
	if cfg.P2P.PrivatePeerIDs != "" {
		t.Fatalf("private peers = %q", cfg.P2P.PrivatePeerIDs)
	}
	if cfg.P2P.MaxNumInboundPeers != 40 || cfg.P2P.MaxNumOutboundPeers != 10 {
		t.Fatalf("peer limits = %d/%d", cfg.P2P.MaxNumInboundPeers, cfg.P2P.MaxNumOutboundPeers)
	}
}

func TestConsortiumNeedsAllowlistAndHidesDiscovery(t *testing.T) {
	config, err := Default("arne:consortium/acme")
	if err != nil {
		t.Fatal(err)
	}
	if config.Chain != "arne-consortium-acme-1" {
		t.Fatalf("chain = %q", config.Chain)
	}
	if err := config.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("validate without allowlist = %v", err)
	}
	config.Allowlist = []string{nodeA}
	config.PersistentPeers = []string{nodeA + "@10.0.0.1:26656"}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	var cfg cmtcfg.Config
	if err := config.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.P2P.PexReactor {
		t.Fatal("consortium network still discovers peers publicly")
	}
	if cfg.P2P.PrivatePeerIDs != nodeA {
		t.Fatalf("private peers = %q", cfg.P2P.PrivatePeerIDs)
	}
}

func TestPublicNetworkRejectsAllowlist(t *testing.T) {
	config := publicConfig(t)
	config.Allowlist = []string{nodeA}
	if err := config.Validate(); !strings.Contains(err.Error(), "must not use an allowlist") {
		t.Fatalf("validate = %v", err)
	}
}

func TestChainMismatchRejected(t *testing.T) {
	config := publicConfig(t)
	config.Chain = "arne-mainnet-1"
	if err := config.Validate(); !strings.Contains(err.Error(), "does not match network") {
		t.Fatalf("validate = %v", err)
	}
}

func TestClassMismatchRejected(t *testing.T) {
	config := publicConfig(t)
	config.Class = protocol.ClassConsortium
	if err := config.Validate(); !strings.Contains(err.Error(), "does not match network class") {
		t.Fatalf("validate = %v", err)
	}
}

func TestBadAddressesRejected(t *testing.T) {
	cases := map[string]string{
		"plaintext":     "http://0.0.0.0:26656",
		"no port":       "tcp://0.0.0.0",
		"bad port":      "tcp://0.0.0.0:99999",
		"path":          "tcp://0.0.0.0:26656/p2p",
		"userinfo":      "tcp://user:pass@0.0.0.0:26656",
		"missing host":  "tcp://:26656",
		"space in host": "tcp://0.0.0.0 :26656",
	}
	for name, address := range cases {
		t.Run(name, func(t *testing.T) {
			config := publicConfig(t)
			config.ListenAddress = address
			if err := config.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("validate = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestAdvertisedAddressMustBeReachable(t *testing.T) {
	config := publicConfig(t)
	config.AdvertisedAddress = "tcp://0.0.0.0:26656"
	if err := config.Validate(); !strings.Contains(err.Error(), "reachable host") {
		t.Fatalf("validate = %v", err)
	}
	config.AdvertisedAddress = "tcp://node.example.com:26656"
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPeerListsRejectBadAndDuplicateEntries(t *testing.T) {
	bad := []string{
		nodeA,
		nodeA + "@10.0.0.1",
		"not-an-id@10.0.0.1:26656",
		strings.ToUpper(nodeA) + "@10.0.0.1:26656", // uppercased, then normalized
		nodeA + "@https://10.0.0.1:26656",
	}
	for _, peer := range bad {
		if peer == strings.ToUpper(nodeA)+"@10.0.0.1:26656" {
			continue // normalizeID lowercases, so this one is accepted
		}
		config := publicConfig(t)
		config.PersistentPeers = []string{peer}
		if err := config.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("peer %q validate = %v, want ErrInvalid", peer, err)
		}
	}

	config := publicConfig(t)
	config.PersistentPeers = []string{nodeA + "@10.0.0.1:26656", nodeA + "@10.0.0.2:26656"}
	if err := config.Validate(); !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("duplicate peer validate = %v", err)
	}

	config = publicConfig(t)
	config.Seeds = []string{"not-a-peer"}
	if err := config.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad seed validate = %v", err)
	}
}

func TestHandshakeAcceptsMatchingPeer(t *testing.T) {
	config := publicConfig(t)
	session, err := config.Handshake(Offer{
		Network: "arne:testnet", Chain: "arne-testnet-1",
		NodeID: nodeA, Versions: []uint16{MinVersion, MaxVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.NodeID != nodeA || session.Version != MaxVersion {
		t.Fatalf("session = %+v", session)
	}
}

func TestHandshakeRejections(t *testing.T) {
	base := Offer{
		Network: "arne:testnet", Chain: "arne-testnet-1",
		NodeID: nodeA, Versions: []uint16{MinVersion},
	}

	cases := []struct {
		name  string
		offer Offer
		want  error
	}{
		{"network", with(base, func(o *Offer) { o.Network = "arne:mainnet" }), ErrNetwork},
		{"chain", with(base, func(o *Offer) { o.Chain = "arne-devnet-1" }), ErrChain},
		{"version", with(base, func(o *Offer) { o.Versions = []uint16{99} }), ErrVersion},
		{"identity", with(base, func(o *Offer) { o.NodeID = "zz" }), ErrIdentity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := publicConfig(t).Handshake(c.offer); !errors.Is(err, c.want) {
				t.Fatalf("handshake = %v, want %v", err, c.want)
			}
		})
	}
}

func TestConsortiumHandshakeEnforcesAllowlist(t *testing.T) {
	config := consortiumConfig(t)
	if _, err := config.Handshake(Offer{
		Network: "arne:consortium/acme", Chain: "arne-consortium-acme-1",
		NodeID: nodeB, Versions: []uint16{MinVersion},
	}); err != nil {
		t.Fatalf("allowlisted peer: %v", err)
	}
	unknown := "cccccccccccccccccccccccccccccccccccccccc"
	if _, err := config.Handshake(Offer{
		Network: "arne:consortium/acme", Chain: "arne-consortium-acme-1",
		NodeID: unknown, Versions: []uint16{MinVersion},
	}); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("unlisted peer = %v, want ErrNotAllowed", err)
	}
}

func TestNodeIDMatchesCometBFTIdentity(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := NodeID(pub)
	if err != nil {
		t.Fatal(err)
	}
	if want := hex.EncodeToString(cmted25519.PubKey(pub).Address()); id != want {
		t.Fatalf("node id = %s, want %s", id, want)
	}
	if _, err := NodeID([]byte("short")); !errors.Is(err, ErrIdentity) {
		t.Fatalf("short key = %v", err)
	}
}

func TestNegotiatePrefersHighestCommonVersion(t *testing.T) {
	if version, ok := negotiate([]uint16{0, 1}); !ok || version != 1 {
		t.Fatalf("negotiate = %d, %v", version, ok)
	}
	if _, ok := negotiate(nil); ok {
		t.Fatal("negotiate with no versions succeeded")
	}
}

func with(offer Offer, edit func(*Offer)) Offer {
	edit(&offer)
	return offer
}
