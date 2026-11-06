// Package governance implements on-chain proposal lifecycles: deposit, voting,
// quorum and threshold tallying, timelock, atomic execution, and cancellation.
//
// It is consensus logic, not a ledger: the caller supplies voting power and
// action handlers. A handler that fails leaves no earlier action applied.
package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind is a proposal category. Constitutional proposals need a higher
// threshold and a longer timelock.
type Kind string

const (
	Parameter          Kind = "parameter"
	CodeUpgrade        Kind = "code_upgrade"
	Treasury           Kind = "treasury"
	ValidatorAdmission Kind = "validator_admission"
	AssetPolicy        Kind = "asset_policy"
	CompliancePolicy   Kind = "compliance_policy"
	SuiteChange        Kind = "suite_change"
	Constitutional     Kind = "constitutional"
)

var kinds = map[Kind]bool{
	Parameter: true, CodeUpgrade: true, Treasury: true,
	ValidatorAdmission: true, AssetPolicy: true, CompliancePolicy: true,
	SuiteChange: true, Constitutional: true,
}

// Option is a vote choice.
type Option string

const (
	Yes     Option = "yes"
	No      Option = "no"
	Abstain Option = "abstain"
)

// Status is the proposal lifecycle state.
type Status string

const (
	Deposit    Status = "deposit"
	Voting     Status = "voting"
	Passed     Status = "passed"
	Rejected   Status = "rejected"
	Timelocked Status = "timelock"
	Executed   Status = "executed"
	Cancelled  Status = "cancelled"
	Failed     Status = "failed"
)

var (
	ErrInvalid        = errors.New("invalid")
	ErrDuplicate      = errors.New("duplicate")
	ErrUnknown        = errors.New("unknown")
	ErrStatus         = errors.New("status")
	ErrVoted          = errors.New("voted")
	ErrPower          = errors.New("power")
	ErrUnauthorized   = errors.New("unauthorized")
	ErrAction         = errors.New("action")
	ErrConstitutional = errors.New("constitutional")
)

// Params are the governance parameters. BPS values are out of 10000.
type Params struct {
	MinDeposit                 uint64 `json:"min_deposit"`
	DepositPeriod              uint64 `json:"deposit_period"`
	VotingPeriod               uint64 `json:"voting_period"`
	Timelock                   uint64 `json:"timelock"`
	QuorumBps                  uint32 `json:"quorum_bps"`
	ThresholdBps               uint32 `json:"threshold_bps"`
	ConstitutionalThresholdBps uint32 `json:"constitutional_threshold_bps"`
	ConstitutionalTimelock     uint64 `json:"constitutional_timelock"`
	MaxActions                 int    `json:"max_actions"`
}

// Action is one state change an approved proposal performs.
type Action struct {
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// Vote records one voter's choice and weight.
type Vote struct {
	Option     Option            `json:"option"`
	Weight     uint64            `json:"weight"`
	At         uint64            `json:"at"`
	Role       string            `json:"role,omitempty"`
	Delegators map[string]uint64 `json:"delegators,omitempty"`
}

// Proposal is one governance proposal.
type Proposal struct {
	ID               string            `json:"id"`
	Network          string            `json:"network"`
	Kind             Kind              `json:"kind"`
	Proposer         string            `json:"proposer"`
	Nonce            uint64            `json:"nonce"`
	Title            string            `json:"title"`
	Actions          []Action          `json:"actions"`
	Deposit          uint64            `json:"deposit"`
	Status           Status            `json:"status"`
	CreatedAt        uint64            `json:"created_at"`
	VotingStart      uint64            `json:"voting_start"`
	VotingEnd        uint64            `json:"voting_end"`
	TimelockEnd      uint64            `json:"timelock_end"`
	ActivationHeight uint64            `json:"activation_height"`
	TotalPower       uint64            `json:"total_power"`
	Yes              uint64            `json:"yes"`
	No               uint64            `json:"no"`
	Abstain          uint64            `json:"abstain"`
	Votes            map[string]Vote   `json:"votes"`
	Attributed       map[string]string `json:"attributed,omitempty"`
	Refunded         bool              `json:"refunded"`
	ExecutionError   string            `json:"execution_error,omitempty"`
}

// ProposalID derives the stable ID: first 16 bytes of
// SHA-256("arne-proposal-v1" || 0 || network || 0 || proposer || 0 || nonce).
func ProposalID(network, proposer string, nonce uint64) string {
	h := sha256.New()
	h.Write([]byte("arne-proposal-v1"))
	h.Write([]byte{0})
	h.Write([]byte(network))
	h.Write([]byte{0})
	h.Write([]byte(proposer))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatUint(nonce, 10)))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// State is the proposal set for one network.
