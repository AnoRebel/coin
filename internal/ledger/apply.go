package ledger

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Error codes are stable API contract values.
type Code string

const (
	CodeInvalidSchema     Code = "INVALID_SCHEMA"
	CodeInvalidPayload    Code = "INVALID_PAYLOAD"
	CodeBadSignature      Code = "BAD_SIGNATURE"
	CodeWrongNetwork      Code = "WRONG_NETWORK"
	CodeExpired           Code = "EXPIRED"
	CodeBadNonce          Code = "BAD_NONCE"
	CodeFeeTooLow         Code = "FEE_TOO_LOW"
	CodeNoFeeRoute        Code = "NO_FEE_ROUTE"
	CodeInsufficientFunds Code = "INSUFFICIENT_FUNDS"
	CodeUnknownAsset      Code = "UNKNOWN_ASSET"
	CodeAssetExists       Code = "ASSET_EXISTS"
	CodeUnauthorized      Code = "UNAUTHORIZED"
	CodeSupplyLimit       Code = "SUPPLY_LIMIT"
	CodeEscrowNotFound    Code = "ESCROW_NOT_FOUND"
	CodeEscrowClosed      Code = "ESCROW_CLOSED"
	CodeAllowanceExceeded Code = "ALLOWANCE_EXCEEDED"
	CodeLimitExceeded     Code = "LIMIT_EXCEEDED"
	CodeConservation      Code = "CONSERVATION_VIOLATION"
	CodeInvariant         Code = "INVARIANT_VIOLATION"
)

// ErrConservation is returned when supply accounting does not balance.
var ErrConservation = errors.New("ledger: supply conservation violated")

// ErrInvariant is returned when a consensus-critical invariant is broken.
var ErrInvariant = errors.New("ledger: invariant violated")

// Error is a typed transaction error.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func txErr(code Code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf extracts a stable error code.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, ErrConservation) {
		return CodeConservation
	}
	return CodeInvalidPayload
}

// Event is a receipt event.
type Event struct {
	Type  string            `json:"type"`
	Attrs map[string]string `json:"attrs"`
}

// Receipt is the result of applying one transaction.
type Receipt struct {
	Hash     string  `json:"hash"`
	Height   uint64  `json:"height"`
	Success  bool    `json:"success"`
	Code     Code    `json:"code,omitempty"`
	Error    string  `json:"error,omitempty"`
	Fee      uint64  `json:"fee"`
	FeeAsset string  `json:"fee_asset"`
	Events   []Event `json:"events"`
}

// ApplyTx validates and applies a transaction atomically. On any error the
// state is unchanged; failed transactions are not charged in v1.
// shortcut: failed txs are free, add fee-on-failure when mempool spam matters.
func (s *State) ApplyTx(tx protocol.Tx, now, height uint64) Receipt {
	hash, _ := tx.Hash()
	r := Receipt{Hash: hash, Height: height, FeeAsset: tx.FeeAsset}
	s.Begin()
	fee, err := s.apply(tx, now, &r)
	if err != nil {
		s.Rollback()
		r.Success = false
		r.Code = CodeOf(err)
		r.Error = err.Error()
		return r
	}
	s.Commit()
	r.Success = true
	r.Fee = fee
	return r
}

func (s *State) apply(tx protocol.Tx, now uint64, r *Receipt) (uint64, error) {
	if err := tx.Validate(); err != nil {
		return 0, wrapCode(CodeInvalidSchema, err)
	}
	if tx.Network != s.Network || tx.Chain != s.Chain {
		return 0, txErr(CodeWrongNetwork, "expected %s/%s", s.Network, s.Chain)
	}
	if tx.ExpiresAt != 0 && now > tx.ExpiresAt {
		return 0, txErr(CodeExpired, "expired at %d", tx.ExpiresAt)
	}
	if s.Params.MaxTxValidity > 0 && tx.ExpiresAt > now+s.Params.MaxTxValidity {
		return 0, txErr(CodeInvalidSchema, "expiry too far in the future")
	}
	if err := tx.Verify(); err != nil {
		return 0, txErr(CodeBadSignature, "%v", err)
	}
	if tx.Nonce != s.getUint(fmt.Sprintf(kNonce, tx.Signer)) {
		return 0, txErr(CodeBadNonce, "expected %d, got %d", s.getUint(fmt.Sprintf(kNonce, tx.Signer)), tx.Nonce)
	}
	fee, err := s.chargeFee(tx)
	if err != nil {
		return 0, err
	}
	s.ensureAccount(tx.Signer)
	s.setUint(fmt.Sprintf(kNonce, tx.Signer), tx.Nonce+1)
	if err := s.execute(tx, now, r); err != nil {
		return 0, err
	}
	if err := s.CheckConservation(tx.FeeAsset); err != nil {
		return 0, err
	}
	if err := s.CheckConservation(protocol.NativeAsset(s.Network)); err != nil {
		return 0, err
	}
	return fee, nil
}

