// Package economics implements ARCE supply accounting, the bounded tail
// emission, genesis allocations, and vesting gates.
package economics

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// SecondsPerYear is a 365-day year, the denominator for the tail rate.
const SecondsPerYear = 365 * 24 * 60 * 60

// MaxTailRateBps is the hard bound on annual tail emission: 5% of supply.
const MaxTailRateBps = 500

// Security tail-emission purposes only.
const (
	ValidatorRewards = "validator_rewards"
	ProtocolSecurity = "protocol_security"
)

var (
	ErrInvalid = errors.New("invalid")
	ErrCap     = errors.New("cap")
	ErrLocked  = errors.New("locked")
	ErrPurpose = errors.New("purpose")
	ErrRate    = errors.New("rate")
)

// Categories are the genesis allocation buckets. Every bucket is published.
var Categories = map[string]bool{
	"community": true, "contributors": true, "treasury": true,
	"validators": true, "liquidity": true,
}

// Schedule publishes one genesis allocation with its vesting terms.
type Schedule struct {
	Beneficiary string `json:"beneficiary"`
	Category    string `json:"category"`
	Amount      uint64 `json:"amount"`
	Cliff       uint64 `json:"cliff"`
	Duration    uint64 `json:"duration"`
}

// ValidateGenesis publishes and checks a genesis allocation. It returns the
// allocated total, which must not exceed the hard cap.
func ValidateGenesis(cap uint64, schedules []Schedule) (uint64, error) {
	if cap == 0 {
		return 0, fmt.Errorf("%w: cap must be positive", ErrInvalid)
	}
	if len(schedules) == 0 {
		return 0, fmt.Errorf("%w: at least one allocation is required", ErrInvalid)
	}
	var total uint64
	for _, s := range schedules {
		if err := s.validate(); err != nil {
			return 0, err
		}
		if s.Amount > cap-total {
			return 0, fmt.Errorf("%w: allocations exceed cap %d", ErrCap, cap)
		}
		total += s.Amount
	}
	return total, nil
}

func (s Schedule) validate() error {
	if strings.TrimSpace(s.Beneficiary) == "" {
		return fmt.Errorf("%w: beneficiary is required", ErrInvalid)
	}
	if !Categories[s.Category] {
		return fmt.Errorf("%w: category %q", ErrInvalid, s.Category)
	}
	if s.Amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	return nil
}

// Entry is one recorded mint or burn.
type Entry struct {
	Kind    string `json:"kind"` // mint, tail, burn
	Amount  uint64 `json:"amount"`
	Purpose string `json:"purpose,omitempty"`
	At      uint64 `json:"at"`
}

// Supply tracks ARCE issuance. Ordinary mints never exceed Cap. Tail emission
// starts only at the cap, is limited to security purposes, and is bounded to
// MaxTailRateBps a year.
type Supply struct {
	Cap         uint64  `json:"cap"`
	TailRateBps uint32  `json:"tail_rate_bps"`
	Minted      uint64  `json:"minted"`
	Burned      uint64  `json:"burned"`
	Tail        uint64  `json:"tail"`
	Entries     []Entry `json:"entries"`
}

// NewSupply returns a supply with the cap set and no tail emission.
func NewSupply(cap uint64) (*Supply, error) {
	if cap == 0 {
		return nil, fmt.Errorf("%w: cap must be positive", ErrInvalid)
	}
	return &Supply{Cap: cap}, nil
}

// Total is circulating supply: ordinary mints plus tail emission minus burns.
func (s *Supply) Total() uint64 {
	return s.Minted + s.Tail - s.Burned
}

// Mint issues ordinary supply. A mint that would pass the cap is refused.
func (s *Supply) Mint(amount uint64, purpose string, at uint64) error {
	if amount == 0 || strings.TrimSpace(purpose) == "" {
		return fmt.Errorf("%w: amount and purpose are required", ErrInvalid)
	}
	total := s.Total()
	if total > s.Cap || amount > s.Cap-total {
		return fmt.Errorf("%w: mint would exceed cap %d", ErrCap, s.Cap)
	}
	s.Minted += amount
	s.Entries = append(s.Entries, Entry{Kind: "mint", Amount: amount, Purpose: purpose, At: at})
	return nil
}

// Burn destroys circulating supply.
func (s *Supply) Burn(amount uint64, at uint64) error {
	if amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if amount > s.Total() {
		return fmt.Errorf("%w: burn above supply", ErrInvalid)
	}
	s.Burned += amount
	s.Entries = append(s.Entries, Entry{Kind: "burn", Amount: amount, At: at})
	return nil
}