type State struct {
	Network    string               `json:"network"`
	Params     Params               `json:"params"`
	Invariants map[string]bool      `json:"invariants,omitempty"`
	Proposals  map[string]*Proposal `json:"proposals"`
}

// DefaultInvariants lists the parameters only a constitutional proposal may
// change. They are the supply cap, value conservation, the cryptographic suite
// floor, and shielded-pool solvency.
func DefaultInvariants() map[string]bool {
	return map[string]bool{
		"supply_cap":             true,
		"value_conservation":     true,
		"crypto_suite_floor":     true,
		"shielded_pool_solvency": true,
	}
}

func (s *State) isInvariant(name string) bool {
	if name == "" {
		return false
	}
	if s.Invariants == nil {
		return DefaultInvariants()[name]
	}
	return s.Invariants[name]
}

// New returns an empty state. Parameters are validated before use.
func New(network string, params Params) (*State, error) {
	if network == "" {
		return nil, fmt.Errorf("%w: network is required", ErrInvalid)
	}
	if params.VotingPeriod == 0 {
		return nil, fmt.Errorf("%w: voting_period must be positive", ErrInvalid)
	}
	if params.MaxActions <= 0 {
		return nil, fmt.Errorf("%w: max_actions must be positive", ErrInvalid)
	}
	if params.QuorumBps > 10_000 || params.ThresholdBps > 10_000 ||
		params.ConstitutionalThresholdBps > 10_000 {
		return nil, fmt.Errorf("%w: bps above 10000", ErrInvalid)
	}
	if params.ConstitutionalThresholdBps < params.ThresholdBps {
		return nil, fmt.Errorf("%w: constitutional threshold below ordinary", ErrInvalid)
	}
	if params.ConstitutionalTimelock < params.Timelock {
		return nil, fmt.Errorf("%w: constitutional timelock below ordinary", ErrInvalid)
	}
	return &State{
		Network: network, Params: params,
		Invariants: DefaultInvariants(),
		Proposals:  map[string]*Proposal{},
	}, nil
}

// Submit records a proposal and starts its deposit period. TotalPower is the
// snapshot of eligible voting power when the proposal is created.
func (s *State) Submit(p Proposal, totalPower, now uint64) error {
	if p.Proposer == "" {
		return fmt.Errorf("%w: proposer is required", ErrInvalid)
	}
	if !kinds[p.Kind] {
		return fmt.Errorf("%w: kind %q", ErrInvalid, p.Kind)
	}
	if p.Deposit < s.Params.MinDeposit {
		return fmt.Errorf("%w: deposit below %d", ErrInvalid, s.Params.MinDeposit)
	}
	if p.ActivationHeight == 0 {
		return fmt.Errorf("%w: activation_height is required", ErrInvalid)
	}
	if totalPower == 0 {
		return fmt.Errorf("%w: total voting power is zero", ErrInvalid)
	}
	if len(p.Actions) == 0 || len(p.Actions) > s.Params.MaxActions {
		return fmt.Errorf("%w: actions must be 1..%d", ErrInvalid, s.Params.MaxActions)
	}
	for _, action := range p.Actions {
		if strings.TrimSpace(action.Type) == "" {
			return fmt.Errorf("%w: action type is required", ErrInvalid)
		}
		if action.Type == "set_parameter" && p.Kind != Constitutional {
			if name, _ := action.Params["name"].(string); s.isInvariant(name) {
				return fmt.Errorf("%w: %s requires a constitutional proposal", ErrConstitutional, name)
			}
		}
	}
	if p.ID == "" {
		p.ID = ProposalID(s.Network, p.Proposer, p.Nonce)
	}
	if _, exists := s.Proposals[p.ID]; exists {
		return fmt.Errorf("%w: proposal %s", ErrDuplicate, p.ID)
	}
	p.Network = s.Network
	p.Status = Deposit
	p.CreatedAt = now
	p.VotingStart = now + s.Params.DepositPeriod
	p.VotingEnd = p.VotingStart + s.Params.VotingPeriod
	p.TotalPower = totalPower
	p.Votes = map[string]Vote{}
	p.Attributed = map[string]string{}
	if s.Params.DepositPeriod == 0 {
		p.Status = Voting
	}
	s.Proposals[p.ID] = &p
	return nil
}

