// Package staking implements the deterministic validator lifecycle, stake and
// delegation accounting, and epoch-boundary validator-set changes.
//
// The package is consensus logic: it does not move ledger balances. Callers
// move ARCE before calling in, and apply the resulting set to the consensus
// engine. Registrations verify a BLS proof of possession, so a rogue key cannot
// be admitted into an aggregate.
package staking

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/anorebel/anorebelcoin/internal/crypto/bls"
)

// Status is the validator lifecycle state.
type Status string

const (
	Pending   Status = "pending"
	Active    Status = "active"
	Jailed    Status = "jailed"
	Unbonding Status = "unbonding"
	Unbonded  Status = "unbonded"
)

var (
	ErrInvalid      = errors.New("invalid")
	ErrDuplicate    = errors.New("duplicate")
	ErrUnknown      = errors.New("unknown")
	ErrInsufficient = errors.New("insufficient")
	ErrSelfBond     = errors.New("self-bond")
	ErrCommission   = errors.New("commission")
	ErrPoP          = errors.New("pop")
	ErrEpoch        = errors.New("epoch")
	ErrStatus       = errors.New("status")
)

// Params are the governance-set staking parameters.
type Params struct {
	MinSelfBond     uint64 `json:"min_self_bond"`
	MinStake        uint64 `json:"min_stake"`
	MaxPowerBps     uint32 `json:"max_power_bps"` // 0 or 10000 means uncapped
	EpochBlocks     uint64 `json:"epoch_blocks"`
	UnbondingBlocks uint64 `json:"unbonding_blocks"`
}

// Validator is one registered consensus participant.
type Validator struct {
	Address       string `json:"address"`
	ConsensusKey  string `json:"consensus_key"`
	PoP           string `json:"pop"`
	CommissionBps uint32 `json:"commission_bps"`
	SelfBond      uint64 `json:"self_bond"`
	Status        Status `json:"status"`
	JailedUntil   uint64 `json:"jailed_until"`
	UnbondingEnd  uint64 `json:"unbonding_end"`
}

// Unbonding is a delegation withdrawal that is still slashable.
type UnbondingEntry struct {
	Validator string `json:"validator"`
	Delegator string `json:"delegator"`
	Amount    uint64 `json:"amount"`
	Release   uint64 `json:"release"`
}

// State is the complete staking state.
type State struct {
	Network     string                       `json:"network"`
	Params      Params                       `json:"params"`
	Height      uint64                       `json:"height"`
	Epoch       uint64                       `json:"epoch"`
	Validators  map[string]*Validator        `json:"validators"`
	Delegations map[string]map[string]uint64 `json:"delegations"` // validator -> delegator -> amount
	Unbonding   []UnbondingEntry             `json:"unbonding"`
	Powers      map[string]uint64            `json:"powers"` // settled at the last epoch
	Active      []string                     `json:"active"`
	// Admission, when non-nil, restricts activation to addresses mapped true.
	// Public networks leave it nil; consortium networks fill it from approvals.
	Admission map[string]bool `json:"admission,omitempty"`
}

// New returns an empty staking state. Params are validated before use.
func New(network string, params Params) (*State, error) {
	if network == "" {
		return nil, fmt.Errorf("%w: network is required", ErrInvalid)
	}
	if params.EpochBlocks == 0 {
		return nil, fmt.Errorf("%w: epoch_blocks must be positive", ErrInvalid)
	}
	if params.MinSelfBond == 0 || params.MinStake == 0 {
		return nil, fmt.Errorf("%w: stake minimums must be positive", ErrInvalid)
	}
	if params.MaxPowerBps > 10_000 {
		return nil, fmt.Errorf("%w: max_power_bps above 10000", ErrInvalid)
	}
	return &State{
		Network: network, Params: params,
		Validators:  map[string]*Validator{},
		Delegations: map[string]map[string]uint64{},
		Powers:      map[string]uint64{},
	}, nil
}

