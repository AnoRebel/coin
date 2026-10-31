package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

func fetchSubscription(network, id string) (protocol.Subscription, error) {
	var sub protocol.Subscription
	if b, err := hex.DecodeString(id); err != nil || len(b) != 16 || id != strings.ToLower(id) {
		return sub, errors.New("subscription ID must be 32 lowercase hex characters")
	}
	var status struct {
		Network string `json:"network"`
	}
	if err := getJSON("/v1/status", &status); err != nil {
		return sub, err
	}
	if status.Network != network {
		return sub, fmt.Errorf("API network %s conflicts with %s", status.Network, network)
	}
	if err := getJSON("/v1/subscriptions/"+url.PathEscape(id), &sub); err != nil {
		return sub, err
	}
	if sub.ID != id || sub.Payer == "" || sub.Payee == "" {
		return sub, errors.New("API returned a malformed subscription")
	}
	return sub, nil
}

// signSubTx fetches the account nonce so creation can derive the subscription
// ID before submitting.
func signSubTx(kp *protocol.KeyPair, network, txType string, payload map[string]any) (protocol.Tx, error) {
	var account accountInfo
	if err := getJSON("/v1/accounts/"+url.PathEscape(kp.Address), &account); err != nil {
		return protocol.Tx{}, err
	}
	chain, err := protocol.ChainID(network)
	if err != nil {
		return protocol.Tx{}, err
	}
	now := uint64(time.Now().Unix())
	tx := protocol.Tx{Version: protocol.CurrentTxVersion, Network: network, Chain: chain,
		Type: txType, Signer: kp.Address, Nonce: account.Nonce,
		CreatedAt: now, ExpiresAt: now + 3600, FeeAsset: protocol.NativeAsset(network),
		MaxFee: 10_000, Payload: payload}
	if err := tx.Sign(kp); err != nil {
		return protocol.Tx{}, err
	}
	return tx, nil
}

func submitSigned(tx protocol.Tx, wait bool) error {
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		return err
	}
	return submitTx(raw, wait)
}

// remainingPeriod mirrors the ledger's per-period reset so an omitted charge
// amount fills in what the current period still allows.
func remainingPeriod(sub protocol.Subscription, now uint64) uint64 {
	charged := sub.ChargedPeriod
	if now >= sub.PeriodStart && now-sub.PeriodStart >= sub.Period {
		charged = 0
	}
	if charged >= sub.AmountPerPeriod || sub.ChargedTotal >= sub.TotalCap {
		return 0
	}
	allowed := sub.AmountPerPeriod - charged
	if total := sub.TotalCap - sub.ChargedTotal; total < allowed {
		return total
	}
	return allowed
}

func subscriptionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "subscription", Short: "Authorize and charge recurring payments"}
	var network, mnemonic, payee, asset, perPeriod, totalCap string
	var period, expires uint64
	var wait bool
	create := &cobra.Command{Use: "create", Short: "Authorize recurring charges as the payer", RunE: func(cmd *cobra.Command, args []string) error {
		kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
		if err != nil {
			return err
		}
		info, err := fetchAsset(asset)
		if err != nil {
			return err
		}
		perUnits, err := parseAmount(perPeriod, info.Decimals)
		if err != nil {
			return err
		}
		capUnits, err := parseAmount(totalCap, info.Decimals)
		if err != nil {
			return err
		}
		payload := map[string]any{"payee": payee, "asset": asset,
			"amount_per_period": perUnits, "period": period, "total_cap": capUnits}
		if expires != 0 {
			payload["expires_at"] = expires
		}
		tx, err := signSubTx(kp, network, protocol.TxCreateSub, payload)
		if err != nil {
			return err
		}
		fmt.Printf("subscription: %s\n", protocol.SubscriptionID(network, kp.Address, payee, asset, tx.Nonce))
		return submitSigned(tx, wait)
	}}
	create.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
	create.Flags().StringVar(&mnemonic, "mnemonic", "", "payer mnemonic")
	create.Flags().StringVar(&payee, "payee", "", "address allowed to charge")
	create.Flags().StringVar(&asset, "asset", "", "asset ID")
	create.Flags().StringVar(&perPeriod, "amount-per-period", "", "maximum human amount per period")
	create.Flags().Uint64Var(&period, "period", 0, "period length in seconds")
	create.Flags().StringVar(&totalCap, "total-cap", "", "maximum human amount over the authorization")
	create.Flags().Uint64Var(&expires, "expires", 0, "Unix expiry (0 means no expiry)")
	create.Flags().BoolVar(&wait, "wait", false, "wait for the receipt")
	for _, flag := range []string{"mnemonic", "payee", "asset", "amount-per-period", "period", "total-cap"} {
		_ = create.MarkFlagRequired(flag)
	}

	var id, amount string
	charge := &cobra.Command{Use: "charge", Short: "Charge an authorization as its payee", RunE: func(cmd *cobra.Command, args []string) error {
		sub, err := fetchSubscription(network, id)
		if err != nil {
			return err
		}
		kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
		if err != nil {
			return err
		}
		info, err := fetchAsset(sub.Asset)
		if err != nil {
			return err
		}
		if amount == "" {
			units := remainingPeriod(sub, uint64(time.Now().Unix()))
			if units == 0 {
				return errors.New("nothing left to charge in this period")
			}
			amount = shortAmount(units, info.Decimals)
		}
		units, err := parseAmount(amount, info.Decimals)
		if err != nil {
			return err
		}
		tx, err := signSubTx(kp, network, protocol.TxChargeSub, map[string]any{"id": id, "amount": units})
		if err != nil {
			return err
		}
		return submitSigned(tx, wait)
	}}
	charge.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
	charge.Flags().StringVar(&mnemonic, "mnemonic", "", "payee mnemonic")
	charge.Flags().StringVar(&id, "id", "", "subscription ID")
	charge.Flags().StringVar(&amount, "amount", "", "human amount (default: what this period allows)")
	charge.Flags().BoolVar(&wait, "wait", false, "wait for the receipt")
	for _, flag := range []string{"mnemonic", "id"} {
		_ = charge.MarkFlagRequired(flag)
	}

	cancel := &cobra.Command{Use: "cancel", Short: "Cancel an authorization as its payer", RunE: func(cmd *cobra.Command, args []string) error {
		kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
		if err != nil {
			return err
		}
		tx, err := signSubTx(kp, network, protocol.TxCancelSub, map[string]any{"id": id})
		if err != nil {
			return err
		}
		return submitSigned(tx, wait)
	}}
	cancel.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
	cancel.Flags().StringVar(&mnemonic, "mnemonic", "", "payer mnemonic")
	cancel.Flags().StringVar(&id, "id", "", "subscription ID")
	cancel.Flags().BoolVar(&wait, "wait", false, "wait for the receipt")
	for _, flag := range []string{"mnemonic", "id"} {
		_ = cancel.MarkFlagRequired(flag)
	}

	show := &cobra.Command{Use: "show", Short: "Inspect a subscription", RunE: func(cmd *cobra.Command, args []string) error {
		sub, err := fetchSubscription(network, id)
		if err != nil {
			return err
		}
		return printJSON(sub)
	}}
	show.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
	show.Flags().StringVar(&id, "id", "", "subscription ID")
	_ = show.MarkFlagRequired("id")

	cmd.AddCommand(create, show, charge, cancel)
	return cmd
}