// Get returns a copy-free proposal pointer.
func (s *State) Get(id string) (*Proposal, bool) {
	p, ok := s.Proposals[id]
	return p, ok
}

// Advance moves proposals whose deposit or voting period ended. It returns the
// proposal IDs whose status changed, in sorted order.
func (s *State) Advance(now uint64) []string {
	ids := make([]string, 0, len(s.Proposals))
	for id := range s.Proposals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	changed := []string{}
	for _, id := range ids {
		p := s.Proposals[id]
		switch p.Status {
		case Deposit:
			if now >= p.VotingStart {
				p.Status = Voting
				changed = append(changed, id)
			}
		case Voting:
			if now >= p.VotingEnd {
				s.tally(p)
				changed = append(changed, id)
			}
		}
	}
	return changed
}

func (s *State) tally(p *Proposal) {
	cast := p.Yes + p.No + p.Abstain
	if cast*10_000 < p.TotalPower*uint64(s.Params.QuorumBps) {
		p.Status = Rejected
		return
	}
	threshold := uint64(s.Params.ThresholdBps)
	if p.Kind == Constitutional {
		threshold = uint64(s.Params.ConstitutionalThresholdBps)
	}
	decided := p.Yes + p.No
	if decided == 0 || p.Yes*10_000 < decided*threshold {
		p.Status = Rejected
		return
	}
	p.Status = Passed
}

// Vote adds one voter's explicit weight.
func (s *State) Vote(id, voter string, option Option, weight, now uint64) error {
	return s.cast(id, voter, option, weight, "", nil, now)
}

func (s *State) cast(id, voter string, option Option, weight uint64, role string, delegators map[string]uint64, now uint64) error {
	p, ok := s.Proposals[id]
	if !ok {
		return fmt.Errorf("%w: proposal %s", ErrUnknown, id)
	}
	if voter == "" {
		return fmt.Errorf("%w: voter is required", ErrInvalid)
	}
	if weight == 0 {
		return fmt.Errorf("%w: weight must be positive", ErrInvalid)
	}
	if option != Yes && option != No && option != Abstain {
		return fmt.Errorf("%w: option %q", ErrInvalid, option)
	}
	if p.Status != Voting || now < p.VotingStart || now >= p.VotingEnd {
		return fmt.Errorf("%w: proposal is %s", ErrStatus, p.Status)
	}
	if _, voted := p.Votes[voter]; voted {
		return fmt.Errorf("%w: voter %s", ErrVoted, voter)
	}
	if p.Attributed == nil {
		p.Attributed = map[string]string{}
	}
	if _, used := p.Attributed[voter]; used {
		return fmt.Errorf("%w: voter %s already voted through a delegate", ErrVoted, voter)
	}
	for delegator := range delegators {
		if _, used := p.Attributed[delegator]; used {
			return fmt.Errorf("%w: delegator %s already voted", ErrVoted, delegator)
		}
	}
	if p.Yes+p.No+p.Abstain+weight > p.TotalPower {
		return fmt.Errorf("%w: cast above snapshot power", ErrPower)
	}
	p.Votes[voter] = Vote{Option: option, Weight: weight, At: now, Role: role, Delegators: delegators}
	p.Attributed[voter] = voter
	for delegator := range delegators {
		p.Attributed[delegator] = voter
	}
	switch option {
	case Yes:
		p.Yes += weight
	case No:
		p.No += weight
	case Abstain:
		p.Abstain += weight
	}
	return nil
}