// Register creates a pending validator after checking its BLS proof of
// possession. Activation still waits for the next epoch and the minimum stake.
func (s *State) Register(v Validator, at uint64) error {
	if v.Address == "" {
		return fmt.Errorf("%w: validator address is required", ErrInvalid)
	}
	if _, ok := s.Validators[v.Address]; ok {
		return fmt.Errorf("%w: validator %s", ErrDuplicate, v.Address)
	}
	if v.CommissionBps > 10_000 {
		return fmt.Errorf("%w: commission above 10000 bps", ErrCommission)
	}
	if v.SelfBond < s.Params.MinSelfBond {
		return fmt.Errorf("%w: self-bond below %d", ErrSelfBond, s.Params.MinSelfBond)
	}
	key, err := hex.DecodeString(v.ConsensusKey)
	if err != nil || len(key) == 0 {
		return fmt.Errorf("%w: consensus key is not hex", ErrInvalid)
	}
	pop, err := hex.DecodeString(v.PoP)
	if err != nil || len(pop) == 0 {
		return fmt.Errorf("%w: proof of possession is not hex", ErrInvalid)
	}
	if !bls.Verify(bls.PublicKey(key), bls.PopMessage(s.Network, v.Address, key), bls.Signature(pop)) {
		return fmt.Errorf("%w: proof of possession does not match the key", ErrPoP)
	}
	if err := s.touch(at); err != nil {
		return err
	}
	v.Status = Pending
	s.Validators[v.Address] = &v
	s.Delegations[v.Address] = map[string]uint64{v.Address: v.SelfBond}
	return nil
}

// Delegate adds bonded stake. Voting power changes only at the next epoch.
func (s *State) Delegate(validator, delegator string, amount, at uint64) error {
	v, ok := s.Validators[validator]
	if !ok {
		return fmt.Errorf("%w: validator %s", ErrUnknown, validator)
	}
	if v.Status == Unbonded {
		return fmt.Errorf("%w: validator is unbonded", ErrStatus)
	}
	if delegator == "" {
		return fmt.Errorf("%w: delegator is required", ErrInvalid)
	}
	if amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if err := s.touch(at); err != nil {
		return err
	}
	if s.Delegations[validator] == nil {
		s.Delegations[validator] = map[string]uint64{}
	}
	s.Delegations[validator][delegator] += amount
	if delegator == validator {
		v.SelfBond += amount
	}
	return nil
}

// Undelegate moves stake to the unbonding queue. It stays slashable until the
// unbonding period elapses, and voting power changes only at the next epoch.
func (s *State) Undelegate(validator, delegator string, amount, at uint64) error {
	v, ok := s.Validators[validator]
	if !ok {
		return fmt.Errorf("%w: validator %s", ErrUnknown, validator)
	}
	if v.Status == Unbonded {
		return fmt.Errorf("%w: validator is unbonded", ErrStatus)
	}
	if amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	held := s.Delegations[validator][delegator]
	if amount > held {
		return fmt.Errorf("%w: undelegate %d above stake %d", ErrInsufficient, amount, held)
	}
	if err := s.touch(at); err != nil {
		return err
	}
	s.Delegations[validator][delegator] = held - amount
	if delegator == validator {
		v.SelfBond -= amount
	}
	s.Unbonding = append(s.Unbonding, UnbondingEntry{
		Validator: validator, Delegator: delegator, Amount: amount,
		Release: at + s.Params.UnbondingBlocks,
	})
	return nil
}

// BeginUnbonding starts a full validator exit. The validator leaves the active
// set at the next epoch and its stake is released when the period elapses.
func (s *State) BeginUnbonding(validator string, at uint64) error {
	v, ok := s.Validators[validator]
	if !ok {
		return fmt.Errorf("%w: validator %s", ErrUnknown, validator)
	}
	if v.Status != Pending && v.Status != Active {
		return fmt.Errorf("%w: cannot unbond from %s", ErrStatus, v.Status)
	}
	if err := s.touch(at); err != nil {
		return err
	}
	v.Status = Unbonding
	v.UnbondingEnd = at + s.Params.UnbondingBlocks
	return nil
}

