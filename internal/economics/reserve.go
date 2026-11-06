package economics

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

var (
	// ErrProof means a published reserve commitment does not match what was
	// observed.
	ErrProof = errors.New("proof")
	// ErrUnbacked means the route's reserves do not cover its obligations.
	ErrUnbacked = errors.New("unbacked")
	// ErrSlippage means a conversion would deliver less than the user allowed.
	ErrSlippage = errors.New("slippage")
)

// ConversionRateScale is the denominator of Rate: ARCE base units per this
// many base units of the converted asset.
const ConversionRateScale = 100_000_000

// Reserve is one protocol-operated conversion route. Reserves and Liabilities
// are ARCE base units: Reserves is what the route holds, Liabilities is what it
// has promised. A route never mints ARCE; it can only quote from its spare
// reserves.
type Reserve struct {
	Asset       string `json:"asset"`
	Rate        uint64 `json:"rate"`
	Reserves    uint64 `json:"reserves"`
	Liabilities uint64 `json:"liabilities"`
}

// NewReserve creates an empty route. A zero rate means no route exists.
func NewReserve(asset string, rate uint64) (*Reserve, error) {
	if strings.TrimSpace(asset) == "" || rate == 0 {
		return nil, fmt.Errorf("%w: asset and a positive rate are required", ErrInvalid)
	}
	return &Reserve{Asset: asset, Rate: rate}, nil
}

// Covered reports whether reserves cover every outstanding obligation.
func (r *Reserve) Covered() bool {
	return r.Reserves >= r.Liabilities
}

// Disabled reports whether new conversions are refused. A route is disabled
// from the moment it is unbacked until it is recapitalized.
func (r *Reserve) Disabled() bool {
	return !r.Covered()
}

// SetReserves replaces the reserve balance and re-evaluates coverage.
func (r *Reserve) SetReserves(amount uint64) {
	r.Reserves = amount
}

// SetLiabilities replaces the committed obligations and re-evaluates coverage.
func (r *Reserve) SetLiabilities(amount uint64) {
	r.Liabilities = amount
}

// Recapitalize adds reserves. It re-enables a route once the new total covers
// the liabilities again.
func (r *Reserve) Recapitalize(amount uint64) {
	r.Reserves += amount
}

// Spare is the reserve above the route's obligations.
func (r *Reserve) Spare() uint64 {
	if !r.Covered() {
		return 0
	}
	return r.Reserves - r.Liabilities
}

// Quote converts assetAmount into ARCE at the route rate and refuses a quote
// below minARCE or above spare coverage. A disabled route is unbacked.
func (r *Reserve) Quote(assetAmount, minARCE uint64) (uint64, error) {
	if assetAmount == 0 {
		return 0, fmt.Errorf("%w: amount must be positive", ErrInvalid)
	}
	if r.Disabled() {
		return 0, fmt.Errorf("%w: route %s has %d reserves for %d liabilities",
			ErrUnbacked, r.Asset, r.Reserves, r.Liabilities)
	}
	quoted := new(big.Int).SetUint64(assetAmount)
	quoted.Mul(quoted, new(big.Int).SetUint64(r.Rate))
	quoted.Div(quoted, big.NewInt(ConversionRateScale))
	if !quoted.IsUint64() {
		return 0, fmt.Errorf("%w: conversion overflow", ErrInvalid)
	}
	arce := quoted.Uint64()
	if arce < minARCE {
		return 0, fmt.Errorf("%w: %d ARCE below minimum %d", ErrSlippage, arce, minARCE)
	}
	if arce > r.Spare() {
		return 0, fmt.Errorf("%w: %d ARCE requested, %d spare", ErrUnbacked, arce, r.Spare())
	}
	return arce, nil
}

// Proof is the published reserve and liability commitment for one route.
type Proof struct {
	Asset       string `json:"asset"`
	Rate        uint64 `json:"rate"`
	Reserves    uint64 `json:"reserves"`
	Liabilities uint64 `json:"liabilities"`
	At          uint64 `json:"at"`
	Digest      string `json:"digest"`
}

// Commit publishes the current reserves and liabilities.
func (r *Reserve) Commit(at uint64) Proof {
	p := Proof{
		Asset: r.Asset, Rate: r.Rate, Reserves: r.Reserves,
		Liabilities: r.Liabilities, At: at,
	}
	p.Digest = proofDigest(p)
	return p
}

// Verify checks a published commitment against observed reserves and
// liabilities, then checks that the reserves cover the obligations. Anything
// else is refused instead of reported as healthy.
func (p Proof) Verify(reserves, liabilities uint64) error {
	if p.Digest != proofDigest(p) {
		return fmt.Errorf("%w: commitment digest does not match its fields", ErrProof)
	}
	if reserves != p.Reserves || liabilities != p.Liabilities {
		return fmt.Errorf("%w: observed %d/%d, committed %d/%d",
			ErrProof, reserves, liabilities, p.Reserves, p.Liabilities)
	}
	if reserves < liabilities {
		return fmt.Errorf("%w: %d reserves for %d liabilities",
			ErrUnbacked, reserves, liabilities)
	}
	return nil
}

func proofDigest(p Proof) string {
	h := sha256.New()
	for _, part := range []string{
		"arne-reserve-v1", p.Asset, strconv.FormatUint(p.Rate, 10),
		strconv.FormatUint(p.Reserves, 10), strconv.FormatUint(p.Liabilities, 10),
		strconv.FormatUint(p.At, 10),
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
