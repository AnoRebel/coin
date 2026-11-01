// Package suites tracks which signature suites are active, deprecated, or
// disabled. A deprecated suite still verifies historical signatures but cannot
// register a new account; a disabled suite is refused outright.
package suites

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Status is a suite lifecycle state.
type Status string

const (
	Active     Status = "active"
	Deprecated Status = "deprecated"
	Disabled   Status = "disabled"
)

var (
	ErrInvalid    = errors.New("suites: invalid suite or status")
	ErrUnknown    = errors.New("suites: unknown suite")
	ErrDeprecated = errors.New("suites: suite is deprecated for new accounts")
	ErrDisabled   = errors.New("suites: suite is disabled")
)

// Verifier checks a signature. It must be safe to call concurrently.
type Verifier func(publicKey, message, signature []byte) bool

// Suite is one registered signature algorithm.
type Suite struct {
	ID        string   `json:"id"`
	Algorithm string   `json:"algorithm"`
	Status    Status   `json:"status"`
	Verify    Verifier `json:"-"`
}

// Registry holds suites by ID.
type Registry struct {
	suites map[string]Suite
}

// New validates and registers suites. IDs must be unique and non-empty, and a
// non-disabled suite needs a verifier.
func New(entries []Suite) (*Registry, error) {
	r := &Registry{suites: make(map[string]Suite, len(entries))}
	for i, s := range entries {
		s.ID = strings.TrimSpace(s.ID)
		s.Algorithm = strings.TrimSpace(s.Algorithm)
		if s.ID == "" || s.Algorithm == "" {
			return nil, fmt.Errorf("%w: entries[%d] needs an id and algorithm", ErrInvalid, i)
		}
		switch s.Status {
		case Active, Deprecated, Disabled:
		default:
			return nil, fmt.Errorf("%w: %s has status %q", ErrInvalid, s.ID, s.Status)
		}
		if s.Status != Disabled && s.Verify == nil {
			return nil, fmt.Errorf("%w: %s needs a verifier", ErrInvalid, s.ID)
		}
		if _, dup := r.suites[s.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate suite %q", ErrInvalid, s.ID)
		}
		r.suites[s.ID] = s
	}
	return r, nil
}

// Status returns a suite's state.
func (r *Registry) Status(id string) (Status, bool) {
	s, ok := r.suites[id]
	return s.Status, ok
}

// CanRegister reports whether a new account may use the suite.
func (r *Registry) CanRegister(id string) error {
	s, ok := r.suites[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknown, id)
	}
	switch s.Status {
	case Active:
		return nil
	case Deprecated:
		return fmt.Errorf("%w: %q", ErrDeprecated, id)
	default:
		return fmt.Errorf("%w: %q", ErrDisabled, id)
	}
}

// Verify checks a signature. Deprecated suites still verify history; disabled
// suites never verify.
func (r *Registry) Verify(id string, publicKey, message, signature []byte) error {
	s, ok := r.suites[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknown, id)
	}
	if s.Status == Disabled {
		return fmt.Errorf("%w: %q", ErrDisabled, id)
	}
	if !s.Verify(publicKey, message, signature) {
		return fmt.Errorf("%w: %q signature", ErrInvalid, id)
	}
	return nil
}

// IDs returns suite IDs in sorted order.
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.suites))
	for id := range r.suites {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
