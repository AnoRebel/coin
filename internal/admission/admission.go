// Package admission separates public validator entry from consortium
// approval. Public mode admits any registered validator that meets the staking
// minimums; consortium mode requires distinct approvals from an authorized
// policy before the validator can enter the active set.
package admission

import (
	"errors"
	"fmt"
	"sort"
)

// Mode is the network admission policy.
type Mode string

const (
	Public     Mode = "public"
	Consortium Mode = "consortium"
)

var (
	ErrInvalid         = errors.New("invalid")
	ErrMode            = errors.New("wrong mode")
	ErrUnauthorized    = errors.New("unauthorized approver")
	ErrDuplicate       = errors.New("duplicate approval")
	ErrAlreadyApproved = errors.New("already approved")
)

// Policy is the governance-set admission rule.
type Policy struct {
	Mode      Mode     `json:"mode"`
	Approvers []string `json:"approvers"`
	Threshold uint32   `json:"threshold"`
}

// Approval is the on-chain record for one validator.
type Approval struct {
	Validator string   `json:"validator"`
	Approvers []string `json:"approvers"`
	Height    uint64   `json:"height"`
}

// State is the admission record set.
type State struct {
	Policy    Policy               `json:"policy"`
	Approvals map[string]*Approval `json:"approvals"`
}

// New validates a policy and returns empty admission state.
func New(policy Policy) (*State, error) {
	if policy.Mode != Public && policy.Mode != Consortium {
		return nil, fmt.Errorf("%w: mode %q", ErrInvalid, policy.Mode)
	}
	seen := map[string]bool{}
	approvers := make([]string, 0, len(policy.Approvers))
	for _, approver := range policy.Approvers {
		if approver == "" || seen[approver] {
			return nil, fmt.Errorf("%w: approver %q", ErrInvalid, approver)
		}
		seen[approver] = true
		approvers = append(approvers, approver)
	}
	sort.Strings(approvers)
	if policy.Mode == Consortium {
		if len(approvers) == 0 {
			return nil, fmt.Errorf("%w: consortium needs approvers", ErrInvalid)
		}
		if policy.Threshold == 0 || int(policy.Threshold) > len(approvers) {
			return nil, fmt.Errorf("%w: threshold must be in 1..%d", ErrInvalid, len(approvers))
		}
	} else if policy.Threshold != 0 || len(approvers) != 0 {
		return nil, fmt.Errorf("%w: public mode has no approvers", ErrInvalid)
	}
	policy.Approvers = approvers
	return &State{Policy: policy, Approvals: map[string]*Approval{}}, nil
}

// Allowed reports whether a validator may activate at the next epoch.
func (s *State) Allowed(validator string) bool {
	if s.Policy.Mode == Public {
		return true
	}
	record, ok := s.Approvals[validator]
	return ok && uint32(len(record.Approvers)) >= s.Policy.Threshold
}

// Approve records one distinct approver. Reaching the threshold stamps the
// height at which the validator became eligible.
func (s *State) Approve(validator, approver string, at uint64) error {
	if s.Policy.Mode != Consortium {
		return fmt.Errorf("%w: public validators do not need approval", ErrMode)
	}
	if validator == "" || approver == "" {
		return fmt.Errorf("%w: validator and approver are required", ErrInvalid)
	}
	if !contains(s.Policy.Approvers, approver) {
		return fmt.Errorf("%w: %s", ErrUnauthorized, approver)
	}
	record := s.Approvals[validator]
	if record == nil {
		record = &Approval{Validator: validator}
		s.Approvals[validator] = record
	}
	if uint32(len(record.Approvers)) >= s.Policy.Threshold {
		return fmt.Errorf("%w: validator %s", ErrAlreadyApproved, validator)
	}
	if contains(record.Approvers, approver) {
		return fmt.Errorf("%w: %s", ErrDuplicate, approver)
	}
	record.Approvers = append(record.Approvers, approver)
	sort.Strings(record.Approvers)
	if uint32(len(record.Approvers)) == s.Policy.Threshold {
		record.Height = at
	}
	return nil
}

// Approved returns the activation map for staking.State.SetAdmission.
func (s *State) Approved() map[string]bool {
	out := map[string]bool{}
	for validator := range s.Approvals {
		if s.Allowed(validator) {
			out[validator] = true
		}
	}
	return out
}

// Status reports "public", "approved", or "pending".
func (s *State) Status(validator string) string {
	if s.Policy.Mode == Public {
		return "public"
	}
	if s.Allowed(validator) {
		return "approved"
	}
	return "pending"
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
