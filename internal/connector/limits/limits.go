// Package limits enforces connector exposure caps and rate limits, and records
// every governed limit change.
//
// A reservation is the connector's promise to move value. Submitting one checks
// the single-transfer size, the counterparty's rate window, the asset-wide
// exposure cap, and the counterparty exposure cap, in that order, before any
// value moves. Releasing a reservation gives the exposure back.
package limits

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalid      = errors.New("limits: invalid")
	ErrUnknownAsset = errors.New("limits: asset is not configured")
	ErrTransfer     = errors.New("limits: transfer exceeds the maximum size")
	ErrRate         = errors.New("limits: rate limit exceeded")
	ErrExposure     = errors.New("limits: exposure cap reached")
	ErrUnknown      = errors.New("limits: unknown reservation")
	ErrState        = errors.New("limits: reservation is already released")
)

// Policy is one asset's limits. The caps must be ordered: a single transfer
// cannot exceed the counterparty cap, and a counterparty cannot exceed the
// asset cap.
type Policy struct {
	Asset                   string `json:"asset"`
	MaxExposure             uint64 `json:"max_exposure"`
	MaxCounterpartyExposure uint64 `json:"max_counterparty_exposure"`
	MaxTransfer             uint64 `json:"max_transfer"`
	RateCount               int    `json:"rate_count"`
	RateWindow              int64  `json:"rate_window"`
}

// Reservation is an accepted submission that holds exposure until released.
type Reservation struct {
	ID           string `json:"id"`
	Asset        string `json:"asset"`
	Counterparty string `json:"counterparty"`
	Amount       uint64 `json:"amount"`
	At           int64  `json:"at"`
	Released     bool   `json:"released"`
}

// Change is one governed limit update. Prior is the zero Policy when the asset
// is configured for the first time.
type Change struct {
	Asset  string `json:"asset"`
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
	Prior  Policy `json:"prior"`
	Next   Policy `json:"next"`
}

// Book is the limit state for every configured asset. It is not safe for
// concurrent use.
type Book struct {
	policies map[string]Policy
	exposure map[string]uint64
	counter  map[string]uint64
	rates    map[string][]int64
	held     map[string]*Reservation
	order    []*Reservation
	history  []Change
	seq      uint64
}

// New starts an empty book.
func New() *Book {
	return &Book{
		policies: map[string]Policy{},
		exposure: map[string]uint64{},
		counter:  map[string]uint64{},
		rates:    map[string][]int64{},
		held:     map[string]*Reservation{},
	}
}

// Govern validates and installs a policy, recording who changed it, why, when,
// and the prior value. Lowering a cap below current exposure is allowed: it
// stops new reservations without invalidating ones already made.
func (b *Book) Govern(next Policy, actor, reason string, now int64) (Change, error) {
	if err := next.validate(); err != nil {
		return Change{}, err
	}
	if err := text("actor", actor, 128); err != nil {
		return Change{}, err
	}
	if err := text("reason", reason, 512); err != nil {
		return Change{}, err
	}
	if now < 0 {
		return Change{}, fmt.Errorf("%w: now must not be negative", ErrInvalid)
	}
	change := Change{
		Asset: next.Asset, Actor: actor, Reason: reason, At: now,
		Prior: b.policies[next.Asset], Next: next,
	}
	b.policies[next.Asset] = next
	b.history = append(b.history, change)
	return change, nil
}