func wrapCode(code Code, err error) error {
	return &Error{Code: code, Message: err.Error()}
}

func (s *State) chargeFee(tx protocol.Tx) (uint64, error) {
	if tx.PriorityFee > s.Params.MaxPriorityFee {
		return 0, txErr(CodeLimitExceeded, "priority fee above cap")
	}
	fee := s.Params.BaseFee + tx.PriorityFee
	if fee < s.Params.MinFee {
		fee = s.Params.MinFee
	}
	if tx.MaxFee < fee {
		return 0, txErr(CodeFeeTooLow, "fee %d above max_fee %d", fee, tx.MaxFee)
	}
	native := protocol.NativeAsset(s.Network)
	if tx.FeeAsset == native {
		if err := s.subBalance(tx.Signer, native, fee); err != nil {
			return 0, err
		}
		s.splitFee(native, fee)
		return fee, nil
	}
	if _, ok, _ := s.Asset(tx.FeeAsset); !ok {
		return 0, txErr(CodeUnknownAsset, "fee asset %q", tx.FeeAsset)
	}
	rate := s.Route(tx.FeeAsset)
	if rate == 0 {
		return 0, txErr(CodeNoFeeRoute, "no fee route for %q", tx.FeeAsset)
	}
	arceFee, ok := mulDiv(fee, rate, 100_000_000)
	if !ok {
		return 0, txErr(CodeLimitExceeded, "fee conversion overflow")
	}
	if err := s.subBalance(tx.Signer, tx.FeeAsset, fee); err != nil {
		return 0, err
	}
	s.addPool(PoolTreasury, tx.FeeAsset, fee)
	if err := s.subPool(PoolFeeSwap, native, arceFee); err != nil {
		return 0, txErr(CodeNoFeeRoute, "fee-swap reserve underfunded")
	}
	s.splitFee(native, arceFee)
	return fee, nil
}

func (s *State) splitFee(native string, fee uint64) {
	burn := fee * uint64(s.Params.FeeBurnBps) / 10_000
	validators := fee * uint64(s.Params.FeeValidatorBps) / 10_000
	treasury := fee - burn - validators
	s.addBurned(native, burn)
	s.addPool(PoolValidators, native, validators)
	s.addPool(PoolTreasury, native, treasury)
}

func mulDiv(a, b, d uint64) (uint64, bool) {
	if d == 0 {
		return 0, false
	}
	hi, lo := mul64(a, b)
	if hi >= d {
		return 0, false
	}
	q, _ := div128(hi, lo, d)
	return q, true
}

func mul64(a, b uint64) (hi, lo uint64) {
	const mask = 0xffffffff
	ah, al := a>>32, a&mask
	bh, bl := b>>32, b&mask
	t := al * bl
	w0 := t & mask
	k := t >> 32
	t = ah*bl + k
	w1 := t & mask
	w2 := t >> 32
	t = al*bh + w1
	w1 = t & mask
	w2 += t >> 32
	hi = ah*bh + w2
	lo = (w1 << 32) | w0
	return hi, lo
}

func div128(hi, lo, d uint64) (uint64, uint64) {
	if hi >= d {
		return math.MaxUint64, lo
	}
	q := hi / d
	r := hi % d
	for i := 63; i >= 0; i-- {
		q <<= 1
		r <<= 1
		if lo>>uint(i)&1 == 1 {
			r |= 1
		}
		if r >= d {
			r -= d
			q |= 1
		}
	}
	return q, r
}

