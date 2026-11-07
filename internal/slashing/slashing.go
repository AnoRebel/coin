// Package slashing verifies equivocation evidence and applies stake penalties.
//
// Double-sign evidence is cryptographically checked against the validator's
// registered consensus key. Downtime evidence is structural: the consensus
// adapter must derive the missed count from finalized commit data, because an
// absence cannot be proven from signatures alone.
package slashing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/anorebel/anorebelcoin/internal/crypto/bls"
	"github.com/anorebel/anorebelcoin/internal/staking"
)

var (
	ErrInvalid      = errors.New("invalid")
	ErrStale        = errors.New("stale")
	ErrForged       = errors.New("forged")
	ErrDuplicate    = errors.New("duplicate")
	ErrInsufficient = errors.New("insufficient")
	ErrNotEligible  = errors.New("not-eligible")
)

// Params are the slashing policy parameters.
type Params struct {
	WindowBlocks         uint64 `json:"window_blocks"`
	JailBlocks           uint64 `json:"jail_blocks"`
	DoubleSignBps        uint32 `json:"double_sign_bps"`
	DowntimeBps          uint32 `json:"downtime_bps"`
	ReporterBps          uint32 `json:"reporter_bps"`
	ReporterCap          uint64 `json:"reporter_cap"` // 0 = uncapped
	BurnBps              uint32 `json:"burn_bps"`
	DowntimeThresholdBps uint32 `json:"downtime_threshold_bps"`
	DowntimeMinBlocks    uint64 `json:"downtime_min_blocks"`
}

// Vote is one signed consensus vote.
type Vote struct {
	Height    uint64 `json:"height"`
	Round     uint32 `json:"round"`
	BlockHash string `json:"block_hash"`
	Signature string `json:"signature"`
}

// DoubleSignEvidence shows one validator signing two blocks at the same
// height and round.
type DoubleSignEvidence struct {
	Validator string `json:"validator"`
	A         Vote   `json:"a"`
	B         Vote   `json:"b"`
}

// DowntimeEvidence is derived by the consensus adapter from finalized commit
// data. It is not accepted from an untrusted network caller.
type DowntimeEvidence struct {
	Validator string `json:"validator"`
	From      uint64 `json:"from"`
	To        uint64 `json:"to"`
	Missed    uint64 `json:"missed"`
	Total     uint64 `json:"total"`
}

// Result is the penalty and its split.
type Result struct {
	Kind       string            `json:"kind"`
	Validator  string            `json:"validator"`
	Slashed    uint64            `json:"slashed"`
	Reporter   uint64            `json:"reporter"`
	Treasury   uint64            `json:"treasury"`
	Burn       uint64            `json:"burn"`
	JailUntil  uint64            `json:"jail_until"`
	Delegators map[string]uint64 `json:"delegators"`
}

// Engine applies evidence exactly once.
type Engine struct {
	network string
	params  Params
	applied map[string]bool
}

// New validates policy parameters.
func New(network string, params Params) (*Engine, error) {
	if network == "" {
		return nil, fmt.Errorf("%w: network is required", ErrInvalid)
	}
	if params.WindowBlocks == 0 || params.JailBlocks == 0 {
		return nil, fmt.Errorf("%w: window and jail blocks must be positive", ErrInvalid)
	}
	if params.DoubleSignBps == 0 || params.DoubleSignBps > 10_000 {
		return nil, fmt.Errorf("%w: double_sign_bps must be in 1..10000", ErrInvalid)
	}
	if params.DowntimeBps == 0 || params.DowntimeBps > 10_000 {
		return nil, fmt.Errorf("%w: downtime_bps must be in 1..10000", ErrInvalid)
	}
	if params.ReporterBps > 10_000 {
		return nil, fmt.Errorf("%w: reporter_bps above 10000", ErrInvalid)
	}
	if params.BurnBps > 10_000 {
		return nil, fmt.Errorf("%w: burn_bps above 10000", ErrInvalid)
	}
	return &Engine{network: network, params: params, applied: map[string]bool{}}, nil
}

// DoubleSign verifies both votes and slashes the validator. The evidence must
// arrive within WindowBlocks of the equivocation height.
func (e *Engine) DoubleSign(st *staking.State, ev DoubleSignEvidence, at uint64) (Result, error) {
	if ev.Validator == "" || ev.A.BlockHash == "" || ev.B.BlockHash == "" {
		return Result{}, fmt.Errorf("%w: missing validator or block hash", ErrInvalid)
	}
	if ev.A.Height == 0 || ev.A.Height != ev.B.Height {
		return Result{}, fmt.Errorf("%w: votes must share a height", ErrInvalid)
	}
	if ev.A.Round != ev.B.Round {
		return Result{}, fmt.Errorf("%w: votes must share a round", ErrInvalid)
	}
	if ev.A.BlockHash == ev.B.BlockHash {
		return Result{}, fmt.Errorf("%w: votes are not conflicting", ErrInvalid)
	}
	if at < ev.A.Height {
		return Result{}, fmt.Errorf("%w: evidence from the future", ErrInvalid)
	}
	if at > ev.A.Height+e.params.WindowBlocks {
		return Result{}, fmt.Errorf("%w: evidence window closed at %d", ErrStale, ev.A.Height+e.params.WindowBlocks)
	}
	if err := e.eligible(st, ev.Validator); err != nil {
		return Result{}, err
	}
	key := fmt.Sprintf("double|%s|%d|%d", ev.Validator, ev.A.Height, ev.A.Round)
	if e.applied[key] {
		return Result{}, fmt.Errorf("%w: double-sign at %d", ErrDuplicate, ev.A.Height)
	}
	v := st.Validators[ev.Validator]
	if err := e.verifyVote(v.ConsensusKey, ev.Validator, ev.A); err != nil {
		return Result{}, err
	}
	if err := e.verifyVote(v.ConsensusKey, ev.Validator, ev.B); err != nil {
		return Result{}, err
	}
	result, err := e.apply(st, "double_sign", ev.Validator, e.params.DoubleSignBps, at)
	if err != nil {
		return Result{}, err
	}
	e.applied[key] = true
	return result, nil
}