// Jail removes a validator from future active sets until the jail height.
func (s *State) Jail(validator string, at, until uint64) error {
	v, ok := s.Validators[validator]
	if !ok {
		return fmt.Errorf("%w: validator %s", ErrUnknown, validator)
	}
	if v.Status == Unbonded {
		return fmt.Errorf("%w: validator is unbonded", ErrStatus)
	}
	if until <= at {
		return fmt.Errorf("%w: jail end must be after the current height", ErrInvalid)
	}
	if err := s.touch(at); err != nil {
		return err
	}
	v.Status = Jailed
	v.JailedUntil = until
	return nil
}

// Unjail returns a jailed validator to pending. It rejoins the active set at
// the next epoch if it still meets the stake minimums.
func (s *State) Unjail(validator string, at uint64) error {
	v, ok := s.Validators[validator]
	if !ok {
		return fmt.Errorf("%w: validator %s", ErrUnknown, validator)
	}
	if v.Status != Jailed {
		return fmt.Errorf("%w: validator is %s, not jailed", ErrStatus, v.Status)
	}
	if at < v.JailedUntil {
		return fmt.Errorf("%w: jail ends at %d", ErrStatus, v.JailedUntil)
	}
	if err := s.touch(at); err != nil {
		return err
	}
	v.Status = Pending
	return nil
}

// AdvanceEpoch applies the scheduled set change at the first block of the next
// epoch. The height must be exactly on the boundary.
func (s *State) AdvanceEpoch(at uint64) error {
	next := (s.Epoch + 1) * s.Params.EpochBlocks
	if at != next {
		return fmt.Errorf("%w: epoch %d starts at %d", ErrEpoch, s.Epoch+1, next)
	}
	s.Height = at
	s.Epoch++

	// Release delegations whose unbonding period has elapsed.
	kept := s.Unbonding[:0]
	for _, u := range s.Unbonding {
		if u.Release > at {
			kept = append(kept, u)
		}
	}
	s.Unbonding = kept

	for _, v := range s.Validators {
		if v.Status == Unbonding && v.UnbondingEnd <= at {
			v.Status = Unbonded
			v.SelfBond = 0
			delete(s.Delegations, v.Address)
			continue
		}
		if v.Status == Jailed || v.Status == Unbonded || v.Status == Unbonding {
			continue
		}
		if s.eligible(v) {
			v.Status = Active
		} else {
			v.Status = Pending
		}
	}

	candidates := make([]string, 0, len(s.Validators))
	var total uint64
	for address, v := range s.Validators {
		if v.Status != Active {
			continue
		}
		stake := s.Stake(address)
		candidates = append(candidates, address)
		total += stake
	}
	sort.Strings(candidates)

	s.Active = candidates
	s.Powers = map[string]uint64{}
	limit := total
	if s.Params.MaxPowerBps > 0 && s.Params.MaxPowerBps < 10_000 {
		limit = total * uint64(s.Params.MaxPowerBps) / 10_000
	}
	for _, address := range candidates {
		power := s.Stake(address)
		if power > limit {
			power = limit
		}
		s.Powers[address] = power
	}
	return nil
}

func (s *State) eligible(v *Validator) bool {
	if s.Admission != nil && !s.Admission[v.Address] {
		return false
	}
	if v.SelfBond < s.Params.MinSelfBond {
		return false
	}
	return s.Stake(v.Address) >= s.Params.MinStake
}

// SetAdmission restricts activation to the allowed addresses. A nil map means
// public admission.
func (s *State) SetAdmission(allowed map[string]bool) {
	s.Admission = allowed
}

func (s *State) touch(at uint64) error {
	if at < s.Height {
		return fmt.Errorf("%w: height %d is before %d", ErrInvalid, at, s.Height)
	}
	s.Height = at
	return nil
}