func (s *State) execute(tx protocol.Tx, now uint64, r *Receipt) error {
	switch tx.Type {
	case protocol.TxTransfer:
		return s.execTransfer(tx, r)
	case protocol.TxBatch:
		return s.execBatch(tx, r)
	case protocol.TxRegisterAsset:
		return s.execRegisterAsset(tx, now, r)
	case protocol.TxCreateEscrow:
		return s.execCreateEscrow(tx, now, r)
	case protocol.TxReleaseEscrow:
		return s.execCloseEscrow(tx, true, r)
	case protocol.TxRefundEscrow:
		return s.execCloseEscrow(tx, false, r)
	case protocol.TxApprove:
		return s.execApprove(tx, r)
	case protocol.TxRevokeAllowance:
		return s.execRevoke(tx, r)
	case protocol.TxTransferFrom:
		return s.execTransferFrom(tx, r)
	case protocol.TxMint:
		return s.execMint(tx, r)
	case protocol.TxBurn:
		return s.execBurn(tx, r)
	default:
		return txErr(CodeInvalidPayload, "unsupported tx type %q", tx.Type)
	}
}

func (s *State) requireAsset(id string) error {
	if _, ok, err := s.Asset(id); err != nil {
		return err
	} else if !ok {
		return txErr(CodeUnknownAsset, "asset %q", id)
	}
	return nil
}

func (s *State) requireAddress(addr string) error {
	return protocol.ValidateAddress(addr, s.Network)
}

func (s *State) execTransfer(tx protocol.Tx, r *Receipt) error {
	asset, err := payloadString(tx.Payload, "asset")
	if err != nil {
		return err
	}
	to, err := payloadString(tx.Payload, "to")
	if err != nil {
		return err
	}
	amount, err := payloadUint(tx.Payload, "amount")
	if err != nil {
		return err
	}
	if memo, ok := tx.Payload["memo"]; ok {
		text, ok := memo.(string)
		if !ok || len(text) > s.Params.MaxMemoBytes {
			return txErr(CodeInvalidPayload, "memo too long")
		}
	}
	if amount == 0 {
		return txErr(CodeInvalidPayload, "amount must be positive")
	}
	if err := s.requireAsset(asset); err != nil {
		return err
	}
	if err := s.requireAddress(to); err != nil {
		return err
	}
	return s.move(tx.Signer, to, asset, amount, r)
}

func (s *State) move(from, to, asset string, amount uint64, r *Receipt) error {
	if err := s.subBalance(from, asset, amount); err != nil {
		return err
	}
	s.ensureAccount(to)
	s.addBalance(to, asset, amount)
	r.Events = append(r.Events, Event{Type: "transfer", Attrs: map[string]string{
		"from": from, "to": to, "asset": asset, "amount": fmt.Sprint(amount),
	}})
	return nil
}

func (s *State) execBatch(tx protocol.Tx, r *Receipt) error {
	raw, ok := tx.Payload["items"]
	if !ok {
		return txErr(CodeInvalidPayload, "items required")
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return txErr(CodeInvalidPayload, "items must be a non-empty array")
	}
	if len(items) > s.Params.MaxBatchItems {
		return txErr(CodeLimitExceeded, "batch above %d items", s.Params.MaxBatchItems)
	}
	for i, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return txErr(CodeInvalidPayload, "item %d", i)
		}
		asset, err := payloadString(item, "asset")
		if err != nil {
			return err
		}
		to, err := payloadString(item, "to")
		if err != nil {
			return err
		}
		amount, err := payloadUint(item, "amount")
		if err != nil {
			return err
		}
		if amount == 0 {
			return txErr(CodeInvalidPayload, "item %d amount", i)
		}
		if err := s.requireAsset(asset); err != nil {
			return err
		}
		if err := s.requireAddress(to); err != nil {
			return err
		}
		if err := s.move(tx.Signer, to, asset, amount, r); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) execRegisterAsset(tx protocol.Tx, now uint64, r *Receipt) error {
	raw, ok := tx.Payload["asset"]
	if !ok {
		return txErr(CodeInvalidPayload, "asset required")
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return txErr(CodeInvalidPayload, "asset must be a map")
	}
	a, err := protocol.AssetFromCBOR(m)
	if err != nil {
		return wrapCode(CodeInvalidPayload, err)
	}
	if a.Class == "native" {
		return txErr(CodeUnauthorized, "native asset is genesis-defined")
	}
	a.Issuer = tx.Signer
	a.Registered = now
	if a.Metadata == nil {
		a.Metadata = map[string]string{}
	}
	if err := a.Validate(s.Network); err != nil {
		return wrapCode(CodeInvalidPayload, err)
	}
	if _, exists, _ := s.Asset(a.ID); exists {
		return txErr(CodeAssetExists, "asset %q", a.ID)
	}
	b, err := protocol.Encode(a.ToCBOR())
	if err != nil {
		return err
	}
	s.set(fmt.Sprintf(kAsset, a.ID), b)
	s.setUint(fmt.Sprintf(kSupply, a.ID), 0)
	r.Events = append(r.Events, Event{Type: "asset_registered", Attrs: map[string]string{"asset": a.ID}})
	return nil
}

