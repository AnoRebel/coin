package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/paymenturi"
	"github.com/anorebel/anorebelcoin/internal/protocol"
)

func fetchIntent(network, id string) (protocol.Intent, error) {
	var in protocol.Intent
	b, err := hex.DecodeString(id)
	if err != nil || len(b) != 16 || id != strings.ToLower(id) {
		return in, errors.New("intent ID must be 32 lowercase hex characters")
	}
	var status struct {
		Network string `json:"network"`
	}
	if err := getJSON("/v1/status", &status); err != nil {
		return in, err
	}
	if status.Network != network {
		return in, fmt.Errorf("API network %s conflicts with %s", status.Network, network)
	}
	if err := getJSON("/v1/intents/"+url.PathEscape(id), &in); err != nil {
		return in, err
	}
	if in.ID != id || protocol.IntentID(network, in.Payee, in.Reference) != id {
		return in, errors.New("API returned a non-canonical intent")
	}
	return in, nil
}

func (r *paymentRequest) resolveIntent() error {
	if r.intent == "" {
		return nil
	}
	in, err := fetchIntent(r.network, r.intent)
	if err != nil {
		return err
	}
	if in.Status != protocol.IntentOpen {
		return fmt.Errorf("intent is %s; payment transaction: %s", in.Status, in.TxHash)
	}
	if in.ExpiresAt != 0 && uint64(time.Now().Unix()) > in.ExpiresAt {
		return errors.New("intent expired")
	}
	if (r.to != "" && r.to != in.Payee) || (r.asset != "" && r.asset != in.Asset) {
		return errors.New("recipient or asset conflicts with intent")
	}
	info, err := fetchAsset(in.Asset)
	if err != nil {
		return err
	}
	if r.amount != "" {
		units, err := parseAmount(r.amount, info.Decimals)
		if err != nil {
			return err
		}
		if units != in.Amount {
			return errors.New("amount conflicts with intent")
		}
	}
	r.to, r.asset, r.amount = in.Payee, in.Asset, shortAmount(in.Amount, info.Decimals)
	if r.memo == "" {
		r.memo = in.Memo
	}
	if in.ExpiresAt != 0 && (r.expires == 0 || in.ExpiresAt < r.expires) {
		r.expires = in.ExpiresAt
	}
	scheme := "arne"
	if r.uri != nil {
		scheme = r.uri.Scheme
	}
	p := paymenturi.Payment{Scheme: scheme, Network: r.network, To: r.to, Asset: r.asset,
		Amount: r.amount, Intent: r.intent, Memo: r.memo, Expires: r.expires}
	validated, err := paymenturi.Parse(p.String())
	if err != nil {
		return err
	}
	r.uri = &validated
	return nil
}

func submitTx(raw []byte, wait bool) error {
	resp, err := http.Post(apiURL("/v1/transactions"), "application/cbor", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("submit failed: %s", strings.TrimSpace(string(body)))
	}
	var out struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	fmt.Println(out.Hash)
	if wait {
		return waitForReceipt(out.Hash)
	}
	return nil
}

func writeIntentQR(path, uri string) error {
	image, err := qrcode.Encode(uri, qrcode.Medium, 768)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(image); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func intentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "intent", Short: "Create, inspect, and link payment intents"}
	var network, mnemonic, asset, amount, reference, memo string
	var expires uint64
	var wait bool
	create := &cobra.Command{Use: "create", Short: "Create an on-chain request signed by the payee", RunE: func(cmd *cobra.Command, args []string) error {
		kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
		if err != nil {
			return err
		}
		if !protocol.ValidIntentReference(reference) {
			return errors.New("reference must be 1-64 letters, digits, _ or -")
		}
		info, err := fetchAsset(asset)
		if err != nil {
			return err
		}
		units, err := parseAmount(amount, info.Decimals)
		if err != nil {
			return err
		}
		var account accountInfo
		if err := getJSON("/v1/accounts/"+url.PathEscape(kp.Address), &account); err != nil {
			return err
		}
		payload := map[string]any{"asset": asset, "amount": units, "reference": reference}
		if expires != 0 {
			payload["expires_at"] = expires
		}
		if memo != "" {
			payload["memo"] = memo
		}
		chain, err := protocol.ChainID(network)
		if err != nil {
			return err
		}
		now := uint64(time.Now().Unix())
		tx := protocol.Tx{Version: protocol.CurrentTxVersion, Network: network, Chain: chain,
			Type: protocol.TxCreateIntent, Signer: kp.Address, Nonce: account.Nonce,
			CreatedAt: now, ExpiresAt: now + 3600, FeeAsset: protocol.NativeAsset(network), MaxFee: 10_000, Payload: payload}
		if err := tx.Sign(kp); err != nil {
			return err
		}
		raw, err := protocol.Encode(tx.ToCBOR())
		if err != nil {
			return err
		}
		fmt.Printf("intent: %s\n", protocol.IntentID(network, kp.Address, reference))
		return submitTx(raw, wait)
	}}
	create.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
	create.Flags().StringVar(&mnemonic, "mnemonic", "", "payee mnemonic")
	create.Flags().StringVar(&asset, "asset", "", "asset ID")
	create.Flags().StringVar(&amount, "amount", "", "human amount")
	create.Flags().StringVar(&reference, "reference", "", "payee-unique reference")
	create.Flags().StringVar(&memo, "memo", "", "public memo")
	create.Flags().Uint64Var(&expires, "expires", 0, "Unix expiry (0 means no expiry)")
	create.Flags().BoolVar(&wait, "wait", false, "wait for the receipt")
	for _, flag := range []string{"mnemonic", "asset", "amount", "reference"} {
		_ = create.MarkFlagRequired(flag)
	}
	cmd.AddCommand(create)
	for _, name := range []string{"show", "link"} {
		var network, id, qrPath string
		sub := &cobra.Command{Use: name, Short: "Inspect an intent or print its canonical payment URI", RunE: func(c *cobra.Command, args []string) error {
			if c.Name() == "show" {
				in, err := fetchIntent(network, id)
				if err != nil {
					return err
				}
				return printJSON(in)
			}
			r := paymentRequest{network: network, intent: id}
			if err := r.resolveIntent(); err != nil {
				return err
			}
			fmt.Println(r.uri.String())
			if qrPath != "" {
				return writeIntentQR(qrPath, r.uri.String())
			}
			return nil
		}}
		sub.Flags().StringVar(&network, "network", "arne:devnet", "network ID")
		sub.Flags().StringVar(&id, "id", "", "intent ID")
		_ = sub.MarkFlagRequired("id")
		if name == "link" {
			sub.Flags().StringVar(&qrPath, "qr", "", "write a PNG QR code to a new file")
		}
		cmd.AddCommand(sub)
	}
	return cmd
}