// Queue starts the timelock for a passed proposal.
func (s *State) Queue(id string, now uint64) error {
	p, ok := s.Proposals[id]
	if !ok {
		return fmt.Errorf("%w: proposal %s", ErrUnknown, id)
	}
	if p.Status != Passed {
		return fmt.Errorf("%w: proposal is %s", ErrStatus, p.Status)
	}
	timelock := s.Params.Timelock
	if p.Kind == Constitutional {
		timelock = s.Params.ConstitutionalTimelock
	}
	p.TimelockEnd = now + timelock
	p.Status = Timelocked
	return nil
}

// Handler performs one action. If it fails it may return an undo for any part
// it already applied; the registry runs undos in reverse.
type Handler func(Action, *Proposal) (undo func(), err error)

// Registry maps action types to handlers.
type Registry struct {
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Handler{}}
}

func (r *Registry) Register(actionType string, h Handler) error {
	if actionType == "" || h == nil {
		return fmt.Errorf("%w: action type and handler are required", ErrInvalid)
	}
	if _, exists := r.handlers[actionType]; exists {
		return fmt.Errorf("%w: action %s", ErrDuplicate, actionType)
	}
	r.handlers[actionType] = h
	return nil
}

// Apply runs every action atomically. A missing handler is rejected before any
// handler runs.
func (r *Registry) Apply(p *Proposal) error {
	for _, action := range p.Actions {
		if _, ok := r.handlers[action.Type]; !ok {
			return fmt.Errorf("%w: no handler for %s", ErrAction, action.Type)
		}
	}
	undos := make([]func(), 0, len(p.Actions))
	for _, action := range p.Actions {
		undo, err := r.handlers[action.Type](action, p)
		if err != nil {
			if undo != nil {
				undo()
			}
			for i := len(undos) - 1; i >= 0; i-- {
				undos[i]()
			}
			return err
		}
		if undo != nil {
			undos = append(undos, undo)
		}
	}
	return nil
}

// Execute applies a timelocked proposal. On failure every applied action is
// reverted and the proposal is marked failed.
func (s *State) Execute(id string, now uint64, registry *Registry) error {
	p, ok := s.Proposals[id]
	if !ok {
		return fmt.Errorf("%w: proposal %s", ErrUnknown, id)
	}
	if p.Status != Timelocked {
		return fmt.Errorf("%w: proposal is %s", ErrStatus, p.Status)
	}
	if now < p.TimelockEnd {
		return fmt.Errorf("%w: timelock ends at %d", ErrStatus, p.TimelockEnd)
	}
	if err := registry.Apply(p); err != nil {
		p.Status = Failed
		p.ExecutionError = err.Error()
		return err
	}
	p.Status = Executed
	p.ExecutionError = ""
	return nil
}

// Cancel stops a proposal before execution. Only the proposer may cancel.
// The deposit is refunded only when nobody has voted.
func (s *State) Cancel(id, actor string, now uint64) error {
	p, ok := s.Proposals[id]
	if !ok {
		return fmt.Errorf("%w: proposal %s", ErrUnknown, id)
	}
	if actor != p.Proposer {
		return fmt.Errorf("%w: only the proposer may cancel", ErrUnauthorized)
	}
	switch p.Status {
	case Deposit, Voting, Passed, Timelocked:
	default:
		return fmt.Errorf("%w: proposal is %s", ErrStatus, p.Status)
	}
	p.Refunded = p.Yes+p.No+p.Abstain == 0
	p.Status = Cancelled
	return nil
}
