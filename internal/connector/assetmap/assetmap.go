// Package assetmap maps external rail references to canonical ARNE asset IDs.
//
// A mapping set is versioned: each new set must be exactly the next version, so
// a caller resolves against a numbered view it can audit later. Within a set a
// normalized external reference may appear only once; two mappings for the same
// reference are ambiguous and are refused rather than resolved by order.
package assetmap

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalid   = errors.New("assetmap: invalid")
	ErrDuplicate = errors.New("assetmap: duplicate external reference")
	ErrAmbiguous = errors.New("assetmap: ambiguous canonical asset")
	ErrUnknown   = errors.New("assetmap: unknown external reference")
	ErrVersion   = errors.New("assetmap: unexpected version")
)

// Rail is the external system a reference belongs to.
type Rail string

const (
	EVM  Rail = "evm"
	Fiat Rail = "fiat"
)

// External is a rail-specific asset reference.
type External struct {
	Rail    Rail   `json:"rail"`
	ChainID uint64 `json:"chain_id,omitempty"`
	Address string `json:"address,omitempty"`
	Scheme  string `json:"scheme,omitempty"`
	Code    string `json:"code,omitempty"`
}

// Normalize returns the canonical form of the reference: EVM addresses are
// lowercased and fiat codes uppercased, so casing cannot create a duplicate.
func (e External) Normalize() External {
	out := e
	if out.Rail == EVM {
		out.Address = strings.ToLower(strings.TrimSpace(out.Address))
	}
	out.Code = strings.ToUpper(strings.TrimSpace(out.Code))
	return out
}