// Downtime applies a downtime penalty when the missed share meets the policy
// threshold. The window must be complete and within the evidence window.
func (e *Engine) Downtime(st *staking.State, ev DowntimeEvidence, at uint64) (Result, error) {
	if ev.Validator == "" || ev.From == 0 || ev.To < ev.From {
		return Result{}, fmt.Errorf("%w: invalid downtime window", ErrInvalid)
	}
	if ev.Total != ev.To-ev.From+1 {
		return Result{}, fmt.Errorf("%w: total does not match the window", ErrForged)
	}
	if ev.Missed > ev.Total {
		return Result{}, fmt.Errorf("%w: missed above total", ErrForged)
	}
	if ev.Total < e.params.DowntimeMinBlocks {
		return Result{}, fmt.Errorf("%w: window shorter than %d blocks", ErrInsufficient, e.params.DowntimeMinBlocks)
	}
	if at < ev.To {
		return Result{}, fmt.Errorf("%w: evidence from the future", ErrInvalid)
	}
	if at > ev.To+e.params.WindowBlocks {
		return Result{}, fmt.Errorf("%w: evidence window closed at %d", ErrStale, ev.To+e.params.WindowBlocks)
	}
	if ev.Missed*10_000 < ev.Total*uint64(e.params.DowntimeThresholdBps) {
		return Result{}, fmt.Errorf("%w: missed share below threshold", ErrInsufficient)
	}
	if err := e.eligible(st, ev.Validator); err != nil {
		return Result{}, err
	}
	key := fmt.Sprintf("downtime|%s|%d|%d", ev.Validator, ev.From, ev.To)
	if e.applied[key] {
		return Result{}, fmt.Errorf("%w: downtime window %d-%d", ErrDuplicate, ev.From, ev.To)
	}
	result, err := e.apply(st, "downtime", ev.Validator, e.params.DowntimeBps, at)
	if err != nil {
		return Result{}, err
	}
	e.applied[key] = true
	return result, nil
}

func (e *Engine) eligible(st *staking.State, validator string) error {
	v, ok := st.Validators[validator]
	if !ok {
		return fmt.Errorf("%w: validator %s", ErrNotEligible, validator)
	}
	if v.Status == staking.Unbonded {
		return fmt.Errorf("%w: validator is unbonded", ErrNotEligible)
	}
	return nil
}

func (e *Engine) verifyVote(keyHex, validator string, vote Vote) error {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return fmt.Errorf("%w: registered key is not hex", ErrInvalid)
	}
	sig, err := hex.DecodeString(vote.Signature)
	if err != nil || len(sig) == 0 {
		return fmt.Errorf("%w: signature is not hex", ErrForged)
	}
	msg := VoteMessage(e.network, validator, vote.Height, vote.Round, vote.BlockHash)
	if !bls.Verify(bls.PublicKey(key), msg, bls.Signature(sig)) {
		return fmt.Errorf("%w: vote signature does not match the validator", ErrForged)
	}
	return nil
}

// Split is a slashed amount divided between the treasury, reporter, and burn.
type Split struct {
	Burn     uint64 `json:"burn"`
	Reporter uint64 `json:"reporter"`
	Treasury uint64 `json:"treasury"`
}

// SplitPenalty divides a slashed amount by policy. The reporter share is
// capped, the burn share is a fixed percentage, and the treasury takes the
// remainder, including any reporter-cap excess.
func SplitPenalty(slashed uint64, params Params) Split {
	reporter := slashed * uint64(params.ReporterBps) / 10_000
	if params.ReporterCap > 0 && reporter > params.ReporterCap {
		reporter = params.ReporterCap
	}
	burn := slashed * uint64(params.BurnBps) / 10_000
	if reporter+burn > slashed {
		burn = slashed - reporter
	}
	return Split{Burn: burn, Reporter: reporter, Treasury: slashed - reporter - burn}
}

func (e *Engine) apply(st *staking.State, kind, validator string, bps uint32, at uint64) (Result, error) {
	penalty, err := st.Penalize(validator, bps, at)
	if err != nil {
		return Result{}, err
	}
	jailUntil := at + e.params.JailBlocks
	if err := st.Jail(validator, at, jailUntil); err != nil {
		return Result{}, err
	}
	split := SplitPenalty(penalty.Total, e.params)
	// Copy the map so a later staking change cannot mutate a result.
	delegators := make(map[string]uint64, len(penalty.Delegators))
	for address, amount := range penalty.Delegators {
		delegators[address] = amount
	}
	return Result{
		Kind: kind, Validator: validator, Slashed: penalty.Total,
		Reporter: split.Reporter, Treasury: split.Treasury, Burn: split.Burn,
		JailUntil: jailUntil, Delegators: delegators,
	}, nil
}

// VoteMessage is the canonical bytes a validator signs for one consensus vote.
func VoteMessage(network, validator string, height uint64, round uint32, blockHash string) []byte {
	h := sha256.New()
	_, _ = h.Write([]byte("arne-vote-v1"))
	for _, part := range [][]byte{
		[]byte(network), []byte(validator),
		[]byte(strconv.FormatUint(height, 10)),
		[]byte(strconv.FormatUint(uint64(round), 10)),
		[]byte(blockHash),
	} {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}
