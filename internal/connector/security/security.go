// Package security protects connector transport: endpoint allowlists,
// mutually authenticated client credentials, rotation with an overlap window,
// and log-safe redaction.
//
// The TLS handshake itself belongs to the transport. This package makes the
// authorization decision after it: a peer with no client certificate is
// rejected, and a peer whose certificate fingerprint is unknown, not yet valid,
// expired, or retired is rejected with a distinct error.
package security

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var (
	ErrEndpoint      = errors.New("connector security: endpoint rejected")
	ErrHost          = errors.New("connector security: host is not allowlisted")
	ErrInvalid       = errors.New("connector security: invalid")
	ErrNoCertificate = errors.New("connector security: client certificate required")
	ErrUnknown       = errors.New("connector security: unknown credential")
	ErrNotYetValid   = errors.New("connector security: credential is not yet valid")
	ErrExpired       = errors.New("connector security: credential has expired")
	ErrRevoked       = errors.New("connector security: credential is retired")
	ErrDuplicate     = errors.New("connector security: duplicate credential")
	ErrState         = errors.New("connector security: wrong credential state")
)

// Allowlist is a set of exact hosts and wildcard suffixes. A wildcard
// `*.example.com` allows `a.example.com` but not `example.com` itself.
type Allowlist struct {
	exact    map[string]bool
	wildcard []string
}

// NewAllowlist validates hosts. Hosts are lowercase, carry no scheme, port, or
// path, and duplicates are refused rather than silently collapsed.
func NewAllowlist(hosts []string) (*Allowlist, error) {
	list := &Allowlist{exact: make(map[string]bool, len(hosts))}
	for _, raw := range hosts {
		host := strings.ToLower(strings.TrimSpace(raw))
		if host == "" {
			return nil, fmt.Errorf("%w: empty host", ErrInvalid)
		}
		if strings.ContainsAny(host, "/:@ ") {
			return nil, fmt.Errorf("%w: host %q must be a bare hostname", ErrInvalid, raw)
		}
		if strings.HasPrefix(host, "*.") {
			suffix := strings.TrimPrefix(host, "*.")
			if suffix == "" || strings.Contains(suffix, "*") {
				return nil, fmt.Errorf("%w: wildcard %q", ErrInvalid, raw)
			}
			for _, existing := range list.wildcard {
				if existing == suffix {
					return nil, fmt.Errorf("%w: duplicate host %q", ErrDuplicate, raw)
				}
			}
			list.wildcard = append(list.wildcard, suffix)
			continue
		}
		if strings.Contains(host, "*") {
			return nil, fmt.Errorf("%w: wildcard %q must be the whole label", ErrInvalid, raw)
		}
		if list.exact[host] {
			return nil, fmt.Errorf("%w: duplicate host %q", ErrDuplicate, raw)
		}
		list.exact[host] = true
	}
	if len(list.exact) == 0 && len(list.wildcard) == 0 {
		return nil, fmt.Errorf("%w: allowlist is empty", ErrInvalid)
	}
	return list, nil
}

// Allows reports whether a host is permitted. The host is lowercased and a
// single trailing dot is ignored.
func (a *Allowlist) Allows(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if a.exact[host] {
		return true
	}
	for _, suffix := range a.wildcard {
		if strings.HasSuffix(host, "."+suffix) && len(host) > len(suffix)+1 {
			return true
		}
	}
	return false
}

// Endpoint is a validated connector destination.
type Endpoint struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Port   string `json:"port,omitempty"`
	Path   string `json:"path,omitempty"`
}

// ParseEndpoint requires an absolute https URL with no embedded credentials and
// a host that the allowlist permits. Plaintext and userinfo are refused because
// either would leak or weaken the connector's authentication.
func ParseEndpoint(raw string, allow *Allowlist) (Endpoint, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return Endpoint{}, fmt.Errorf("%w: %v", ErrEndpoint, err)
	}
	if parsed.Scheme != "https" {
		return Endpoint{}, fmt.Errorf("%w: scheme must be https", ErrEndpoint)
	}
	if parsed.User != nil {
		return Endpoint{}, fmt.Errorf("%w: credentials must not appear in the URL", ErrEndpoint)
	}
	if parsed.Fragment != "" {
		return Endpoint{}, fmt.Errorf("%w: fragment is not allowed", ErrEndpoint)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return Endpoint{}, fmt.Errorf("%w: host is required", ErrEndpoint)
	}
	if allow != nil && !allow.Allows(host) {
		return Endpoint{}, fmt.Errorf("%w: %s", ErrHost, host)
	}
	port := parsed.Port()
	if port != "" {
		if err := validPort(port); err != nil {
			return Endpoint{}, err
		}
	}
	return Endpoint{Scheme: "https", Host: host, Port: port, Path: parsed.EscapedPath()}, nil
}

// Credential is a client certificate identity. Fingerprint is the SHA-256 of
// the DER certificate as 64 lowercase hex characters.
type Credential struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	NotBefore   int64  `json:"not_before"`
	NotAfter    int64  `json:"not_after,omitempty"`
}

// Registry authorizes client certificates and supports rotation.
type Registry struct {
	byID     map[string]*credential
	byFP     map[string]*credential
	retireAt map[string]int64
}

type credential struct {
	value Credential
}

// NewRegistry starts an empty credential registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:     map[string]*credential{},
		byFP:     map[string]*credential{},
		retireAt: map[string]int64{},
	}
}

