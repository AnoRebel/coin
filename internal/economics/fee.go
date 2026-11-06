package economics

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

var (
	ErrFee     = errors.New("fee")
	ErrExpired = errors.New("expired")
	ErrBudget  = errors.New("budget")
)

// DefaultMaxChangeBps bounds one block's base-fee movement to 12.5%.
const DefaultMaxChangeBps = 1250

// FeeMarket is the deterministic congestion rule. The base fee moves toward the
// target utilization by the same proportion the block is over or under it,
// bounded by MaxChangeBps, and clamped to [MinBaseFee, MaxBaseFee].
type FeeMarket struct {
	BaseFee      uint64 `json:"base_fee"`
	MinBaseFee   uint64 `json:"min_base_fee"`
	MaxBaseFee   uint64 `json:"max_base_fee"`
	TargetUnits  uint64 `json:"target_units"`
	MaxChangeBps uint32 `json:"max_change_bps"`
}

// NewFeeMarket validates a market.
func NewFeeMarket(base, min, max, target uint64, maxChangeBps uint32) (*FeeMarket, error) {
	if base == 0 || target == 0 || min == 0 || max < base || base < min {
		return nil, fmt.Errorf("%w: base, min, max and target must be ordered and positive", ErrInvalid)
	}
	if maxChangeBps == 0 || maxChangeBps > 10_000 {
		return nil, fmt.Errorf("%w: max_change_bps must be 1..10000", ErrInvalid)
	}
	return &FeeMarket{
		BaseFee: base, MinBaseFee: min, MaxBaseFee: max,
		TargetUnits: target, MaxChangeBps: maxChangeBps,
	}, nil
}

// Next returns the base fee for the next block after using used units.
func (m *FeeMarket) Next(used uint64) uint64 {
	if used == m.TargetUnits {
		return m.BaseFee
	}
	base := new(big.Int).SetUint64(m.BaseFee)
	var diff, target uint64
	over := used > m.TargetUnits
	if over {
		diff, target = used-m.TargetUnits, m.TargetUnits
	} else {
		diff, target = m.TargetUnits-used, m.TargetUnits
	}
	change := new(big.Int).SetUint64(diff)
	change.Mul(change, base)
	change.Div(change, new(big.Int).SetUint64(target))
	cap := new(big.Int).Mul(base, big.NewInt(int64(m.MaxChangeBps)))
	cap.Div(cap, big.NewInt(10_000))
	if change.Cmp(cap) > 0 {
		change = cap
	}
	next := new(big.Int).Set(base)
	if over {
		next.Add(next, change)
	} else {
		next.Sub(next, change)
	}
	if next.Sign() < 0 {
		next.SetUint64(0)
	}
	value := next.Uint64()
	if value < m.MinBaseFee {
		return m.MinBaseFee
	}
	if value > m.MaxBaseFee {
		return m.MaxBaseFee
	}
	return value
}

// EffectiveFee applies a priority fee, the per-transaction cap, and the minimum
// fee. It is the value a transaction pays from its fee asset.
func EffectiveFee(base, priority, maxPriority, minFee uint64) (uint64, error) {
	if priority > maxPriority {
		return 0, fmt.Errorf("%w: priority fee above cap", ErrFee)
	}
	fee := base + priority
	if fee < base {
		return 0, fmt.Errorf("%w: fee overflow", ErrFee)
	}
	if fee < minFee {
		fee = minFee
	}
	return fee, nil
}

// Split is the disposition of one fee.
type Split struct {
	Burn       uint64 `json:"burn"`
	Validators uint64 `json:"validators"`
	Treasury   uint64 `json:"treasury"`
}

// SplitFee divides a fee. Treasury takes the rounding remainder so the parts
// always sum to the fee.
func SplitFee(fee uint64, burnBps, validatorBps uint32) (Split, error) {
	if uint64(burnBps)+uint64(validatorBps) > 10_000 {
		return Split{}, fmt.Errorf("%w: fee bps above 10000", ErrInvalid)
	}
	burn := bpsOf(fee, burnBps)
	validators := bpsOf(fee, validatorBps)
	return Split{Burn: burn, Validators: validators, Treasury: fee - burn - validators}, nil
}

func bpsOf(fee uint64, bps uint32) uint64 {
	value := new(big.Int).SetUint64(fee)
	value.Mul(value, big.NewInt(int64(bps)))
	value.Div(value, big.NewInt(10_000))
	return value.Uint64()
}

// Sponsorship pays fees for another account from a declared budget. It has no
// method that moves the sponsored account's funds.
type Sponsorship struct {
	Sponsor   string `json:"sponsor"`
	Sponsored string `json:"sponsored"`
	Asset     string `json:"asset"`
	Budget    uint64 `json:"budget"`
	Spent     uint64 `json:"spent"`
	ExpiresAt uint64 `json:"expires_at"`
}

// NewSponsorship validates a sponsorship.
func NewSponsorship(sponsor, sponsored, asset string, budget, expiresAt uint64) (*Sponsorship, error) {
	if strings.TrimSpace(sponsor) == "" || strings.TrimSpace(sponsored) == "" ||
		strings.TrimSpace(asset) == "" {
		return nil, fmt.Errorf("%w: sponsor, sponsored and asset are required", ErrInvalid)
	}
	if sponsor == sponsored {
		return nil, fmt.Errorf("%w: an account cannot sponsor itself", ErrInvalid)
	}
	if budget == 0 {
		return nil, fmt.Errorf("%w: budget must be positive", ErrInvalid)
	}
	return &Sponsorship{
		Sponsor: sponsor, Sponsored: sponsored, Asset: asset,
		Budget: budget, ExpiresAt: expiresAt,
	}, nil
}

// Remaining is the budget left at now.
func (s *Sponsorship) Remaining(now uint64) uint64 {
	if now >= s.ExpiresAt {
		return 0
	}
	return s.Budget - s.Spent
}

// Pay charges one fee to the sponsor's budget.
func (s *Sponsorship) Pay(amount, now uint64) error {
	if amount == 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if now >= s.ExpiresAt {
		return fmt.Errorf("%w: sponsorship ended at %d", ErrExpired, s.ExpiresAt)
	}
	if amount > s.Budget-s.Spent {
		return fmt.Errorf("%w: %d left, %d requested", ErrBudget, s.Budget-s.Spent, amount)
	}
	s.Spent += amount
	return nil
}