// Stake returns bonded stake, including the validator's self-bond.
func (s *State) Stake(validator string) uint64 {
	var total uint64
	for _, amount := range s.Delegations[validator] {
		total += amount
	}
	return total
}

// VotingPower returns the settled power for the current epoch.
func (s *State) VotingPower(validator string) uint64 { return s.Powers[validator] }

// ValidatorView is a deterministic snapshot of one validator.
type ValidatorView struct {
	Address       string `json:"address"`
	Status        Status `json:"status"`
	SelfBond      uint64 `json:"self_bond"`
	Delegated     uint64 `json:"delegated"`
	Stake         uint64 `json:"stake"`
	VotingPower   uint64 `json:"voting_power"`
	CommissionBps uint32 `json:"commission_bps"`
}

// Penalty is the stake removed by a slash, per delegator.
type Penalty struct {
	Total      uint64            `json:"total"`
	Delegators map[string]uint64 `json:"delegators"`
}

// Penalize removes a percentage of all stake the validator controls, including
// delegation withdrawals still in the unbonding queue. It does not change the
// validator status; the slashing policy jails separately.
func (s *State) Penalize(validator string, bps uint32, at uint64) (Penalty, error) {
	v, ok := s.Validators[validator]
	if !ok {
		return Penalty{}, fmt.Errorf("%w: validator %s", ErrUnknown, validator)
	}
	if v.Status == Unbonded {
		return Penalty{}, fmt.Errorf("%w: validator is unbonded", ErrStatus)
	}
	if bps == 0 || bps > 10_000 {
		return Penalty{}, fmt.Errorf("%w: slash bps must be in 1..10000", ErrInvalid)
	}
	if err := s.touch(at); err != nil {
		return Penalty{}, err
	}
	out := Penalty{Delegators: map[string]uint64{}}
	for delegator, amount := range s.Delegations[validator] {
		cut := amount * uint64(bps) / 10_000
		s.Delegations[validator][delegator] = amount - cut
		if delegator == validator {
			v.SelfBond -= cut
		}
		out.Delegators[delegator] += cut
		out.Total += cut
	}
	for i := range s.Unbonding {
		u := &s.Unbonding[i]
		if u.Validator != validator {
			continue
		}
		cut := u.Amount * uint64(bps) / 10_000
		u.Amount -= cut
		out.Delegators[u.Delegator] += cut
		out.Total += cut
	}
	return out, nil
}

// Snapshot is the state shape used by conformance vectors.
type Snapshot struct {
	Epoch      uint64           `json:"epoch"`
	Height     uint64           `json:"height"`
	Active     []string         `json:"active"`
	Validators []ValidatorView  `json:"validators"`
	Unbonding  []UnbondingEntry `json:"unbonding"`
}

// Snapshot returns maps and slices in a deterministic order.
func (s *State) Snapshot() Snapshot {
	addresses := make([]string, 0, len(s.Validators))
	for address := range s.Validators {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	views := make([]ValidatorView, 0, len(addresses))
	for _, address := range addresses {
		v := s.Validators[address]
		stake := s.Stake(address)
		views = append(views, ValidatorView{
			Address: address, Status: v.Status, SelfBond: v.SelfBond,
			Delegated: stake - v.SelfBond, Stake: stake,
			VotingPower: s.Powers[address], CommissionBps: v.CommissionBps,
		})
	}
	active := append([]string(nil), s.Active...)
	unbonding := append([]UnbondingEntry(nil), s.Unbonding...)
	sort.Slice(unbonding, func(i, j int) bool {
		if unbonding[i].Validator != unbonding[j].Validator {
			return unbonding[i].Validator < unbonding[j].Validator
		}
		if unbonding[i].Delegator != unbonding[j].Delegator {
			return unbonding[i].Delegator < unbonding[j].Delegator
		}
		return unbonding[i].Release < unbonding[j].Release
	})
	return Snapshot{Epoch: s.Epoch, Height: s.Height, Active: active, Validators: views, Unbonding: unbonding}
}