func escrowID(signer, asset string, amount, timeout, nonce uint64, beneficiary string) string {
	body, _ := protocol.Encode(map[string]any{
		"dom": "arne-escrow-v1", "signer": signer, "nonce": nonce,
		"asset": asset, "amount": amount, "timeout": timeout, "beneficiary": beneficiary,
	})
	sum := protocol.HashHex(body)
	return "esc_" + sum[:32]
}

func (s *State) execCreateEscrow(tx protocol.Tx, now uint64, r *Receipt) error {
	asset, err := payloadString(tx.Payload, "asset")
	if err != nil {
		return err
	}
	beneficiary, err := payloadString(tx.Payload, "beneficiary")
	if err != nil {
		return err
	}
	arbiter, err := payloadString(tx.Payload, "arbiter")
	if err != nil {
		return err
	}
	amount, err := payloadUint(tx.Payload, "amount")
	if err != nil {
		return err
	}
	timeout, err := payloadUint(tx.Payload, "timeout")
	if err != nil {
		return err
	}
	if amount == 0 {
		return txErr(CodeInvalidPayload, "amount must be positive")
	}
	if timeout <= now {
		return txErr(CodeInvalidPayload, "timeout must be in the future")
	}
	if err := s.requireAsset(asset); err != nil {
		return err
	}
	for _, a := range []string{beneficiary, arbiter} {
		if err := s.requireAddress(a); err != nil {
			return err
		}
	}
	id := escrowID(tx.Signer, asset, amount, timeout, tx.Nonce, beneficiary)
	if err := s.subBalance(tx.Signer, asset, amount); err != nil {
		return err
	}
	k := fmt.Sprintf(kEscrowAmt, asset)
	s.setUint(k, s.getUint(k)+amount)
	e := protocol.Escrow{
		ID: id, Payer: tx.Signer, Beneficiary: beneficiary, Arbiter: arbiter,
		Asset: asset, Amount: amount, Timeout: timeout, Status: protocol.EscrowOpen, CreatedAt: now,
	}
	if err := s.putEscrow(e); err != nil {
		return err
	}
	r.Events = append(r.Events, Event{Type: "escrow_created", Attrs: map[string]string{
		"id": id, "asset": asset, "amount": fmt.Sprint(amount),
	}})
	return nil
}

func (s *State) execCloseEscrow(tx protocol.Tx, release bool, r *Receipt) error {
	id, err := payloadString(tx.Payload, "id")
	if err != nil {
		return err
	}
	e, ok, err := s.Escrow(id)
	if err != nil {
		return err
	}
	if !ok {
		return txErr(CodeEscrowNotFound, "escrow %q", id)
	}
	if e.Status != protocol.EscrowOpen {
		return txErr(CodeEscrowClosed, "escrow %q is %s", id, e.Status)
	}
	if tx.Signer != e.Arbiter && tx.Signer != e.Payer {
		return txErr(CodeUnauthorized, "only payer or arbiter may close")
	}
	k := fmt.Sprintf(kEscrowAmt, e.Asset)
	s.setUint(k, s.getUint(k)-e.Amount)
	if release {
		e.Status = protocol.EscrowReleased
		s.ensureAccount(e.Beneficiary)
		s.addBalance(e.Beneficiary, e.Asset, e.Amount)
	} else {
		e.Status = protocol.EscrowRefunded
		s.addBalance(e.Payer, e.Asset, e.Amount)
	}
	if err := s.putEscrow(e); err != nil {
		return err
	}
	r.Events = append(r.Events, Event{Type: "escrow_" + e.Status, Attrs: map[string]string{"id": id}})
	return nil
}