// Submit reserves exposure if every limit passes. A rejected submission
// consumes no rate budget and no exposure.
func (b *Book) Submit(asset, counterparty string, amount uint64, now int64) (Reservation, error) {
	policy, ok := b.policies[asset]
	if !ok {
		return Reservation{}, fmt.Errorf("%w: %q", ErrUnknownAsset, asset)
	}
	if err := text("counterparty", counterparty, 128); err != nil {
		return Reservation{}, err
	}
	if amount == 0 {
		return Reservation{}, fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if now < 0 {
		return Reservation{}, fmt.Errorf("%w: now must not be negative", ErrInvalid)
	}
	if amount > policy.MaxTransfer {
		return Reservation{}, fmt.Errorf("%w: %d above %d", ErrTransfer, amount, policy.MaxTransfer)
	}

	key := counterKey(asset, counterparty)
	window := prune(b.rates[key], now-policy.RateWindow)
	b.rates[key] = window
	if len(window) >= policy.RateCount {
		return Reservation{}, fmt.Errorf("%w: %d per %ds", ErrRate, policy.RateCount, policy.RateWindow)
	}

	if b.exposure[asset]+amount > policy.MaxExposure {
		return Reservation{}, fmt.Errorf("%w: asset %s has %d of %d",
			ErrExposure, asset, b.exposure[asset], policy.MaxExposure)
	}
	if b.counter[key]+amount > policy.MaxCounterpartyExposure {
		return Reservation{}, fmt.Errorf("%w: counterparty %s has %d of %d",
			ErrExposure, counterparty, b.counter[key], policy.MaxCounterpartyExposure)
	}

	b.seq++
	reservation := &Reservation{
		ID:           reservationID(asset, counterparty, b.seq),
		Asset:        asset,
		Counterparty: counterparty,
		Amount:       amount,
		At:           now,
	}
	b.held[reservation.ID] = reservation
	b.order = append(b.order, reservation)
	b.exposure[asset] += amount
	b.counter[key] += amount
	b.rates[key] = append(window, now)
	return *reservation, nil
}

// Release gives the exposure back. Releasing twice is a state error.
func (b *Book) Release(id string) (Reservation, error) {
	reservation, ok := b.held[id]
	if !ok {
		return Reservation{}, fmt.Errorf("%w: %q", ErrUnknown, id)
	}
	if reservation.Released {
		return Reservation{}, fmt.Errorf("%w: %q", ErrState, id)
	}
	reservation.Released = true
	b.exposure[reservation.Asset] -= reservation.Amount
	key := counterKey(reservation.Asset, reservation.Counterparty)
	b.counter[key] -= reservation.Amount
	return *reservation, nil
}

// Exposure is the outstanding amount for an asset.
func (b *Book) Exposure(asset string) uint64 { return b.exposure[asset] }

// CounterpartyExposure is the outstanding amount for one asset and counterparty.
func (b *Book) CounterpartyExposure(asset, counterparty string) uint64 {
	return b.counter[counterKey(asset, counterparty)]
}

// History returns every governed change, oldest first.
func (b *Book) History() []Change { return append([]Change(nil), b.history...) }

// Reservation returns one held reservation by ID.
func (b *Book) Reservation(id string) (Reservation, bool) {
	reservation, ok := b.held[id]
	if !ok {
		return Reservation{}, false
	}
	return *reservation, true
}

func (p Policy) validate() error {
	if err := text("asset", p.Asset, 128); err != nil {
		return err
	}
	if p.MaxExposure == 0 || p.MaxCounterpartyExposure == 0 || p.MaxTransfer == 0 {
		return fmt.Errorf("%w: caps must be positive", ErrInvalid)
	}
	if p.MaxTransfer > p.MaxCounterpartyExposure {
		return fmt.Errorf("%w: max_transfer above max_counterparty_exposure", ErrInvalid)
	}
	if p.MaxCounterpartyExposure > p.MaxExposure {
		return fmt.Errorf("%w: max_counterparty_exposure above max_exposure", ErrInvalid)
	}
	if p.RateCount <= 0 || p.RateWindow <= 0 {
		return fmt.Errorf("%w: rate_count and rate_window must be positive", ErrInvalid)
	}
	return nil
}

func text(field, value string, max int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	if utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%w: %s must be at most %d characters", ErrInvalid, field, max)
	}
	return nil
}

// prune drops timestamps that fell out of the half-open window (now-window, now].
func prune(timestamps []int64, cutoff int64) []int64 {
	out := timestamps[:0]
	for _, at := range timestamps {
		if at > cutoff {
			out = append(out, at)
		}
	}
	return out
}

func counterKey(asset, counterparty string) string {
	return asset + "\x00" + counterparty
}

func reservationID(asset, counterparty string, seq uint64) string {
	h := sha256.New()
	h.Write([]byte("arne-reservation-v1"))
	h.Write([]byte{0})
	h.Write([]byte(asset))
	h.Write([]byte{0})
	h.Write([]byte(counterparty))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatUint(seq, 10)))
	return hex.EncodeToString(h.Sum(nil)[:16])
}
