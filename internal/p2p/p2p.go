// Package p2p configures and checks the authenticated peer transport.
//
// CometBFT encrypts and authenticates every peer connection with the node's
// ed25519 key (a secret connection), and its handshake carries the chain ID and
// protocol version. This package owns the node-side policy around that
// handshake: which peers a network may talk to, whether open discovery is
// allowed, and which protocol versions it accepts. Nothing here replaces the
// secret connection; it decides what a completed handshake is allowed to do.
package p2p

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	cmtcfg "github.com/cometbft/cometbft/config"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// MinVersion and MaxVersion bound the peer protocol version this node speaks.
const (
	MinVersion uint16 = 1
	MaxVersion uint16 = 1
)

var (
	ErrInvalid    = errors.New("p2p: invalid configuration")
	ErrNetwork    = errors.New("p2p: peer is on another network")
	ErrChain      = errors.New("p2p: peer is on another chain")
	ErrVersion    = errors.New("p2p: no mutually supported protocol version")
	ErrIdentity   = errors.New("p2p: invalid peer identity")
	ErrNotAllowed = errors.New("p2p: peer is not allowlisted")
)

// Config is a node's peer policy. The class comes from the network ID and
// decides whether open discovery is allowed.
type Config struct {
	Network           string   `json:"network"`
	Chain             string   `json:"chain"`
	Class             string   `json:"class"`
	ListenAddress     string   `json:"listen_address"`
	AdvertisedAddress string   `json:"advertised_address,omitempty"`
	PersistentPeers   []string `json:"persistent_peers,omitempty"`
	Seeds             []string `json:"seeds,omitempty"`
	Allowlist         []string `json:"allowlist,omitempty"`
	MaxInbound        int      `json:"max_inbound"`
	MaxOutbound       int      `json:"max_outbound"`
}

// Default returns a config with the network's chain ID and conservative peer
// limits. Public networks discover peers; consortium networks do not.
func Default(network string) (Config, error) {
	class, err := protocol.NetworkClass(network)
	if err != nil {
		return Config{}, err
	}
	chain, err := protocol.ChainID(network)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Network:       network,
		Chain:         chain,
		Class:         class,
		ListenAddress: "tcp://0.0.0.0:26656",
		MaxInbound:    40,
		MaxOutbound:   10,
	}, nil
}

// Validate checks that the policy is complete and internally consistent. A
// consortium network needs an allowlist; a public network must not have one,
// because an allowlist there would silently disable discovery.
func (c Config) Validate() error {
	class, err := protocol.NetworkClass(c.Network)
	if err != nil {
		return err
	}
	if c.Class != "" && c.Class != class {
		return fmt.Errorf("%w: class %q does not match network class %q", ErrInvalid, c.Class, class)
	}
	chain, err := protocol.ChainID(c.Network)
	if err != nil {
		return err
	}
	if c.Chain != chain {
		return fmt.Errorf("%w: chain %q does not match network %q (%s)", ErrInvalid, c.Chain, c.Network, chain)
	}
	if _, _, err := parseAddress("listen_address", c.ListenAddress); err != nil {
		return err
	}
	if c.AdvertisedAddress != "" {
		host, _, err := parseAddress("advertised_address", c.AdvertisedAddress)
		if err != nil {
			return err
		}
		if host == "0.0.0.0" || host == "::" || host == "" {
			return fmt.Errorf("%w: advertised_address needs a reachable host", ErrInvalid)
		}
	}
	if c.MaxInbound <= 0 || c.MaxOutbound <= 0 {
		return fmt.Errorf("%w: max_inbound and max_outbound must be positive", ErrInvalid)
	}
	if err := validatePeers("persistent_peers", c.PersistentPeers); err != nil {
		return err
	}
	if err := validatePeers("seeds", c.Seeds); err != nil {
		return err
	}
	if err := validateAllowlist(c.Allowlist); err != nil {
		return err
	}
	if class == protocol.ClassConsortium {
		if len(c.Allowlist) == 0 {
			return fmt.Errorf("%w: a consortium network needs an allowlist", ErrInvalid)
		}
		return nil
	}
	if len(c.Allowlist) != 0 {
		return fmt.Errorf("%w: a %s network must not use an allowlist", ErrInvalid, class)
	}
	return nil
}

// Apply writes the policy into a CometBFT P2P config. CometBFT always
// authenticates and encrypts with the node key, so this only sets addresses,
// discovery, peer limits, and which peers stay private.
func (c Config) Apply(cfg *cmtcfg.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if cfg.P2P == nil {
		cfg.P2P = cmtcfg.DefaultP2PConfig()
	}
	p2p := cfg.P2P
	p2p.ListenAddress = c.ListenAddress
	p2p.ExternalAddress = c.AdvertisedAddress
	if len(c.Seeds) > 0 {
		p2p.Seeds = strings.Join(c.Seeds, ",")
	}
	if len(c.PersistentPeers) > 0 {
		p2p.PersistentPeers = strings.Join(c.PersistentPeers, ",")
	}
	p2p.MaxNumInboundPeers = c.MaxInbound
	p2p.MaxNumOutboundPeers = c.MaxOutbound
	p2p.AddrBookStrict = true
	p2p.AllowDuplicateIP = false
	p2p.PexReactor = c.Class != protocol.ClassConsortium
	if c.Class == protocol.ClassConsortium {
		ids := make([]string, 0, len(c.Allowlist))
		for _, id := range c.Allowlist {
			ids = append(ids, strings.ToLower(id))
		}
		p2p.PrivatePeerIDs = strings.Join(ids, ",")
	} else {
		p2p.PrivatePeerIDs = ""
	}
	return nil
}

