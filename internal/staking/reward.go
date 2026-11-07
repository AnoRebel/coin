package staking

import (
	"fmt"
	"sort"
)

// DelegatorReward is one delegator's share of an epoch budget.
type DelegatorReward struct {
	Address string `json:"address"`
	Stake   uint64 `json:"stake"`
	Amount  uint64 `json:"amount"`
}

// ValidatorReward is one active validator's share, split into commission and
// delegator rewards. The validator's own delegation appears in Delegators.
type ValidatorReward struct {
	Validator     string            `json:"validator"`
	Stake         uint64            `json:"stake"`
	CommissionBps uint32            `json:"commission_bps"`
	Commission    uint64            `json:"commission"`
	Delegators    []DelegatorReward `json:"delegators"`
	Total         uint64            `json:"total"`
}

// RewardDistribution is one epoch's reconciled reward split.
type RewardDistribution struct {
	Epoch      uint64            `json:"epoch"`
	Budget     uint64            `json:"budget"`
	Treasury   uint64            `json:"treasury"`
	Validators []ValidatorReward `json:"validators"`
	Total      uint64            `json:"total"`
}

// DistributeRewards splits an epoch budget across the active validators in
// proportion to bonded stake. Jailed, pending, unbonding, and unbonded
// validators earn nothing. Each validator keeps its commission and the rest is
// shared among its delegators; rounding dust goes to the treasury so the parts
// always sum to the budget.
func (s *State) DistributeRewards(budget uint64, treasuryBps uint32) (RewardDistribution, error) {
	if budget == 0 {
		return RewardDistribution{}, fmt.Errorf("%w: budget must be positive", ErrInvalid)
	}
	if treasuryBps > 10_000 {
		return RewardDistribution{}, fmt.Errorf("%w: treasury_bps above 10000", ErrInvalid)
	}
	treasury := budget * uint64(treasuryBps) / 10_000
	remaining := budget - treasury

	addresses := make([]string, 0, len(s.Validators))
	var weight uint64
	for address, v := range s.Validators {
		if v.Status != Active {
			continue
		}
		stake := s.Stake(address)
		if stake == 0 {
			continue
		}
		addresses = append(addresses, address)
		weight += stake
	}
	sort.Strings(addresses)
	distribution := RewardDistribution{Epoch: s.Epoch, Budget: budget}
	if weight == 0 {
		distribution.Treasury = budget
		distribution.Total = budget
		return distribution, nil
	}

	var distributed uint64
	for _, address := range addresses {
		v := s.Validators[address]
		stake := s.Stake(address)
		gross := remaining * stake / weight
		commission := gross * uint64(v.CommissionBps) / 10_000
		pool := gross - commission
		delegators := make([]string, 0, len(s.Delegations[address]))
		for delegator := range s.Delegations[address] {
			delegators = append(delegators, delegator)
		}
		sort.Strings(delegators)
		reward := ValidatorReward{
			Validator: address, Stake: stake, CommissionBps: v.CommissionBps,
			Commission: commission,
		}
		var paid uint64
		for i, delegator := range delegators {
			amount := s.Delegations[address][delegator]
			share := pool * amount / stake
			if i == len(delegators)-1 {
				share = pool - paid
			}
			reward.Delegators = append(reward.Delegators, DelegatorReward{
				Address: delegator, Stake: amount, Amount: share,
			})
			paid += share
		}
		reward.Total = commission + paid
		distributed += gross
		distribution.Validators = append(distribution.Validators, reward)
	}
	distribution.Treasury = treasury + (remaining - distributed)
	distribution.Total = distribution.Treasury
	for _, reward := range distribution.Validators {
		distribution.Total += reward.Total
	}
	if distribution.Total != budget {
		return RewardDistribution{}, fmt.Errorf("%w: distributed %d of %d", ErrInvalid, distribution.Total, budget)
	}
	return distribution, nil
}