func (e External) validate() error {
	switch e.Rail {
	case EVM:
		if e.ChainID == 0 {
			return fmt.Errorf("%w: evm chain_id is required", ErrInvalid)
		}
		if len(e.Address) != 42 || !strings.HasPrefix(e.Address, "0x") || !lowerHex(e.Address[2:]) {
			return fmt.Errorf("%w: evm address must be 0x followed by 40 hex characters", ErrInvalid)
		}
		if e.Scheme != "" || e.Code != "" {
			return fmt.Errorf("%w: evm mapping has no scheme or code", ErrInvalid)
		}
	case Fiat:
		if e.Scheme != "iso4217" {
			return fmt.Errorf("%w: fiat scheme must be iso4217", ErrInvalid)
		}
		if len(e.Code) != 3 || !upperAlpha(e.Code) {
			return fmt.Errorf("%w: fiat code must be an ISO 4217 code", ErrInvalid)
		}
		if e.ChainID != 0 || e.Address != "" {
			return fmt.Errorf("%w: fiat mapping has no chain or address", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: rail %q", ErrInvalid, e.Rail)
	}
	return nil
}

// Key is the normalized lookup key.
func (e External) Key() string {
	e = e.Normalize()
	switch e.Rail {
	case EVM:
		return fmt.Sprintf("evm:%d:%s", e.ChainID, e.Address)
	case Fiat:
		return fmt.Sprintf("fiat:%s:%s", e.Scheme, e.Code)
	default:
		return string(e.Rail)
	}
}

// Mapping binds an external reference to a canonical asset. MergePolicy is
// required when the canonical asset already appears earlier in the set: two
// rails may share one asset only with a recorded reason.
type Mapping struct {
	External    External `json:"external"`
	Canonical   string   `json:"canonical"`
	Decimals    uint8    `json:"decimals"`
	Since       int64    `json:"since"`
	MergePolicy string   `json:"merge_policy,omitempty"`
}

// Set is one version of the mapping table.
type Set struct {
	Version  uint32    `json:"version"`
	Mappings []Mapping `json:"mappings"`
	index    map[string]int
}

func (s *Set) build() error {
	if s.Version == 0 {
		return fmt.Errorf("%w: version starts at 1", ErrInvalid)
	}
	if len(s.Mappings) == 0 {
		return fmt.Errorf("%w: a set needs a mapping", ErrInvalid)
	}
	s.index = make(map[string]int, len(s.Mappings))
	canonicalSeen := make(map[string]bool, len(s.Mappings))
	for i, m := range s.Mappings {
		if strings.TrimSpace(m.Canonical) == "" || utf8.RuneCountInString(m.Canonical) > 128 {
			return fmt.Errorf("%w: canonical asset is required", ErrInvalid)
		}
		if m.Decimals > 36 {
			return fmt.Errorf("%w: decimals must be at most 36", ErrInvalid)
		}
		normalized := m.External.Normalize()
		if err := normalized.validate(); err != nil {
			return fmt.Errorf("mapping %d: %w", i, err)
		}
		key := normalized.Key()
		if _, seen := s.index[key]; seen {
			return fmt.Errorf("%w: %s", ErrDuplicate, key)
		}
		if canonicalSeen[m.Canonical] && strings.TrimSpace(m.MergePolicy) == "" {
			return fmt.Errorf("%w: mapping %d needs a merge policy for %s", ErrAmbiguous, i, m.Canonical)
		}
		if utf8.RuneCountInString(m.MergePolicy) > 128 {
			return fmt.Errorf("%w: merge policy must be at most 128 characters", ErrInvalid)
		}
		canonicalSeen[m.Canonical] = true
		s.index[key] = i
		s.Mappings[i].External = normalized
	}
	return nil
}

// Registry resolves against the newest applied set and keeps every prior set.
type Registry struct {
	sets []*Set
}

// New starts a registry with version 1.
func New(version uint32, mappings []Mapping) (*Registry, error) {
	set := &Set{Version: version, Mappings: mappings}
	if err := set.build(); err != nil {
		return nil, err
	}
	return &Registry{sets: []*Set{set}}, nil
}

// Apply installs the next version. Older versions stay resolvable.
func (r *Registry) Apply(version uint32, mappings []Mapping) error {
	current := r.Current()
	if version != current+1 {
		return fmt.Errorf("%w: %d after %d", ErrVersion, version, current)
	}
	set := &Set{Version: version, Mappings: mappings}
	if err := set.build(); err != nil {
		return err
	}
	r.sets = append(r.sets, set)
	return nil
}

// Current is the newest version.
func (r *Registry) Current() uint32 { return r.sets[len(r.sets)-1].Version }

// Versions lists every applied version, oldest first.
func (r *Registry) Versions() []uint32 {
	out := make([]uint32, 0, len(r.sets))
	for _, set := range r.sets {
		out = append(out, set.Version)
	}
	return out
}

// Resolve maps an external reference using the newest version.
func (r *Registry) Resolve(external External) (Mapping, error) {
	return r.ResolveVersion(r.Current(), external)
}

// ResolveVersion maps a reference against a specific historical version.
func (r *Registry) ResolveVersion(version uint32, external External) (Mapping, error) {
	set, err := r.set(version)
	if err != nil {
		return Mapping{}, err
	}
	key := external.Normalize().Key()
	index, ok := set.index[key]
	if !ok {
		return Mapping{}, fmt.Errorf("%w: %s", ErrUnknown, key)
	}
	return set.Mappings[index], nil
}

// Reverse lists every external reference that maps to a canonical asset. It is
// a list, not a single value, because several rails can share one asset.
func (r *Registry) Reverse(canonical string) []Mapping {
	set := r.sets[len(r.sets)-1]
	out := make([]Mapping, 0, 1)
	for _, mapping := range set.Mappings {
		if mapping.Canonical == canonical {
			out = append(out, mapping)
		}
	}
	return out
}

func (r *Registry) set(version uint32) (*Set, error) {
	for _, set := range r.sets {
		if set.Version == version {
			return set, nil
		}
	}
	return nil, fmt.Errorf("%w: %d is not applied", ErrVersion, version)
}

func lowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func upperAlpha(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 'A' || value[i] > 'Z' {
			return false
		}
	}
	return true
}
