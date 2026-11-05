package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

func fetchEscrow(network, id string) (protocol.Escrow, error) {
	var escrow protocol.Escrow
	if !strings.HasPrefix(id, "esc_") || len(id) != 36 {
		return escrow, errors.New("escrow ID must be esc_ followed by 32 hex characters")
	}
	var status struct {
		Network string `json:"network"`
	}
	if err := getJSON("/v1/status", &status); err != nil {
		return escrow, err
	}
	if status.Network != network {
		return escrow, fmt.Errorf("API network %s conflicts with %s", status.Network, network)
	}
	if err := getJSON("/v1/escrows/"+url.PathEscape(id), &escrow); err != nil {
		return escrow, err
	}
	if escrow.ID != id || !strings.HasSuffix(escrow.Asset, ":"+protocol.ShortNetwork(network)) {
		return escrow, errors.New("API returned a malformed escrow")
	}
	return escrow, nil
}

func escrowCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "escrow", Short: "Create, inspect, release, and refund escrows"}
	var network, mnemonic, beneficiary, arbiter, asset, amount, id string
	var timeout uint64
	var wait bool

	create := &cobra.Command{Use: "create", Short: "Lock funds for a beneficiary", RunE: func(cmd *cobra.Command, args []string) error {
		now := uint64(time.Now().Unix())
		if timeout <= now {
			return errors.New("--timeout must be a future Unix timestamp")
		}
		if err := protocol.ValidateAddress(beneficiary, network); err != nil {
			return err
		}
		if arbiter != "" {
			if err := protocol.ValidateAddress(arbiter, network); err != nil {
				return err
			}
		}
		info, err := fetchAsset(asset)
		if err != nil {
			return err
		}
		units, err := parseAmount(amount, info.Decimals)
		if err != nil {
			return err
		}
		kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
		if err != nil {
			return err
		}
		tx, err := signTx(kp, network, protocol.TxCreateEscrow, map[string]any{
			"beneficiary": beneficiary, "arbiter": arbiter, "asset": asset,
			"amount": units, "timeout": timeout,
		})
		if err != nil {
			return err
		}
		return submitSigned(tx, wait)
	}}
	create.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
	create.Flags().StringVar(&mnemonic, "mnemonic", "", "payer mnemonic")
	create.Flags().StringVar(&beneficiary, "beneficiary", "", "beneficiary address")
	create.Flags().StringVar(&arbiter, "arbiter", "", "arbiter address (default: payer)")
	create.Flags().StringVar(&asset, "asset", "", "asset ID")
	create.Flags().StringVar(&amount, "amount", "", "human amount")
	create.Flags().Uint64Var(&timeout, "timeout", 0, "refund-after Unix timestamp")
	create.Flags().BoolVar(&wait, "wait", false, "wait for the receipt")
	for _, flag := range []string{"mnemonic", "beneficiary", "asset", "amount", "timeout"} {
		_ = create.MarkFlagRequired(flag)
	}

	show := &cobra.Command{Use: "show", Short: "Inspect an escrow", RunE: func(cmd *cobra.Command, args []string) error {
		escrow, err := fetchEscrow(network, id)
		if err != nil {
			return err
		}
		return printJSON(escrow)
	}}
	show.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
	show.Flags().StringVar(&id, "id", "", "escrow ID")
	_ = show.MarkFlagRequired("id")

	closeCmd := func(name, short, txType string) *cobra.Command {
		close := &cobra.Command{Use: name, Short: short, RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := fetchEscrow(network, id); err != nil {
				return err
			}
			kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
			if err != nil {
				return err
			}
			tx, err := signTx(kp, network, txType, map[string]any{"id": id})
			if err != nil {
				return err
			}
			return submitSigned(tx, wait)
		}}
		close.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
		close.Flags().StringVar(&mnemonic, "mnemonic", "", "signer mnemonic")
		close.Flags().StringVar(&id, "id", "", "escrow ID")
		close.Flags().BoolVar(&wait, "wait", false, "wait for the receipt")
		for _, flag := range []string{"mnemonic", "id"} {
			_ = close.MarkFlagRequired(flag)
		}
		return close
	}

	cmd.AddCommand(create, show,
		closeCmd("release", "Release escrow funds to the beneficiary", protocol.TxReleaseEscrow),
		closeCmd("refund", "Return escrow funds to the payer after timeout", protocol.TxRefundEscrow))
	return cmd
}