// Register adds a credential. IDs and fingerprints are unique, and the
// fingerprint must be a SHA-256 hex digest.
func (r *Registry) Register(c Credential) error {
	if err := c.validate(); err != nil {
		return err
	}
	if _, ok := r.byID[c.ID]; ok {
		return fmt.Errorf("%w: id %q", ErrDuplicate, c.ID)
	}
	if _, ok := r.byFP[c.Fingerprint]; ok {
		return fmt.Errorf("%w: fingerprint", ErrDuplicate)
	}
	entry := &credential{value: c}
	r.byID[c.ID] = entry
	r.byFP[c.Fingerprint] = entry
	return nil
}

// Rotate registers `next` and keeps `oldID` valid until now+overlap. The new
// credential is valid from now. A negative overlap, an unknown or already
// retired old credential, or a reused id/fingerprint is refused.
func (r *Registry) Rotate(oldID string, next Credential, now, overlap int64) error {
	if overlap < 0 {
		return fmt.Errorf("%w: overlap must not be negative", ErrInvalid)
	}
	if now < 0 {
		return fmt.Errorf("%w: now must not be negative", ErrInvalid)
	}
	old, ok := r.byID[oldID]
	if !ok {
		return fmt.Errorf("%w: id %q", ErrUnknown, oldID)
	}
	if _, retired := r.retireAt[oldID]; retired {
		return fmt.Errorf("%w: %q is already retired", ErrState, oldID)
	}
	if next.ID == old.value.ID {
		return fmt.Errorf("%w: the new credential needs a new id", ErrInvalid)
	}
	if next.Fingerprint == old.value.Fingerprint {
		return fmt.Errorf("%w: the new credential needs a new fingerprint", ErrInvalid)
	}
	if next.NotBefore == 0 {
		next.NotBefore = now
	}
	if next.NotAfter != 0 && next.NotAfter <= next.NotBefore {
		return fmt.Errorf("%w: not_after must be after not_before", ErrInvalid)
	}
	if err := r.Register(next); err != nil {
		return err
	}
	r.retireAt[oldID] = now + overlap
	return nil
}

// Revoke retires a credential at `at` without a replacement. This is the
// emergency path when a key may be compromised; it cannot be undone.
func (r *Registry) Revoke(id string, at int64) error {
	if at < 0 {
		return fmt.Errorf("%w: at must not be negative", ErrInvalid)
	}
	if _, ok := r.byID[id]; !ok {
		return fmt.Errorf("%w: id %q", ErrUnknown, id)
	}
	if _, retired := r.retireAt[id]; retired {
		return fmt.Errorf("%w: %q is already retired", ErrState, id)
	}
	r.retireAt[id] = at
	return nil
}

// Authorize checks a presented client certificate. An empty fingerprint means
// the peer did not present one, which is always refused.
func (r *Registry) Authorize(fingerprint string, now int64) (Credential, error) {
	if fingerprint == "" {
		return Credential{}, ErrNoCertificate
	}
	entry, ok := r.byFP[fingerprint]
	if !ok {
		return Credential{}, fmt.Errorf("%w: fingerprint", ErrUnknown)
	}
	c := entry.value
	if now < c.NotBefore {
		return Credential{}, fmt.Errorf("%w: %s valid from %d", ErrNotYetValid, c.ID, c.NotBefore)
	}
	if c.NotAfter != 0 && now >= c.NotAfter {
		return Credential{}, fmt.Errorf("%w: %s expired at %d", ErrExpired, c.ID, c.NotAfter)
	}
	if at, retired := r.retireAt[c.ID]; retired && now >= at {
		return Credential{}, fmt.Errorf("%w: %s retired at %d", ErrRevoked, c.ID, at)
	}
	return c, nil
}

// RetiresAt reports when a credential stops working, with false when it never
// will. It is used by operators to plan rotation.
func (r *Registry) RetiresAt(id string) (int64, bool) {
	at, ok := r.retireAt[id]
	return at, ok
}

func (c Credential) validate() error {
	if !identifier(c.ID) {
		return fmt.Errorf("%w: id must be 1..128 characters of [A-Za-z0-9_.:-]", ErrInvalid)
	}
	if len(c.Fingerprint) != 64 || !lowerHex(c.Fingerprint) {
		return fmt.Errorf("%w: fingerprint must be 64 lowercase hex characters", ErrInvalid)
	}
	if c.NotBefore < 0 || c.NotAfter < 0 {
		return fmt.Errorf("%w: validity times must not be negative", ErrInvalid)
	}
	if c.NotAfter != 0 && c.NotAfter <= c.NotBefore {
		return fmt.Errorf("%w: not_after must be after not_before", ErrInvalid)
	}
	return nil
}

func identifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '.', r == ':', r == '-':
		default:
			return false
		}
	}
	return true
}

func lowerHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validPort(port string) error {
	if len(port) > 5 {
		return fmt.Errorf("%w: invalid port %q", ErrEndpoint, port)
	}
	value := 0
	for _, r := range port {
		if r < '0' || r > '9' {
			return fmt.Errorf("%w: invalid port %q", ErrEndpoint, port)
		}
		value = value*10 + int(r-'0')
	}
	if value < 1 || value > 65535 {
		return fmt.Errorf("%w: port %q out of range", ErrEndpoint, port)
	}
	return nil
}
