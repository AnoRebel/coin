package ledger

import (
	"encoding/binary"
	"fmt"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Params are consensus parameters. Defaults are governance-configurable and
// pinned in the genesis file; changing them requires a network upgrade.
type Params struct {
	BaseFee         uint64 `json:"base_fee"`
	MinFee          uint64 `json:"min_fee"`
	MaxPriorityFee  uint64 `json:"max_priority_fee"`
	FeeBurnBps      uint32 `json:"fee_burn_bps"`
	FeeValidatorBps uint32 `json:"fee_validator_bps"`
	FeeTreasuryBps  uint32 `json:"fee_treasury_bps"`
	MaxMemoBytes    int    `json:"max_memo_bytes"`
	MaxBatchItems   int    `json:"max_batch_items"`
	NativeMaxSupply uint64 `json:"native_max_supply"`
	MaxTxValidity   uint64 `json:"max_tx_validity"` // seconds, 0 = unlimited
}

// DefaultParams returns mainnet-class defaults.
func DefaultParams() Params {
	return Params{
		BaseFee:         1000,
		MinFee:          1000,
		MaxPriorityFee:  1_000_000_000_000,
		FeeBurnBps:      5000,
		FeeValidatorBps: 3000,
		FeeTreasuryBps:  2000,
		MaxMemoBytes:    256,
		MaxBatchItems:   100,
		NativeMaxSupply: 100_000_000_000_000_000, // 1e9 ARCE at 8 decimals
		MaxTxValidity:   30 * 24 * 3600,
	}
}

// Validate checks parameter consistency.
func (p Params) Validate() error {
	if p.FeeBurnBps+p.FeeValidatorBps+p.FeeTreasuryBps != 10_000 {
		return fmt.Errorf("%w: fee split must total 10000 bps", protocol.ErrValidation)
	}
	if p.NativeMaxSupply == 0 || p.MaxBatchItems == 0 {
		return fmt.Errorf("%w: zero parameter", protocol.ErrValidation)
	}
	if p.MinFee > p.BaseFee {
		return fmt.Errorf("%w: min_fee above base_fee", protocol.ErrValidation)
	}
	return nil
}

// State key layout. Every key is consensus-critical and mirrored in Python.
const (
	kNonce     = "account/%s/nonce"
	kAuth      = "account/%s/auth"
	kBalance   = "balance/%s/%s"
	kAsset     = "asset/%s"
	kSupply    = "supply/%s"
	kBurned    = "burned/%s"
	kEscrow    = "escrow/%s"
	kEscrowAmt = "escrow_held/%s"
	kIntent    = "intent/%s"
	kTransfer  = "transfer/%s"
	kRefunded  = "refunded/%s"
	kAllowance = "allowance/%s/%s/%s"
	kAllowExp  = "allowance_expiry/%s/%s/%s"
	kPool      = "pool/%s/%s"
	kRoute     = "route/%s"
	kMeta      = "meta/%s"
)

// Pool names.
const (
	PoolFeeSwap    = "fee_swap"
	PoolValidators = "validators"
	PoolTreasury   = "treasury"
	PoolFaucet     = "faucet"
	PoolCommunity  = "community"
)

func accountAuth(b []byte) string { return string(b) }
func encodeUint(v uint64) []byte  { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
func decodeUint(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