func (s *State) execApprove(tx protocol.Tx, r *Receipt) error {
	spender, err := payloadString(tx.Payload, "spender")
	if err != nil {
		return err
	}
	asset, err := payloadString(tx.Payload, "asset")
	if err != nil {
		return err
	}
	amount, err := payloadUint(tx.Payload, "amount")
	if err != nil {
		return err
	}
	expires, err := payloadUint(tx.Payload, "expires_at")
	if err != nil && !errors.Is(err, ErrMissingField) {
		return err
	}
	if err := s.requireAsset(asset); err != nil {
		return err
	}
	if err := s.requireAddress(spender); err != nil {
		return err
	}
	if amount > 0 && s.Balance(tx.Signer, asset) == 0 {
		return txErr(CodeInsufficientFunds, "owner holds no %s", asset)
	}
	s.setUint(fmt.Sprintf(kAllowance, tx.Signer, spender, asset), amount)
	s.setUint(fmt.Sprintf(kAllowExp, tx.Signer, spender, asset), expires)
	r.Events = append(r.Events, Event{Type: "allowance_set", Attrs: map[string]string{
		"owner": tx.Signer, "spender": spender, "asset": asset, "amount": fmt.Sprint(amount),
	}})
	return nil
}

func (s *State) execRevoke(tx protocol.Tx, r *Receipt) error {
	spender, err := payloadString(tx.Payload, "spender")
	if err != nil {
		return err
	}
	asset, err := payloadString(tx.Payload, "asset")
	if err != nil {
		return err
	}
	s.setUint(fmt.Sprintf(kAllowance, tx.Signer, spender, asset), 0)
	s.setUint(fmt.Sprintf(kAllowExp, tx.Signer, spender, asset), 0)
	r.Events = append(r.Events, Event{Type: "allowance_revoked", Attrs: map[string]string{
		"owner": tx.Signer, "spender": spender, "asset": asset,
	}})
	return nil
}

func (s *State) execTransferFrom(tx protocol.Tx, r *Receipt) error {
	owner, err := payloadString(tx.Payload, "from")
	if err != nil {
		return err
	}
	to, err := payloadString(tx.Payload, "to")
	if err != nil {
		return err
	}
	asset, err := payloadString(tx.Payload, "asset")
	if err != nil {
		return err
	}
	amount, err := payloadUint(tx.Payload, "amount")
	if err != nil {
		return err
	}
	if amount == 0 {
		return txErr(CodeInvalidPayload, "amount must be positive")
	}
	if err := s.requireAsset(asset); err != nil {
		return err
	}
	if err := s.requireAddress(to); err != nil {
		return err
	}
	remaining := s.Allowance(owner, tx.Signer, asset)
	if remaining < amount {
		return txErr(CodeAllowanceExceeded, "allowance %d below %d", remaining, amount)
	}
	if exp := s.AllowanceExpiry(owner, tx.Signer, asset); exp != 0 && s.BlockTime() > exp {
		return txErr(CodeAllowanceExceeded, "allowance expired")
	}
	if err := s.move(owner, to, asset, amount, r); err != nil {
		return err
	}
	s.setUint(fmt.Sprintf(kAllowance, owner, tx.Signer, asset), remaining-amount)
	return nil
}

