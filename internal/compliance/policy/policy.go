// Package policy implements the deterministic, versioned jurisdiction policy
// engine. A policy set maps participants, assets, and transfer types to an
// allow, deny, or hold decision. Policies activate at a block height, and a
// decision always reports the version and rule that produced it.
package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Decision is the outcome of a policy evaluation.
type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
	Hold  Decision = "hold"
)

// ErrInvalidPolicy wraps every schema or semantic error.
var ErrInvalidPolicy = errors.New("policy: invalid policy set")

// Match selects the transfers a rule applies to. An empty list matches
// anything. Amounts are in the asset's base units; MaxAmount 0 is unbounded.
type Match struct {
	Assets                   []string `json:"assets"`
	OriginatorJurisdictions  []string `json:"originator_jurisdictions"`
	BeneficiaryJurisdictions []string `json:"beneficiary_jurisdictions"`
	TransferTypes            []string `json:"transfer_types"`
	MinAmount                uint64   `json:"min_amount"`
	MaxAmount                uint64   `json:"max_amount"`
}

// Rule is one named policy rule. Higher priority wins; ties break by rule ID.
type Rule struct {
	ID           string   `json:"id"`
	Priority     uint64   `json:"priority"`
	Decision     Decision `json:"decision"`
	Reason       string   `json:"reason"`
	Requirements []string `json:"requirements"`
	Match        Match    `json:"match"`
}

// Default is the decision when no rule matches.
type Default struct {
	Decision     Decision `json:"decision"`
	Reason       string   `json:"reason"`
	Requirements []string `json:"requirements"`
}

// Version is one policy set effective from ActivationHeight until the next
// version activates.
type Version struct {
	Version          uint64  `json:"version"`
	ActivationHeight uint64  `json:"activation_height"`
	Default          Default `json:"default"`
	Rules            []Rule  `json:"rules"`
}

// PolicySet is the complete, ordered policy history for a network.
type PolicySet struct {
	Network  string    `json:"network"`
	Versions []Version `json:"versions"`
}

// Context describes the transfer being evaluated.
type Context struct {
	Asset                   string `json:"asset"`
	Amount                  uint64 `json:"amount"`
	OriginatorJurisdiction  string `json:"originator_jurisdiction"`
	BeneficiaryJurisdiction string `json:"beneficiary_jurisdiction"`
	TransferType            string `json:"transfer_type"`
}

// Outcome is the machine-readable result.
type Outcome struct {
	Decision         Decision `json:"decision"`
	RuleID           string   `json:"rule_id"`
	PolicyVersion    uint64   `json:"policy_version"`
	ActivationHeight uint64   `json:"activation_height"`
	Reasons          []string `json:"reasons"`
	Requirements     []string `json:"requirements"`
}

// Parse decodes and validates a policy set. Unknown fields are rejected so a
// typo cannot silently change a decision.
func Parse(data []byte) (*PolicySet, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var ps PolicySet
	if err := dec.Decode(&ps); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: trailing content", ErrInvalidPolicy)
	}
	if err := ps.Validate(); err != nil {
		return nil, err
	}
	return &ps, nil
}

