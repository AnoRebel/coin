package arne

// The payload types below mirror what the ledger accepts for each transaction
// type. They are plain data: the node performs authoritative validation.

// Payment builds a transfer payload. Memo is public on chain; set MemoEnc with
// bytes from the memo package to keep the text private. Intent, RefundOf, and
// MemoEnc are optional; when both Intent and RefundOf are set the node rejects
// the transaction.
type Payment struct {
	To         string
	Asset      string
	Amount     uint64
	Memo       string
	MemoEnc    []byte
	Intent     string
	RefundOf   string
	MaxARCEFee uint64
}

func (p Payment) Payload() map[string]any {
	m := map[string]any{"to": p.To, "asset": p.Asset, "amount": p.Amount}
	if p.Memo != "" {
		m["memo"] = p.Memo
	}
	if len(p.MemoEnc) > 0 {
		m["memo_enc"] = p.MemoEnc
	}
	if p.Intent != "" {
		m["intent"] = p.Intent
	}
	if p.RefundOf != "" {
		m["refund_of"] = p.RefundOf
	}
	if p.MaxARCEFee > 0 {
		m["max_arce_fee"] = p.MaxARCEFee
	}
	return m
}

// EscrowCreate locks payer funds until release or timeout.
type EscrowCreate struct {
	Beneficiary string
	Arbiter     string
	Asset       string
	Amount      uint64
	Timeout     uint64
}

func (e EscrowCreate) Payload() map[string]any {
	return map[string]any{
		"beneficiary": e.Beneficiary, "arbiter": e.Arbiter, "asset": e.Asset,
		"amount": e.Amount, "timeout": e.Timeout,
	}
}

// EscrowClose releases or refunds an escrow. Use TxReleaseEscrow or
// TxRefundEscrow.
type EscrowClose struct {
	ID string
}

func (e EscrowClose) Payload() map[string]any { return map[string]any{"id": e.ID} }

// Allowance authorizes a spender. ExpiresAt is unix seconds; zero never expires.
type Allowance struct {
	Spender   string
	Asset     string
	Amount    uint64
	ExpiresAt uint64
}

func (a Allowance) Payload() map[string]any {
	m := map[string]any{"spender": a.Spender, "asset": a.Asset, "amount": a.Amount}
	if a.ExpiresAt > 0 {
		m["expires_at"] = a.ExpiresAt
	}
	return m
}

// AllowanceRevoke clears an allowance.
type AllowanceRevoke struct {
	Spender string
	Asset   string
}

func (a AllowanceRevoke) Payload() map[string]any {
	return map[string]any{"spender": a.Spender, "asset": a.Asset}
}

// AllowanceTransfer spends an allowance as the spender.
type AllowanceTransfer struct {
	From   string
	To     string
	Asset  string
	Amount uint64
}

func (a AllowanceTransfer) Payload() map[string]any {
	return map[string]any{"from": a.From, "to": a.To, "asset": a.Asset, "amount": a.Amount}
}

// AssetRegister registers a non-native asset. The signer becomes the issuer.
type AssetRegister struct {
	Asset Asset
}

func (a AssetRegister) Payload() map[string]any {
	return map[string]any{"asset": a.Asset.ToCBOR()}
}

// Mint issues units of an asset the signer issued.
type Mint struct {
	Asset  string
	To     string
	Amount uint64
}

func (m Mint) Payload() map[string]any {
	return map[string]any{"asset": m.Asset, "to": m.To, "amount": m.Amount}
}

// Burn destroys units the signer holds.
type Burn struct {
	Asset  string
	Amount uint64
}

func (b Burn) Payload() map[string]any {
	return map[string]any{"asset": b.Asset, "amount": b.Amount}
}

// IntentCreate creates a signed payment request. The ID is derived from the
// payee, network, and reference, so a retry with the same reference conflicts.
type IntentCreate struct {
	Reference string
	Asset     string
	Amount    uint64
	ExpiresAt uint64 // unix seconds; zero never expires
	Memo      string
}

func (i IntentCreate) Payload() map[string]any {
	m := map[string]any{"reference": i.Reference, "asset": i.Asset, "amount": i.Amount}
	if i.ExpiresAt > 0 {
		m["expires_at"] = i.ExpiresAt
	}
	if i.Memo != "" {
		m["memo"] = i.Memo
	}
	return m
}

// SubscriptionCreate authorizes the payee to charge up to AmountPerPeriod each
// Period seconds, capped by TotalCap.
type SubscriptionCreate struct {
	Payee           string
	Asset           string
	AmountPerPeriod uint64
	Period          uint64
	TotalCap        uint64
	ExpiresAt       uint64 // unix seconds; zero never expires
}

func (s SubscriptionCreate) Payload() map[string]any {
	m := map[string]any{
		"payee": s.Payee, "asset": s.Asset, "amount_per_period": s.AmountPerPeriod,
		"period": s.Period, "total_cap": s.TotalCap,
	}
	if s.ExpiresAt > 0 {
		m["expires_at"] = s.ExpiresAt
	}
	return m
}

// SubscriptionCharge is signed by the payee.
type SubscriptionCharge struct {
	ID     string
	Amount uint64
}

func (s SubscriptionCharge) Payload() map[string]any {
	return map[string]any{"id": s.ID, "amount": s.Amount}
}

// SubscriptionCancel is signed by the payer.
type SubscriptionCancel struct {
	ID string
}

func (s SubscriptionCancel) Payload() map[string]any { return map[string]any{"id": s.ID} }

// BatchItem is one transfer inside a batch.
type BatchItem struct {
	To     string
	Asset  string
	Amount uint64
}

// Batch sends several transfers in one transaction. Atomic batches fail as a
// whole; best-effort batches record per-item results.
type Batch struct {
	Items      []BatchItem
	BestEffort bool
}

func (b Batch) Payload() map[string]any {
	items := make([]any, len(b.Items))
	for i, item := range b.Items {
		items[i] = map[string]any{"to": item.To, "asset": item.Asset, "amount": item.Amount}
	}
	mode := "atomic"
	if b.BestEffort {
		mode = "best_effort"
	}
	return map[string]any{"items": items, "mode": mode}
}