func (s *State) execMint(tx protocol.Tx, r *Receipt) error {
	asset, err := payloadString(tx.Payload, "asset")
	if err != nil {
		return err
	}
	to, err := payloadString(tx.Payload, "to")
	if err != nil {
		return err
	}
	amount, err := payloadUint(tx.Payload, "amount")
	if err != nil {
		return err
	}
	if amount == 0 {
		return txErr(CodeInvalidPayload, "amount must be positive")
	}
	a, ok, err := s.Asset(asset)
	if err != nil {
		return err
	}
	if !ok {
		return txErr(CodeUnknownAsset, "asset %q", asset)
	}
	if tx.Signer != a.Issuer {
		return txErr(CodeUnauthorized, "only the issuer may mint")
	}
	if a.Policy == "fixed" {
		return txErr(CodeSupplyLimit, "asset %q is fixed-supply", asset)
	}
	supply := s.Supply(asset)
	if a.MaxSupply > 0 && amount > a.MaxSupply-supply {
		return txErr(CodeSupplyLimit, "mint exceeds cap")
	}
	if err := s.requireAddress(to); err != nil {
		return err
	}
	s.setUint(fmt.Sprintf(kSupply, asset), supply+amount)
	s.ensureAccount(to)
	s.addBalance(to, asset, amount)
	r.Events = append(r.Events, Event{Type: "mint", Attrs: map[string]string{
		"asset": asset, "to": to, "amount": fmt.Sprint(amount),
	}})
	return nil
}

func (s *State) execBurn(tx protocol.Tx, r *Receipt) error {
	asset, err := payloadString(tx.Payload, "asset")
	if err != nil {
		return err
	}
	amount, err := payloadUint(tx.Payload, "amount")
	if err != nil {
		return err
	}
	if amount == 0 {
		return txErr(CodeInvalidPayload, "amount must be positive")
	}
	a, ok, err := s.Asset(asset)
	if err != nil {
		return err
	}
	if !ok {
		return txErr(CodeUnknownAsset, "asset %q", asset)
	}
	if a.Policy != "burnable" {
		return txErr(CodeUnauthorized, "asset %q does not allow burning", asset)
	}
	if err := s.subBalance(tx.Signer, asset, amount); err != nil {
		return err
	}
	s.addBurned(asset, amount)
	r.Events = append(r.Events, Event{Type: "burn", Attrs: map[string]string{
		"asset": asset, "amount": fmt.Sprint(amount),
	}})
	return nil
}

var ErrMissingField = errors.New("missing field")

func payloadString(m map[string]any, key string) (string, error) {
	v, ok := m[key]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrMissingField, key)
	}
	s, ok := v.(string)
	if !ok {
		return "", txErr(CodeInvalidPayload, "%s must be text", key)
	}
	return s, nil
}

func payloadUint(m map[string]any, key string) (uint64, error) {
	v, ok := m[key]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrMissingField, key)
	}
	u, ok := v.(uint64)
	if !ok {
		return 0, txErr(CodeInvalidPayload, "%s must be unsigned", key)
	}
	return u, nil
}

// MustNow is a helper for callers that need the wall clock in seconds.
func MustNow() uint64 { return uint64(time.Now().Unix()) }

// CheckTxWindow is how far ahead of the current nonce a mempool transaction may
// be. It lets clients submit sequential transactions before earlier ones land.
const CheckTxWindow = 64

// CheckTx validates a transaction for mempool admission. It does not change
// state; nonce may be within CheckTxWindow ahead of the account nonce.
func (s *State) CheckTx(tx protocol.Tx, now uint64) error {
	s.Begin()
	defer s.Rollback()
	cur := s.getUint(fmt.Sprintf(kNonce, tx.Signer))
	if tx.Nonce < cur || tx.Nonce > cur+CheckTxWindow {
		return txErr(CodeBadNonce, "expected between %d and %d, got %d", cur, cur+CheckTxWindow, tx.Nonce)
	}
	s.setUint(fmt.Sprintf(kNonce, tx.Signer), tx.Nonce)
	r := Receipt{}
	if _, err := s.apply(tx, now, &r); err != nil {
		return err
	}
	return nil
}

// SimulateTx applies a transaction and always rolls it back.
func (s *State) SimulateTx(tx protocol.Tx, now, height uint64) Receipt {
	hash, _ := tx.Hash()
	r := Receipt{Hash: hash, Height: height, FeeAsset: tx.FeeAsset}
	s.Begin()
	fee, err := s.apply(tx, now, &r)
	s.Rollback()
	if err != nil {
		r.Success = false
		r.Code = CodeOf(err)
		r.Error = err.Error()
		return r
	}
	r.Success = true
	r.Fee = fee
	return r
}
