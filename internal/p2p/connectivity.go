package p2p

import (
	"fmt"
	"net"
	"strings"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Connectivity is what the node can tell an operator about its reachability.
// It is a report, not a test: the node cannot prove an inbound path exists
// without an external dialer, so an advertised host that is not obviously
// private is treated as reachable.
type Connectivity struct {
	OutboundOnly bool   `json:"outbound_only"`
	Advertised   bool   `json:"advertised"`
	Reachable    bool   `json:"reachable"`
	Mismatch     bool   `json:"mismatch"`
	Reason       string `json:"reason,omitempty"`
	RelayPeers   int    `json:"relay_peers"`
	AnchorPeers  int    `json:"anchor_peers"`
}

// validateConnectivity checks relay and anchor peers and refuses an
// outbound-only node that still advertises an address.
func (c Config) validateConnectivity() error {
	if err := validatePeers("relay_peers", c.RelayPeers); err != nil {
		return err
	}
	if err := validatePeers("anchor_peers", c.AnchorPeers); err != nil {
		return err
	}
	if c.OutboundOnly && c.AdvertisedAddress != "" {
		return fmt.Errorf("%w: outbound-only nodes must not advertise an address", ErrInvalid)
	}
	if c.OutboundOnly && len(c.Seeds)+len(c.PersistentPeers)+len(c.RelayPeers) == 0 {
		return fmt.Errorf("%w: outbound-only nodes need a seed, persistent, or relay peer", ErrInvalid)
	}
	return nil
}

// Report describes how the node expects to be reached. A public node with no
// advertised address, or with a private advertised host, is reported as
// outbound-only with a mismatch; callers log it and keep running, because
// outbound connectivity still reaches the network.
func (c Config) Report() Connectivity {
	report := Connectivity{
		OutboundOnly: c.OutboundOnly,
		RelayPeers:   len(c.RelayPeers),
		AnchorPeers:  len(c.AnchorPeers),
	}
	if c.OutboundOnly {
		report.Reason = "configured outbound-only"
		return report
	}
	if c.AdvertisedAddress == "" {
		report.OutboundOnly = true
		report.Reason = "no advertised address; peers cannot dial this node"
		return report
	}
	report.Advertised = true
	host, _, err := parseAddress("advertised_address", c.AdvertisedAddress)
	if err != nil {
		report.Mismatch = true
		report.Reason = err.Error()
		return report
	}
	class, _ := protocol.NetworkClass(c.Network)
	if class == protocol.ClassMainnet || class == protocol.ClassTestnet {
		if !publicHost(host) {
			report.Mismatch = true
			report.Reason = fmt.Sprintf("advertised host %q is not publicly reachable", host)
			return report
		}
	}
	report.Reachable = true
	return report
}

// mergePeers appends peers that are not already present by node ID, so a relay
// or anchor can also be listed as a persistent peer without a duplicate.
func mergePeers(existing, extra []string) []string {
	seen := make(map[string]bool, len(existing)+len(extra))
	merged := make([]string, 0, len(existing)+len(extra))
	for _, raw := range append(append([]string{}, existing...), extra...) {
		id, address, err := parsePeer(raw)
		if err != nil {
			merged = append(merged, raw)
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		merged = append(merged, id+"@"+address)
	}
	return merged
}

func peerIDs(peers []string) []string {
	ids := make([]string, 0, len(peers))
	for _, raw := range peers {
		id, _, err := parsePeer(raw)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func publicHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A hostname is assumed reachable; there is no resolver here.
		return true
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast())
}
