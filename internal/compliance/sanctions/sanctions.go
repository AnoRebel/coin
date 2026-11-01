// Package sanctions implements provider-agnostic sanctions screening.
//
// Screening returns allow, hold, or deny. A potential match is always a hold for
// review, never an automatic freeze. A provider error is a deny (fail closed),
// because settlement must not continue when the list cannot be consulted. The
// decision carries the provider name and list version so a caller can record
// what was checked.
package sanctions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Outcome is the settlement decision.
type Outcome string

const (
	Allow Outcome = "allow"
	Hold  Outcome = "hold"
	Deny  Outcome = "deny"
)

// Result is the raw screening result.
type Result string

const (
	Clear          Result = "clear"
	PotentialMatch Result = "potential_match"
	Unavailable    Result = "unavailable"
)

var (
	ErrInvalid     = errors.New("sanctions: invalid list")
	ErrUnavailable = errors.New("sanctions: provider unavailable")
)

// Subject is the party being screened.
type Subject struct {
	Address       string   `json:"address"`
	Name          string   `json:"name"`
	Jurisdictions []string `json:"jurisdictions,omitempty"`
}

// Match is a provider answer. ListVersion identifies the data consulted.
type Match struct {
	Provider    string   `json:"provider"`
	ListVersion string   `json:"list_version"`
	Result      Result   `json:"result"`
	Matches     []string `json:"matches,omitempty"`
	Score       int      `json:"score,omitempty"`
}

// Decision is the recorded outcome of one screening.
type Decision struct {
	Outcome        Outcome  `json:"outcome"`
	Provider       string   `json:"provider"`
	ListVersion    string   `json:"list_version"`
	Result         Result   `json:"result"`
	Matches        []string `json:"matches,omitempty"`
	Score          int      `json:"score,omitempty"`
	Reason         string   `json:"reason"`
	RequiresReview bool     `json:"requires_review"`
}

// Provider screens a subject against one list or service.
type Provider interface {
	Name() string
	Screen(ctx context.Context, subject Subject) (Match, error)
}

// Screener applies fail-closed policy to a provider.
type Screener struct {
	provider Provider
}

// New wraps a provider. A nil provider is rejected.
func New(provider Provider) (*Screener, error) {
	if provider == nil {
		return nil, fmt.Errorf("%w: provider is required", ErrInvalid)
	}
	return &Screener{provider: provider}, nil
}

// Screen never returns an error: failures become a deny decision so callers
// cannot accidentally settle on a failed check.
func (s *Screener) Screen(ctx context.Context, subject Subject) Decision {
	match, err := s.provider.Screen(ctx, subject)
	d := Decision{
		Provider:    s.provider.Name(),
		ListVersion: match.ListVersion,
		Matches:     append([]string(nil), match.Matches...),
		Score:       match.Score,
	}
	if err != nil {
		d.Outcome, d.Result = Deny, Unavailable
		d.Reason = "screening unavailable: " + err.Error()
		return d
	}
	switch match.Result {
	case Clear:
		d.Outcome, d.Result = Allow, Clear
		d.Reason = "no match"
	case PotentialMatch:
		d.Outcome, d.Result = Hold, PotentialMatch
		d.Reason = "potential match requires review"
		d.RequiresReview = true
	default:
		d.Outcome, d.Result = Deny, Unavailable
		d.Reason = "provider returned an unknown result"
	}
	return d
}

// Entry is one sanctioned party. At least one of Address or Name is required.
type Entry struct {
	ID      string   `json:"id"`
	Address string   `json:"address,omitempty"`
	Name    string   `json:"name,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

// List is a versioned sanctions list.
type List struct {
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	UpdatedAt int64   `json:"updated_at"`
	Entries   []Entry `json:"entries"`
}

// ListProvider screens against a local, versioned list.
type ListProvider struct {
	list List
	byID map[string]Entry
}

// NewListProvider validates and indexes a list.
func NewListProvider(list List) (*ListProvider, error) {
	if strings.TrimSpace(list.Name) == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if strings.TrimSpace(list.Version) == "" {
		return nil, fmt.Errorf("%w: version is required", ErrInvalid)
	}
	if len(list.Entries) == 0 {
		return nil, fmt.Errorf("%w: at least one entry is required", ErrInvalid)
	}
	p := &ListProvider{list: list, byID: make(map[string]Entry, len(list.Entries))}
	for i, e := range list.Entries {
		if strings.TrimSpace(e.Address) == "" && strings.TrimSpace(e.Name) == "" {
			return nil, fmt.Errorf("%w: entries[%d] needs an address or a name", ErrInvalid, i)
		}
		if e.ID == "" {
			e.ID = entryID(e)
		}
		if _, dup := p.byID[e.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate entry id %q", ErrInvalid, e.ID)
		}
		p.byID[e.ID] = e
		p.list.Entries[i] = e
	}
	return p, nil
}

// Name identifies the provider and list.
func (p *ListProvider) Name() string { return "list:" + p.list.Name }

// Version returns the list version.
func (p *ListProvider) Version() string { return p.list.Version }

// Screen matches an address exactly (case-insensitive) or a name/alias after
// whitespace and case normalization.
func (p *ListProvider) Screen(ctx context.Context, subject Subject) (Match, error) {
	if err := ctx.Err(); err != nil {
		return Match{}, err
	}
	m := Match{Provider: p.Name(), ListVersion: p.list.Version, Result: Clear}
	address := normalizeAddress(subject.Address)
	name := normalizeName(subject.Name)
	seen := map[string]bool{}
	for _, e := range p.list.Entries {
		hit, score := false, 0
		if address != "" && normalizeAddress(e.Address) == address {
			hit, score = true, 100
		}
		if !hit && name != "" {
			for _, candidate := range append([]string{e.Name}, e.Aliases...) {
				if candidate != "" && normalizeName(candidate) == name {
					hit, score = true, 80
					break
				}
			}
		}
		if hit && !seen[e.ID] {
			seen[e.ID] = true
			m.Matches = append(m.Matches, e.ID)
			if score > m.Score {
				m.Score = score
			}
		}
	}
	if len(m.Matches) > 0 {
		sort.Strings(m.Matches)
		m.Result = PotentialMatch
	}
	return m, nil
}

// UnavailableProvider is a provider that cannot answer; useful for tests and as
// an explicit disabled state.
type UnavailableProvider struct {
	ProviderName string
	Reason       string
}

// Name implements Provider.
func (u UnavailableProvider) Name() string {
	if u.ProviderName == "" {
		return "unavailable"
	}
	return u.ProviderName
}

// Screen implements Provider.
func (u UnavailableProvider) Screen(context.Context, Subject) (Match, error) {
	reason := u.Reason
	if reason == "" {
		reason = "no provider configured"
	}
	return Match{}, fmt.Errorf("%w: %s", ErrUnavailable, reason)
}

func normalizeAddress(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func normalizeName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func entryID(e Entry) string {
	h := sha256.New()
	_, _ = h.Write([]byte("arne-sanctions-entry-v1"))
	for _, part := range []string{normalizeAddress(e.Address), normalizeName(e.Name)} {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}
