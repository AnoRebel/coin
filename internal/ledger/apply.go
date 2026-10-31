package ledger

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	arnememo "github.com/anorebel/anorebelcoin/internal/memo"
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
	CodeMempoolConflict   Code = "MEMPOOL_CONFLICT"
	CodeFeeTooLow         Code = "FEE_TOO_LOW"
	CodeNoFeeRoute        Code = "NO_FEE_ROUTE"
	CodeInsufficientFunds Code = "INSUFFICIENT_FUNDS"
	CodeUnknownAsset      Code = "UNKNOWN_ASSET"
	CodeAssetExists       Code = "ASSET_EXISTS"
	CodeUnauthorized      Code = "UNAUTHORIZED"
	CodeSupplyLimit       Code = "SUPPLY_LIMIT"
	CodeEscrowNotFound    Code = "ESCROW_NOT_FOUND"
	CodeEscrowClosed      Code = "ESCROW_CLOSED"
	CodeIntentNotFound    Code = "INTENT_NOT_FOUND"
	CodeIntentPaid        Code = "INTENT_PAID"
	CodeIntentExpired     Code = "INTENT_EXPIRED"
	CodeIntentExists      Code = "INTENT_EXISTS"
	CodeIntentMismatch    Code = "INTENT_MISMATCH"
	CodeRefundNotFound    Code = "REFUND_NOT_FOUND"
	CodeRefundMismatch    Code = "REFUND_MISMATCH"
	CodeRefundExceeded    Code = "REFUND_EXCEEDED"
	CodeSubNotFound       Code = "SUBSCRIPTION_NOT_FOUND"
	CodeSubExists         Code = "SUBSCRIPTION_EXISTS"
	CodeSubClosed         Code = "SUBSCRIPTION_CLOSED"
	CodeSubExpired        Code = "SUBSCRIPTION_EXPIRED"
	CodeSubLimit          Code = "SUBSCRIPTION_LIMIT"
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
	var maxArceFee uint64
	if raw, ok := tx.Payload["max_arce_fee"]; ok {
		v, ok := raw.(uint64)
		if !ok || v == 0 {
			return 0, txErr(CodeInvalidPayload, "max_arce_fee must be a positive integer")
		}
		maxArceFee = v
	}
	native := protocol.NativeAsset(s.Network)
	if tx.FeeAsset == native {
		if maxArceFee > 0 && fee > maxArceFee {
			return 0, txErr(CodeLimitExceeded, "fee %d above max_arce_fee %d", fee, maxArceFee)
		}
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
	if maxArceFee > 0 && arceFee > maxArceFee {
		return 0, txErr(CodeLimitExceeded, "converted fee %d above max_arce_fee %d", arceFee, maxArceFee)
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
		return s.execTransfer(tx, now, r)
	case protocol.TxCreateIntent:
		return s.execCreateIntent(tx, now, r)
	case protocol.TxCreateSub:
		return s.execCreateSubscription(tx, now, r)
	case protocol.TxChargeSub:
		return s.execChargeSubscription(tx, now, r)
	case protocol.TxCancelSub:
		return s.execCancelSubscription(tx, now, r)
	case protocol.TxBatch:
		return s.execBatch(tx, r)
	case protocol.TxRegisterAsset:
		return s.execRegisterAsset(tx, now, r)
	case protocol.TxCreateEscrow:
		return s.execCreateEscrow(tx, now, r)
	case protocol.TxReleaseEscrow:
		return s.execCloseEscrow(tx, true, now, r)
	case protocol.TxRefundEscrow:
		return s.execCloseEscrow(tx, false, now, r)
	case protocol.TxApprove:
		return s.execApprove(tx, r)
	case protocol.TxRevokeAllowance:
		return s.execRevoke(tx, r)
	case protocol.TxTransferFrom:
		return s.execTransferFrom(tx, now, r)
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

func (s *State) execTransfer(tx protocol.Tx, now uint64, r *Receipt) error {
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
	refundOf := ""
	if raw, ok := tx.Payload["refund_of"]; ok {
		hash, ok := raw.(string)
		if !ok || !protocol.ValidTxHash(hash) {
			return txErr(CodeInvalidPayload, "refund_of must be a 64-character lowercase hex transaction hash")
		}
		refundOf = hash
	}
	var intent *protocol.Intent
	if raw, ok := tx.Payload["intent"]; ok {
		if refundOf != "" {
			return txErr(CodeInvalidPayload, "use intent or refund_of, not both")
		}
		id, ok := raw.(string)
		if !ok || id == "" {
			return txErr(CodeInvalidPayload, "intent must be text")
		}
		in, err := s.checkIntent(id, to, asset, amount, now)
		if err != nil {
			return err
		}
		intent = &in
	}
	if plain, ok := tx.Payload["memo"]; ok {
		text, ok := plain.(string)
		if !ok || len(text) > s.Params.MaxMemoBytes {
			return txErr(CodeInvalidPayload, "memo too long")
		}
		if _, both := tx.Payload["memo_enc"]; both {
			return txErr(CodeInvalidPayload, "use memo or memo_enc, not both")
		}
	}
	if raw, ok := tx.Payload["memo_enc"]; ok {
		sealed, ok := raw.([]byte)
		if !ok {
			return txErr(CodeInvalidPayload, "memo_enc must be bytes")
		}
		if err := arnememo.Validate(sealed); err != nil {
			return txErr(CodeInvalidPayload, "memo_enc: %v", err)
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
	remaining := uint64(0)
	if refundOf != "" {
		remaining, err = s.checkRefund(tx, refundOf, to, asset, amount)
		if err != nil {
			return err
		}
	}
	if err := s.move(tx.Signer, to, asset, amount, r); err != nil {
		return err
	}
	if err := s.putTransfer(r.Hash, protocol.Transfer{
		From: tx.Signer, To: to, Asset: asset, Amount: amount, RefundOf: refundOf,
	}); err != nil {
		return err
	}
	if refundOf != "" {
		s.setUint(fmt.Sprintf(kRefunded, refundOf), s.Refunded(refundOf)+amount)
		r.Events = append(r.Events, Event{Type: "refund", Attrs: map[string]string{
			"refund_of": refundOf, "from": tx.Signer, "to": to, "asset": asset,
			"amount": fmt.Sprint(amount), "remaining": fmt.Sprint(remaining),
		}})
	}
	if intent != nil {
		intent.Status = protocol.IntentPaid
		intent.Payer, intent.TxHash, intent.PaidAt = tx.Signer, r.Hash, now
		if err := s.putIntent(*intent); err != nil {
			return err
		}
		r.Events = append(r.Events, Event{Type: "intent_paid", Attrs: map[string]string{
			"id": intent.ID, "reference": intent.Reference, "payer": tx.Signer,
		}})
	}
	return nil
}

func (s *State) execCreateIntent(tx protocol.Tx, now uint64, r *Receipt) error {
	reference, err := payloadString(tx.Payload, "reference")
	if err != nil {
		return err
	}
	if !protocol.ValidIntentReference(reference) {
		return txErr(CodeInvalidPayload, "reference must be 1-64 letters, digits, _ or -")
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
	var expiresAt uint64
	if raw, ok := tx.Payload["expires_at"]; ok {
		expiresAt, ok = raw.(uint64)
		if !ok {
			return txErr(CodeInvalidPayload, "expires_at must be unsigned")
		}
		if expiresAt <= now {
			return txErr(CodeInvalidPayload, "expires_at must be in the future")
		}
	}
	var memo string
	if raw, ok := tx.Payload["memo"]; ok {
		memo, ok = raw.(string)
		if !ok || len(memo) > s.Params.MaxMemoBytes || strings.ContainsAny(memo, "\x00\r\n") {
			return txErr(CodeInvalidPayload, "intent memo must fit a payment URI (max 256 bytes; no NUL, CR, or LF)")
		}
	}
	id := protocol.IntentID(s.Network, tx.Signer, reference)
	if _, exists, err := s.Intent(id); err != nil {
		return err
	} else if exists {
		return txErr(CodeIntentExists, "intent %s already exists", id)
	}
	in := protocol.Intent{
		ID: id, Payee: tx.Signer, Reference: reference, Asset: asset, Amount: amount,
		ExpiresAt: expiresAt, Memo: memo, Status: protocol.IntentOpen, CreatedAt: now,
	}
	if err := s.putIntent(in); err != nil {
		return err
	}
	r.Events = append(r.Events, Event{Type: "intent_created", Attrs: map[string]string{
		"id": id, "reference": reference, "payee": tx.Signer, "asset": asset,
		"amount": fmt.Sprint(amount),
	}})
	return nil
}

func (s *State) checkIntent(id, payee, asset string, amount, now uint64) (protocol.Intent, error) {
	in, ok, err := s.Intent(id)
	if err != nil {
		return in, err
	}
	if !ok {
		return in, txErr(CodeIntentNotFound, "intent %s", id)
	}
	if in.Status == protocol.IntentPaid {
		return in, txErr(CodeIntentPaid, "intent %s already paid", id)
	}
	if in.ExpiresAt != 0 && now > in.ExpiresAt {
		return in, txErr(CodeIntentExpired, "intent %s expired at %d", id, in.ExpiresAt)
	}
	if in.Payee != payee || in.Asset != asset || in.Amount != amount {
		return in, txErr(CodeIntentMismatch, "payment does not match intent %s", id)
	}
	return in, nil
}

func (s *State) execCreateSubscription(tx protocol.Tx, now uint64, r *Receipt) error {
	payee, err := payloadString(tx.Payload, "payee")
	if err != nil {
		return err
	}
	asset, err := payloadString(tx.Payload, "asset")
	if err != nil {
		return err
	}
	perPeriod, err := payloadUint(tx.Payload, "amount_per_period")
	if err != nil {
		return err
	}
	period, err := payloadUint(tx.Payload, "period")
	if err != nil {
		return err
	}
	totalCap, err := payloadUint(tx.Payload, "total_cap")
	if err != nil {
		return err
	}
	if perPeriod == 0 || period == 0 || totalCap == 0 {
		return txErr(CodeInvalidPayload, "amount_per_period, period, and total_cap must be positive")
	}
	if payee == tx.Signer {
		return txErr(CodeInvalidPayload, "payee must differ from payer")
	}
	if err := s.requireAsset(asset); err != nil {
		return err
	}
	if err := s.requireAddress(payee); err != nil {
		return err
	}
	var expiresAt uint64
	if raw, ok := tx.Payload["expires_at"]; ok {
		expiresAt, ok = raw.(uint64)
		if !ok {
			return txErr(CodeInvalidPayload, "expires_at must be unsigned")
		}
		if expiresAt <= now {
			return txErr(CodeInvalidPayload, "expires_at must be in the future")
		}
	}
	id := protocol.SubscriptionID(s.Network, tx.Signer, payee, asset, tx.Nonce)
	if _, exists, err := s.Subscription(id); err != nil {
		return err
	} else if exists {
		return txErr(CodeSubExists, "subscription %s already exists", id)
	}
	sub := protocol.Subscription{
		ID: id, Payer: tx.Signer, Payee: payee, Asset: asset,
		AmountPerPeriod: perPeriod, Period: period, TotalCap: totalCap,
		PeriodStart: now, ExpiresAt: expiresAt, Status: protocol.SubscriptionActive, CreatedAt: now,
	}
	if err := s.putSubscription(sub); err != nil {
		return err
	}
	r.Events = append(r.Events, Event{Type: "subscription_created", Attrs: map[string]string{
		"id": id, "payer": tx.Signer, "payee": payee, "asset": asset,
		"amount_per_period": fmt.Sprint(perPeriod), "period": fmt.Sprint(period),
		"total_cap": fmt.Sprint(totalCap), "expires_at": fmt.Sprint(expiresAt),
	}})
	return nil
}

func (s *State) execChargeSubscription(tx protocol.Tx, now uint64, r *Receipt) error {
	id, err := payloadString(tx.Payload, "id")
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
	sub, ok, err := s.Subscription(id)
	if err != nil {
		return err
	}
	if !ok {
		return txErr(CodeSubNotFound, "subscription %s", id)
	}
	if sub.Status != protocol.SubscriptionActive {
		return txErr(CodeSubClosed, "subscription %s is %s", id, sub.Status)
	}
	if tx.Signer != sub.Payee {
		return txErr(CodeUnauthorized, "only the subscription payee may charge")
	}
	if sub.ExpiresAt != 0 && now > sub.ExpiresAt {
		return txErr(CodeSubExpired, "subscription %s expired at %d", id, sub.ExpiresAt)
	}
	// Advances reset the per-period counter; unused periods do not accumulate.
	if now >= sub.PeriodStart && now-sub.PeriodStart >= sub.Period {
		periods := (now - sub.PeriodStart) / sub.Period
		sub.PeriodStart += periods * sub.Period
		sub.ChargedPeriod = 0
	}
	if sub.ChargedPeriod > sub.AmountPerPeriod || sub.ChargedTotal > sub.TotalCap {
		return txErr(CodeSubLimit, "subscription %s totals are inconsistent", id)
	}
	if amount > sub.AmountPerPeriod-sub.ChargedPeriod || amount > sub.TotalCap-sub.ChargedTotal {
		return txErr(CodeSubLimit, "charge exceeds subscription %s limits", id)
	}
	if err := s.move(sub.Payer, sub.Payee, sub.Asset, amount, r); err != nil {
		return err
	}
	sub.ChargedPeriod += amount
	sub.ChargedTotal += amount
	if err := s.putSubscription(sub); err != nil {
		return err
	}
	r.Events = append(r.Events, Event{Type: "subscription_charged", Attrs: map[string]string{
		"id": id, "payer": sub.Payer, "payee": sub.Payee, "asset": sub.Asset,
		"amount": fmt.Sprint(amount), "charged_period": fmt.Sprint(sub.ChargedPeriod),
		"charged_total":   fmt.Sprint(sub.ChargedTotal),
		"remaining_total": fmt.Sprint(sub.TotalCap - sub.ChargedTotal),
		"period_start":    fmt.Sprint(sub.PeriodStart),
	}})
	return nil
}

func (s *State) execCancelSubscription(tx protocol.Tx, now uint64, r *Receipt) error {
	id, err := payloadString(tx.Payload, "id")
	if err != nil {
		return err
	}
	sub, ok, err := s.Subscription(id)
	if err != nil {
		return err
	}
	if !ok {
		return txErr(CodeSubNotFound, "subscription %s", id)
	}
	if sub.Status != protocol.SubscriptionActive {
		return txErr(CodeSubClosed, "subscription %s is %s", id, sub.Status)
	}
	if tx.Signer != sub.Payer {
		return txErr(CodeUnauthorized, "only the subscription payer may cancel")
	}
	sub.Status = protocol.SubscriptionCancelled
	sub.CancelledAt = now
	if err := s.putSubscription(sub); err != nil {
		return err
	}
	r.Events = append(r.Events, Event{Type: "subscription_cancelled", Attrs: map[string]string{
		"id": id, "payer": sub.Payer, "payee": sub.Payee,
	}})
	return nil
}

// checkRefund validates a refund against the original transfer index and
// returns the amount still refundable after this refund.
func (s *State) checkRefund(tx protocol.Tx, refundOf, to, asset string, amount uint64) (uint64, error) {
	original, ok, err := s.Transfer(refundOf)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, txErr(CodeRefundNotFound, "original transfer %s", refundOf)
	}
	if original.RefundOf != "" {
		return 0, txErr(CodeRefundMismatch, "cannot refund a refund")
	}
	if tx.Signer != original.To {
		return 0, txErr(CodeUnauthorized, "only the original payee may refund")
	}
	if to != original.From || asset != original.Asset {
		return 0, txErr(CodeRefundMismatch, "refund must return %s to %s", original.Asset, original.From)
	}
	refunded := s.Refunded(refundOf)
	if refunded > original.Amount {
		return 0, txErr(CodeRefundExceeded, "refund total exceeds original %d", original.Amount)
	}
	remaining := original.Amount - refunded
	if amount > remaining {
		return 0, txErr(CodeRefundExceeded, "refund %d exceeds remaining %d", amount, remaining)
	}
	return remaining - amount, nil
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
	mode := "atomic"
	if raw, ok := tx.Payload["mode"]; ok {
		m, ok := raw.(string)
		if !ok || (m != "atomic" && m != "best_effort") {
			return txErr(CodeInvalidPayload, "mode must be atomic or best_effort")
		}
		mode = m
	}
	for i, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			err := txErr(CodeInvalidPayload, "item %d", i)
			if mode == "atomic" {
				return err
			}
			s.batchResult(r, i, err)
			continue
		}
		err := s.execBatchItem(tx.Signer, item, r)
		if err != nil {
			if mode == "atomic" {
				return err
			}
			s.batchResult(r, i, err)
			continue
		}
		r.Events = append(r.Events, Event{Type: "batch_item", Attrs: map[string]string{
			"index": fmt.Sprint(i), "success": "true",
		}})
	}
	return nil
}

// execBatchItem validates one item before moving value, so a failed item leaves
// no partial state for a best-effort batch to continue from.
func (s *State) execBatchItem(signer string, item map[string]any, r *Receipt) error {
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
		return txErr(CodeInvalidPayload, "amount must be positive")
	}
	if err := s.requireAsset(asset); err != nil {
		return err
	}
	if err := s.requireAddress(to); err != nil {
		return err
	}
	return s.move(signer, to, asset, amount, r)
}

func (s *State) batchResult(r *Receipt, index int, err error) {
	r.Events = append(r.Events, Event{Type: "batch_item", Attrs: map[string]string{
		"index": fmt.Sprint(index), "success": "false", "code": string(CodeOf(err)),
	}})
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

func (s *State) execCloseEscrow(tx protocol.Tx, release bool, now uint64, r *Receipt) error {
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
	// The payer may release at any time, but may only reclaim after the
	// timeout. The beneficiary may claim after it. The arbiter may always act.
	switch {
	case tx.Signer == e.Arbiter:
	case release && tx.Signer == e.Payer:
	case tx.Signer == e.Beneficiary && now >= e.Timeout:
	case !release && tx.Signer == e.Payer && now >= e.Timeout:
	default:
		return txErr(CodeUnauthorized, "not allowed to close this escrow yet")
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

func (s *State) execTransferFrom(tx protocol.Tx, now uint64, r *Receipt) error {
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
	if exp := s.AllowanceExpiry(owner, tx.Signer, asset); exp != 0 && now > exp {
		return txErr(CodeAllowanceExceeded, "allowance expired")
	}
	if err := s.move(owner, to, asset, amount, r); err != nil {
		return err
	}
	if err := s.putTransfer(r.Hash, protocol.Transfer{
		From: owner, To: to, Asset: asset, Amount: amount,
	}); err != nil {
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