// Validate checks the schema and semantic constraints.
func (p *PolicySet) Validate() error {
	if strings.TrimSpace(p.Network) == "" {
		return fmt.Errorf("%w: network is required", ErrInvalidPolicy)
	}
	if len(p.Versions) == 0 {
		return fmt.Errorf("%w: at least one version is required", ErrInvalidPolicy)
	}
	versions := make(map[uint64]bool, len(p.Versions))
	heights := make(map[uint64]bool, len(p.Versions))
	for i, v := range p.Versions {
		if v.Version == 0 {
			return fmt.Errorf("%w: versions[%d]: version must be positive", ErrInvalidPolicy, i)
		}
		if versions[v.Version] {
			return fmt.Errorf("%w: duplicate version %d", ErrInvalidPolicy, v.Version)
		}
		versions[v.Version] = true
		if heights[v.ActivationHeight] {
			return fmt.Errorf("%w: duplicate activation height %d", ErrInvalidPolicy, v.ActivationHeight)
		}
		heights[v.ActivationHeight] = true
		if err := validateDecision(v.Default.Decision); err != nil {
			return fmt.Errorf("%w: version %d default: %v", ErrInvalidPolicy, v.Version, err)
		}
		ids := make(map[string]bool, len(v.Rules))
		for j, r := range v.Rules {
			if strings.TrimSpace(r.ID) == "" {
				return fmt.Errorf("%w: version %d rules[%d]: id is required", ErrInvalidPolicy, v.Version, j)
			}
			if ids[r.ID] {
				return fmt.Errorf("%w: version %d: duplicate rule id %q", ErrInvalidPolicy, v.Version, r.ID)
			}
			ids[r.ID] = true
			if err := validateDecision(r.Decision); err != nil {
				return fmt.Errorf("%w: version %d rule %q: %v", ErrInvalidPolicy, v.Version, r.ID, err)
			}
			if r.Match.MaxAmount != 0 && r.Match.MinAmount > r.Match.MaxAmount {
				return fmt.Errorf("%w: version %d rule %q: min_amount exceeds max_amount", ErrInvalidPolicy, v.Version, r.ID)
			}
		}
	}
	return nil
}

func validateDecision(d Decision) error {
	switch d {
	case Allow, Deny, Hold:
		return nil
	default:
		return fmt.Errorf("unknown decision %q", d)
	}
}

// At returns the version active at height.
func (p *PolicySet) At(height uint64) (Version, bool) {
	var best *Version
	for i := range p.Versions {
		v := &p.Versions[i]
		if v.ActivationHeight > height {
			continue
		}
		if best == nil || v.ActivationHeight > best.ActivationHeight ||
			(v.ActivationHeight == best.ActivationHeight && v.Version > best.Version) {
			best = v
		}
	}
	if best == nil {
		return Version{}, false
	}
	return *best, true
}

// Evaluate applies the version active at height. Before the first activation
// the result is deny with rule_id "no-active-policy"; the engine never falls
// back to an implicit allow.
func (p *PolicySet) Evaluate(height uint64, ctx Context) Outcome {
	version, ok := p.At(height)
	if !ok {
		return Outcome{
			Decision: Deny, RuleID: "no-active-policy",
			Reasons: []string{"no policy version is active at this height"}, Requirements: []string{},
		}
	}
	rules := append([]Rule(nil), version.Rules...)
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority > rules[j].Priority
		}
		return rules[i].ID < rules[j].ID
	})
	for _, r := range rules {
		if !r.Match.matches(ctx) {
			continue
		}
		return Outcome{
			Decision: r.Decision, RuleID: r.ID,
			PolicyVersion: version.Version, ActivationHeight: version.ActivationHeight,
			Reasons: nonNil(r.Reason), Requirements: nonNilList(r.Requirements),
		}
	}
	return Outcome{
		Decision: version.Default.Decision, RuleID: "default",
		PolicyVersion: version.Version, ActivationHeight: version.ActivationHeight,
		Reasons: nonNil(version.Default.Reason), Requirements: nonNilList(version.Default.Requirements),
	}
}

// History returns the versions ordered by activation height.
func (p *PolicySet) History() []Version {
	out := append([]Version(nil), p.Versions...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].ActivationHeight < out[j].ActivationHeight
	})
	return out
}

func (m Match) matches(ctx Context) bool {
	return inList(m.Assets, ctx.Asset) &&
		inList(m.OriginatorJurisdictions, ctx.OriginatorJurisdiction) &&
		inList(m.BeneficiaryJurisdictions, ctx.BeneficiaryJurisdiction) &&
		inList(m.TransferTypes, ctx.TransferType) &&
		ctx.Amount >= m.MinAmount &&
		(m.MaxAmount == 0 || ctx.Amount <= m.MaxAmount)
}

func inList(list []string, value string) bool {
	if len(list) == 0 {
		return true
	}
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func nonNil(s string) []string {
	if s == "" {
		return []string{}
	}
	return []string{s}
}

func nonNilList(list []string) []string {
	if len(list) == 0 {
		return []string{}
	}
	return append([]string(nil), list...)
}
