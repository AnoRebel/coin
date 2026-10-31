package main

import (
	"errors"
	"fmt"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

type transferInfo struct {
	Hash       string `json:"hash"`
	From       string `json:"from"`
	To         string `json:"to"`
	Asset      string `json:"asset"`
	Amount     uint64 `json:"amount"`
	RefundOf   string `json:"refund_of"`
	Refunded   uint64 `json:"refunded"`
	Refundable uint64 `json:"refundable"`
}

func fetchTransfer(network, hash string) (transferInfo, error) {
	var t transferInfo
	if !protocol.ValidTxHash(hash) {
		return t, errors.New("--refund-of must be a 64-character lowercase hex transaction hash")
	}
	var status struct {
		Network string `json:"network"`
	}
	if err := getJSON("/v1/status", &status); err != nil {
		return t, err
	}
	if status.Network != network {
		return t, fmt.Errorf("API network %s conflicts with %s", status.Network, network)
	}
	if err := getJSON("/v1/transfers/"+hash, &t); err != nil {
		return t, err
	}
	if t.Hash != hash || t.From == "" || t.To == "" {
		return t, errors.New("API returned a malformed transfer")
	}
	return t, nil
}

// resolveRefund fills a refund from the original transfer and refuses
// conflicting explicit values. The ledger repeats every check.
func (r *paymentRequest) resolveRefund() error {
	if r.refundOf == "" {
		return nil
	}
	if r.intent != "" {
		return errors.New("use --intent or --refund-of, not both")
	}
	t, err := fetchTransfer(r.network, r.refundOf)
	if err != nil {
		return err
	}
	if t.RefundOf != "" {
		return errors.New("cannot refund a refund")
	}
	if t.Refundable == 0 {
		return errors.New("transfer is already fully refunded")
	}
	if (r.to != "" && r.to != t.From) || (r.asset != "" && r.asset != t.Asset) {
		return errors.New("recipient or asset conflicts with --refund-of")
	}
	info, err := fetchAsset(t.Asset)
	if err != nil {
		return err
	}
	if r.amount != "" {
		units, err := parseAmount(r.amount, info.Decimals)
		if err != nil {
			return err
		}
		if units > t.Refundable {
			return fmt.Errorf("refund exceeds remaining %s", formatAmount(t.Refundable, info.Decimals))
		}
	}
	r.to, r.asset = t.From, t.Asset
	if r.amount == "" {
		r.amount = shortAmount(t.Refundable, info.Decimals)
	}
	return nil
}
