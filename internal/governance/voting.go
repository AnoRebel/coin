package governance

import (
	"fmt"
	"sort"
)

// VotingPower derives stake- or role-weighted ballots. Delegated stake counts
// for the delegate but stays attributed to the delegator.
type VotingPower struct {
	Stake       map[string]uint64 `json:"stake"`
	DelegatedTo map[string]string `json:"delegated_to,omitempty"`
	RoleOf      map[string]string `json:"role_of,omitempty"`
	RoleWeights map[string]uint64 `json:"role_weights,omitempty"`
	MaxVoterBps uint32            `json:"max_voter_bps,omitempty"`
	Consortium  bool              `json:"consortium,omitempty"`
}

// NewVotingPower returns an empty stake-weighted power set.
func NewVotingPower() *VotingPower {
	return &VotingPower{
		Stake:       map[string]uint64{},
		DelegatedTo: map[string]string{},
		RoleOf:      map[string]string{},
		RoleWeights: map[string]uint64{},
	}
}

// SetStake sets one member's bonded amount.
func (v *VotingPower) SetStake(member string, amount uint64) error {
	if member == "" {
		return fmt.Errorf("%w: member is required", ErrInvalid)
	}
	if v.Stake == nil {
		v.Stake = map[string]uint64{}
	}
	v.Stake[member] = amount
	return nil
}

// SetDelegation points a member's stake at a delegate. An empty delegate
// clears it. Cycles and self-delegation are rejected.
func (v *VotingPower) SetDelegation(delegator, delegate string) error {
	if _, ok := v.Stake[delegator]; !ok {
		return fmt.Errorf("%w: unknown delegator %s", ErrInvalid, delegator)
	}
	if delegate == "" {
		delete(v.DelegatedTo, delegator)
		return nil
	}
	if delegate == delegator {
		return fmt.Errorf("%w: self-delegation", ErrInvalid)
	}
	if _, ok := v.Stake[delegate]; !ok {
		return fmt.Errorf("%w: unknown delegate %s", ErrInvalid, delegate)
	}
	if v.DelegatedTo == nil {
		v.DelegatedTo = map[string]string{}
	}
	for at := delegate; at != ""; at = v.DelegatedTo[at] {
		if at == delegator {
			return fmt.Errorf("%w: delegation cycle", ErrInvalid)
		}
	}
	v.DelegatedTo[delegator] = delegate
	return nil
}

// SetRole assigns a consortium role and its charter weight.
func (v *VotingPower) SetRole(member, role string, weight uint64) error {
	if member == "" || role == "" {
		return fmt.Errorf("%w: member and role are required", ErrInvalid)
	}
	if weight == 0 {
		return fmt.Errorf("%w: role weight must be positive", ErrInvalid)
	}
	if v.RoleOf == nil {
		v.RoleOf = map[string]string{}
	}
	if v.RoleWeights == nil {
		v.RoleWeights = map[string]uint64{}
	}
	v.RoleOf[member] = role
	v.RoleWeights[role] = weight
	v.Consortium = true
	return nil
}

// Total is the eligible power: bonded stake, or the sum of member role weights.
func (v *VotingPower) Total() uint64 {
	if v.Consortium {
		var total uint64
		for member, role := range v.RoleOf {
			if _, ok := v.RoleWeights[role]; ok {
				total += v.RoleWeights[role]
				_ = member
			}
		}
		return total
	}
	var total uint64
	for _, amount := range v.Stake {
		total += amount
	}
	return total
}

// Ballot returns the weight, role, and delegated amounts for one voter. The
// delegated map excludes the voter's own stake and is sorted by delegator.
func (v *VotingPower) Ballot(voter string) (uint64, string, map[string]uint64) {
	if role, ok := v.RoleOf[voter]; ok {
		weight, allowed := v.RoleWeights[role]
		if !allowed {
			return 0, role, nil
		}
		return weight, role, nil
	}
	weight := v.Stake[voter]
	if v.DelegatedTo[voter] != "" {
		// Delegated stake is spent; it belongs to the delegate's ballot.
		weight = 0
	}
	delegators := map[string]uint64{}
	for member, delegate := range v.DelegatedTo {
		if delegate == voter && member != voter {
			delegators[member] = v.Stake[member]
			weight += v.Stake[member]
		}
	}
	if v.MaxVoterBps > 0 && v.MaxVoterBps < 10_000 {
		cap := v.Total() * uint64(v.MaxVoterBps) / 10_000
		if weight > cap {
			weight = cap
		}
	}
	if len(delegators) == 0 {
		return weight, "", nil
	}
	return weight, "", delegators
}

// SortedDelegators returns delegator addresses in deterministic order.
func SortedDelegators(delegators map[string]uint64) []string {
	out := make([]string, 0, len(delegators))
	for member := range delegators {
		out = append(out, member)
	}
	sort.Strings(out)
	return out
}

// CastVote computes a ballot from the power set and records it. A delegator
// whose power is already attributed cannot vote through a second delegate.
func (s *State) CastVote(id, voter string, option Option, power *VotingPower, now uint64) error {
	if power == nil {
		return fmt.Errorf("%w: voting power is required", ErrInvalid)
	}
	weight, role, delegators := power.Ballot(voter)
	if weight == 0 {
		return fmt.Errorf("%w: voter %s has no voting power", ErrPower, voter)
	}
	return s.cast(id, voter, option, weight, role, delegators, now)
}
