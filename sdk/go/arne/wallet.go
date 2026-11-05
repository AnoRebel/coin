package arne

import (
	"context"
	"time"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Wallet is a signing identity bound to one network. Build fills sensible
// defaults for fee and lifetime, so a caller normally only sets a payload.
type Wallet struct {
	*KeyPair
	FeeAsset    string
	MaxFee      uint64
	PriorityFee uint64
	TTL         time.Duration // zero means one hour
}

// NewWallet generates a 24-word mnemonic and its first account.
func NewWallet(network string) (*Wallet, string, error) {
	kp, mnemonic, err := NewKeyPair(network)
	if err != nil {
		return nil, "", err
	}
	return &Wallet{KeyPair: kp}, mnemonic, nil
}

// WalletFromMnemonic restores account index at m/44'/9999'/0'/0/index.
func WalletFromMnemonic(mnemonic, passphrase, network string, index uint32) (*Wallet, error) {
	kp, err := KeyPairFromMnemonic(mnemonic, passphrase, 0, 0, index, network)
	if err != nil {
		return nil, err
	}
	return &Wallet{KeyPair: kp}, nil
}

// Build reads the account nonce and returns a signed transaction.
func (w *Wallet) Build(ctx context.Context, c *Client, typ string, payload map[string]any) (Tx, error) {
	account, err := c.Account(ctx, w.Address)
	if err != nil {
		return Tx{}, err
	}
	return w.BuildNonce(typ, payload, account.Nonce, time.Now())
}

// BuildNonce signs a transaction with an explicit nonce, for offline use or
// when several transactions are queued from one account.
func (w *Wallet) BuildNonce(typ string, payload map[string]any, nonce uint64, now time.Time) (Tx, error) {
	chain, err := ChainID(w.Network)
	if err != nil {
		return Tx{}, err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	ttl := w.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	feeAsset := w.FeeAsset
	if feeAsset == "" {
		feeAsset = NativeAsset(w.Network)
	}
	maxFee := w.MaxFee
	if maxFee == 0 {
		maxFee = 10_000
	}
	tx := Tx{
		Version: protocol.CurrentTxVersion, Network: w.Network, Chain: chain,
		Type: typ, Signer: w.Address, Nonce: nonce,
		CreatedAt: uint64(now.Unix()), ExpiresAt: uint64(now.Add(ttl).Unix()),
		FeeAsset: feeAsset, MaxFee: maxFee, PriorityFee: w.PriorityFee,
		Payload: payload,
	}
	if err := tx.Sign(w.KeyPair); err != nil {
		return Tx{}, err
	}
	return tx, nil
}

// Send signs and submits a transaction, returning its hash.
func (w *Wallet) Send(ctx context.Context, c *Client, typ string, payload map[string]any) (string, error) {
	tx, err := w.Build(ctx, c, typ, payload)
	if err != nil {
		return "", err
	}
	return c.SubmitTx(ctx, tx)
}

// SendAndWait submits and then polls until the receipt exists or ctx expires.
func (w *Wallet) SendAndWait(ctx context.Context, c *Client, typ string, payload map[string]any) (Receipt, error) {
	hash, err := w.Send(ctx, c, typ, payload)
	if err != nil {
		return Receipt{}, err
	}
	return c.WaitReceipt(ctx, hash)
}

// Simulate dry-runs a signed transaction without submitting it.
func (w *Wallet) Simulate(ctx context.Context, c *Client, typ string, payload map[string]any) (Receipt, error) {
	tx, err := w.Build(ctx, c, typ, payload)
	if err != nil {
		return Receipt{}, err
	}
	raw, err := Encode(tx.ToCBOR())
	if err != nil {
		return Receipt{}, err
	}
	return c.Simulate(ctx, raw)
}