// SetTailRate sets the annual tail rate in basis points. The hard bound is
// MaxTailRateBps regardless of governance.
func (s *Supply) SetTailRate(bps uint32) error {
	if bps > MaxTailRateBps {
		return fmt.Errorf("%w: tail rate above %d bps", ErrRate, MaxTailRateBps)
	}
	s.TailRateBps = bps
	return nil
}

// MintTail issues tail emission for one epoch. It requires the cap to be
// reached, a security purpose, an enabled rate, and an annualized amount
// within that rate.
func (s *Supply) MintTail(amount, epoch, at uint64) error {
	if amount == 0 || epoch == 0 {
		return fmt.Errorf("%w: amount and epoch are required", ErrInvalid)
	}
	if s.Total() < s.Cap {
		return fmt.Errorf("%w: tail emission starts at the cap", ErrCap)
	}
	if s.TailRateBps == 0 {
		return fmt.Errorf("%w: tail emission is disabled", ErrRate)
	}
	// amount * SecondsPerYear * 10000 <= total * rateBps * epoch
	left := new(big.Int).SetUint64(amount)
	left.Mul(left, big.NewInt(SecondsPerYear))
	left.Mul(left, big.NewInt(10_000))
	right := new(big.Int).SetUint64(s.Total())
	right.Mul(right, big.NewInt(int64(s.TailRateBps)))
	right.Mul(right, new(big.Int).SetUint64(epoch))
	if left.Cmp(right) > 0 {
		return fmt.Errorf("%w: %d per %ds exceeds %d bps a year", ErrRate, amount, epoch, s.TailRateBps)
	}
	s.Tail += amount
	s.Entries = append(s.Entries, Entry{Kind: "tail", Amount: amount, Purpose: ProtocolSecurity, At: at})
	return nil
}

// MintTailFor is MintTail with an explicit purpose. Only validator rewards and
// protocol security are accepted.
func (s *Supply) MintTailFor(amount, epoch, at uint64, purpose string) error {
	if purpose != ValidatorRewards && purpose != ProtocolSecurity {
		return fmt.Errorf("%w: tail emission may not fund %q", ErrPurpose, purpose)
	}
	if err := s.MintTail(amount, epoch, at); err != nil {
		return err
	}
	s.Entries[len(s.Entries)-1].Purpose = purpose
	return nil
}

// VestingEvent records an unlock.
type VestingEvent struct {
	Kind   string `json:"kind"`
	At     uint64 `json:"at"`
	Amount uint64 `json:"amount"`
}

// Vesting gates one schedule. Locked units are refused until the cliff, then
// unlock linearly; the schedule is fully transferable at Cliff+Duration.
type Vesting struct {
	Schedule    Schedule       `json:"schedule"`
	Transferred uint64         `json:"transferred"`
	Completed   bool           `json:"completed"`
	Events      []VestingEvent `json:"events,omitempty"`
}

// NewVesting validates and creates a vesting gate.
func NewVesting(schedule Schedule) (*Vesting, error) {
	if err := schedule.validate(); err != nil {
		return nil, err
	}
	return &Vesting{Schedule: schedule}, nil
}

// Unlocked is the amount transferable at now.
func (v *Vesting) Unlocked(now uint64) uint64 {
	s := v.Schedule
	if now < s.Cliff {
		return 0
	}
	if s.Duration == 0 || now >= s.Cliff+s.Duration {
		return s.Amount
	}
	elapsed := now - s.Cliff
	// Split the multiply so a large amount cannot overflow uint64.
	return s.Amount/s.Duration*elapsed + (s.Amount%s.Duration)*elapsed/s.Duration
}

// Transferable is Unlocked minus what already left.
func (v *Vesting) Transferable(now uint64) uint64 {
	return v.Unlocked(now) - v.Transferred
}

// Transfer releases amount from the schedule. It refuses locked units.
func (v *Vesting) Transfer(amount, now uint64) error {
	if amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	available := v.Transferable(now)
	if amount > available {
		return fmt.Errorf("%w: %d unlocked, %d requested", ErrLocked, available, amount)
	}
	v.Transferred += amount
	return nil
}

// Advance records completion once the schedule has fully elapsed. It returns
// true only on the transition.
func (v *Vesting) Advance(now uint64) bool {
	if v.Completed || now < v.Schedule.Cliff+v.Schedule.Duration {
		return false
	}
	v.Completed = true
	v.Events = append(v.Events, VestingEvent{
		Kind: "vested", At: now, Amount: v.Schedule.Amount,
	})
	return true
}