// Offer is what a peer claims during its handshake: its network, chain, node
// ID, and the protocol versions it speaks.
type Offer struct {
	Network  string   `json:"network"`
	Chain    string   `json:"chain"`
	NodeID   string   `json:"node_id"`
	Versions []uint16 `json:"versions"`
}

// Session is an accepted peer at a negotiated version.
type Session struct {
	NodeID  string `json:"node_id"`
	Version uint16 `json:"version"`
}

// Handshake decides whether a peer offer is acceptable. It never inspects
// application messages: an unacceptable offer is refused before anything else
// happens. The caller must still require CometBFT's secret connection, which is
// what proves the peer holds the key behind NodeID.
func (c Config) Handshake(offer Offer) (Session, error) {
	if err := c.Validate(); err != nil {
		return Session{}, err
	}
	nodeID, err := normalizeID(offer.NodeID)
	if err != nil {
		return Session{}, err
	}
	if offer.Network != c.Network {
		return Session{}, fmt.Errorf("%w: %q, expected %q", ErrNetwork, offer.Network, c.Network)
	}
	if offer.Chain != c.Chain {
		return Session{}, fmt.Errorf("%w: %q, expected %q", ErrChain, offer.Chain, c.Chain)
	}
	if c.Class == protocol.ClassConsortium && !contains(c.Allowlist, nodeID) {
		return Session{}, fmt.Errorf("%w: %s", ErrNotAllowed, nodeID)
	}
	version, ok := negotiate(offer.Versions)
	if !ok {
		return Session{}, fmt.Errorf("%w: peer supports %v, node supports %d..%d",
			ErrVersion, offer.Versions, MinVersion, MaxVersion)
	}
	return Session{NodeID: nodeID, Version: version}, nil
}

// NodeID derives a node ID from a public key the same way CometBFT does: the
// first 20 bytes of SHA-256 over the encoded key, in lowercase hex. It is the
// identity the secret connection authenticates and the value an allowlist holds.
func NodeID(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("%w: public key must be %d bytes", ErrIdentity, ed25519.PublicKeySize)
	}
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:20]), nil
}

func negotiate(versions []uint16) (uint16, bool) {
	best := uint16(0)
	found := false
	for _, version := range versions {
		if version < MinVersion || version > MaxVersion {
			continue
		}
		if !found || version > best {
			best = version
			found = true
		}
	}
	return best, found
}

func validatePeers(field string, peers []string) error {
	seen := make(map[string]bool, len(peers))
	for _, raw := range peers {
		id, _, err := parsePeer(raw)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalid, field, err)
		}
		if seen[id] {
			return fmt.Errorf("%w: %s repeats %s", ErrInvalid, field, id)
		}
		seen[id] = true
	}
	return nil
}

func validateAllowlist(ids []string) error {
	seen := make(map[string]bool, len(ids))
	for _, raw := range ids {
		id, err := normalizeID(raw)
		if err != nil {
			return err
		}
		if seen[id] {
			return fmt.Errorf("%w: allowlist repeats %s", ErrInvalid, id)
		}
		seen[id] = true
	}
	return nil
}

func parsePeer(raw string) (string, string, error) {
	idPart, address, ok := strings.Cut(strings.TrimSpace(raw), "@")
	if !ok {
		return "", "", fmt.Errorf("peer %q must be <node-id>@<host>:<port>", raw)
	}
	id, err := normalizeID(idPart)
	if err != nil {
		return "", "", err
	}
	host, port, err := parseAddress("peer address", "tcp://"+address)
	if err != nil {
		return "", "", err
	}
	return id, net.JoinHostPort(host, port), nil
}

func parseAddress(field, raw string) (string, string, error) {
	rest, ok := strings.CutPrefix(raw, "tcp://")
	if !ok {
		return "", "", fmt.Errorf("%w: %s must start with tcp://", ErrInvalid, field)
	}
	if strings.ContainsAny(rest, "/?#@ ") {
		return "", "", fmt.Errorf("%w: %s must be host:port", ErrInvalid, field)
	}
	host, port, err := net.SplitHostPort(rest)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s: %v", ErrInvalid, field, err)
	}
	if host == "" {
		return "", "", fmt.Errorf("%w: %s needs a host", ErrInvalid, field)
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return "", "", fmt.Errorf("%w: %s has an invalid port %q", ErrInvalid, field, port)
	}
	return host, port, nil
}

func normalizeID(raw string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if len(id) != 40 || !lowerHex(id) {
		return "", fmt.Errorf("%w: node id must be 40 lowercase hex characters", ErrIdentity)
	}
	return id, nil
}

func lowerHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
