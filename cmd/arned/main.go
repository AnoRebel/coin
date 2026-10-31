// Command arned runs an ARNE node and provides wallet, payment, asset, and
// operator commands.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/govalues/decimal"
	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/api"
	"github.com/anorebel/anorebelcoin/internal/backup"
	"github.com/anorebel/anorebelcoin/internal/ledger"
	arnememo "github.com/anorebel/anorebelcoin/internal/memo"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/paymenturi"
	"github.com/anorebel/anorebelcoin/internal/preview"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
	"github.com/anorebel/anorebelcoin/internal/webhook"
)

const version = "0.1.0"

func main() {
	if err := root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func root() *cobra.Command {
	c := &cobra.Command{
		Use:           "arned",
		Short:         "ARNE node, wallet, and settlement CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.AddCommand(versionCmd(), devnetCmd(), startCmd(), walletCmd(), balanceCmd(), payCmd(),
		previewCmd(), faucetCmd(), statusCmd(), assetCmd(), proofCmd(), memoCmd(), uriCmd(),
		webhookCmd(), intentCmd(), subscriptionCmd(), backupCmd(), restoreCmd())
	return c
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("arned %s (protocol arne/1)\n", version)
		},
	}
}

// webhookSeed validates --webhook-key when endpoints are configured.
func webhookSeed(urls []string, keyHex string) ([]byte, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	seed, err := hex.DecodeString(keyHex)
	if err != nil || len(seed) != 32 {
		return nil, errors.New("--webhook-key must be 32 bytes of hex when --webhook is set")
	}
	return seed, nil
}

func webhookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Webhook signing keys",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "keygen",
		Short: "Generate a webhook signing seed and public key",
		RunE: func(cmd *cobra.Command, args []string) error {
			seed := make([]byte, 32)
			if _, err := rand.Read(seed); err != nil {
				return err
			}
			signer, err := webhook.NewSigner(seed)
			if err != nil {
				return err
			}
			fmt.Printf("seed:       %s\npublic_key: %s\n", hex.EncodeToString(seed), signer.PublicKey())
			return nil
		},
	})
	return cmd
}

func devnetCmd() *cobra.Command {
	var (
		network        string
		addr           string
		dataDir        string
		mnemonic       string
		interval       time.Duration
		noPersist      bool
		apiKeySpecs    []string
		allowedOrigins []string
		rateLimit      int
		keepBlocks     uint64
		webhookURLs    []string
		webhookKey     string
	)
	cmd := &cobra.Command{
		Use:   "devnet",
		Short: "Run a single-node development network",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := protocol.NetworkClass(network); err != nil {
				return err
			}
			chain, _ := protocol.ChainID(network)
			generated := false
			if mnemonic == "" {
				kp, phrase, err := protocol.NewKeyPair(network)
				if err != nil {
					return err
				}
				_ = kp
				mnemonic = phrase
				generated = true
			}
			funder, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
			if err != nil {
				return err
			}
			faucetKP, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 1, network)
			if err != nil {
				return err
			}
			var store storage.Store
			if noPersist {
				store = storage.NewMemory()
			} else {
				store, err = storage.OpenPebble(dataDir)
				if err != nil {
					return err
				}
			}
			defer store.Close()
			seed, err := webhookSeed(webhookURLs, webhookKey)
			if err != nil {
				return err
			}
			n, err := node.New(node.Config{
				Network: network, Chain: chain, Params: ledger.DefaultParams(),
				Proposer: funder.Address, Faucet: faucetKP, FaucetAmount: 100_000_000_00,
				Devnet: true, BlockInterval: interval,
				Webhooks: webhookURLs, WebhookSeed: seed,
			}, node.DevnetGenesis(network, uint64(time.Now().Unix()), funder, faucetKP), store)
			if err != nil {
				return err
			}
			defer n.CloseWebhooks()
			if len(webhookURLs) > 0 {
				fmt.Printf("webhooks: %d endpoint(s), signing key %s\n", len(webhookURLs), n.WebhookPublicKey())
			}
			if generated {
				fmt.Println("Generated devnet mnemonic (write it down):")
				fmt.Println("  " + mnemonic)
			}
			fmt.Printf("network:  %s\nchain:    %s\nfunder:   %s\nfaucet:   %s\napi:      http://%s\n",
				network, chain, funder.Address, faucetKP.Address, addr)

			apiKeys, err := parseAPIKeys(apiKeySpecs)
			if err != nil {
				return err
			}
			srv := api.New(n, api.Config{
				Devnet: true, RateLimit: rateLimit,
				APIKeys: apiKeys, AllowedOrigins: allowedOrigins,
			})
			go func() {
				if err := srv.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
					fmt.Fprintln(os.Stderr, "api:", err)
				}
			}()

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			lastBlock := time.Now()
			for {
				select {
				case <-stop:
					fmt.Println("\nshutting down")
					return nil
				case <-ticker.C:
					due := n.Status().Mempool > 0 || time.Since(lastBlock) >= interval
					if !due {
						continue
					}
					b, err := n.ProduceBlock(uint64(time.Now().Unix()))
					if err != nil {
						fmt.Fprintln(os.Stderr, "block:", err)
						continue
					}
					lastBlock = time.Now()
					if len(b.TxHashes) > 0 {
						h, _ := b.Hash()
						fmt.Printf("block %d %s txs=%d\n", b.Height, h[:12], len(b.TxHashes))
					}
				}
			}
		},
	}
	cmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	cmd.Flags().StringVar(&addr, "listen", "127.0.0.1:8080", "API listen address")
	cmd.Flags().StringVar(&dataDir, "data-dir", ".arne-devnet", "Pebble data directory")
	cmd.Flags().StringVar(&mnemonic, "mnemonic", "", "devnet mnemonic (generated if empty)")
	cmd.Flags().DurationVar(&interval, "block-interval", 2*time.Second, "idle block interval")
	cmd.Flags().BoolVar(&noPersist, "ephemeral", false, "keep state in memory only")
	cmd.Flags().StringArrayVar(&apiKeySpecs, "api-key", nil, "API key as key=read,write[,admin] (repeatable)")
	cmd.Flags().StringArrayVar(&allowedOrigins, "allow-origin", nil, "CORS origin to allow (repeatable)")
	cmd.Flags().IntVar(&rateLimit, "rate-limit", 200, "requests per second per IP (0 disables)")
	cmd.Flags().Uint64Var(&keepBlocks, "keep-blocks", 0, "keep only the newest N blocks (0 archives all)")
	cmd.Flags().StringArrayVar(&webhookURLs, "webhook", nil, "webhook endpoint URL (repeatable)")
	cmd.Flags().StringVar(&webhookKey, "webhook-key", "", "hex Ed25519 seed for signing webhooks")
	return cmd
}

// --- wallet ---

func walletCmd() *cobra.Command {
	c := &cobra.Command{Use: "wallet", Short: "Wallet utilities"}
	var network string
	var jsonOut bool
	newCmd := &cobra.Command{
		Use:   "new",
		Short: "Generate a mnemonic and address",
		RunE: func(cmd *cobra.Command, args []string) error {
			kp, phrase, err := protocol.NewKeyPair(network)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(map[string]any{"mnemonic": phrase, "address": kp.Address, "network": network})
			}
			fmt.Println("mnemonic:", phrase)
			fmt.Println("address: ", kp.Address)
			return nil
		},
	}
	newCmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	newCmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	addrCmd := &cobra.Command{
		Use:   "address",
		Short: "Derive an address from a mnemonic",
		RunE: func(cmd *cobra.Command, args []string) error {
			mnemonic, _ := cmd.Flags().GetString("mnemonic")
			index, _ := cmd.Flags().GetUint32("index")
			kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, index, network)
			if err != nil {
				return err
			}
			_, err = fmt.Println(kp.Address)
			return err
		},
	}
	addrCmd.Flags().String("mnemonic", "", "BIP-39 mnemonic")
	addrCmd.Flags().Uint32("index", 0, "address index")
	addrCmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	_ = addrCmd.MarkFlagRequired("mnemonic")
	c.AddCommand(newCmd, addrCmd)
	return c
}

// parseAPIKeys reads repeated key=scope[,scope] flags.
func parseAPIKeys(specs []string) (map[string][]string, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	keys := make(map[string][]string, len(specs))
	for _, spec := range specs {
		key, scopes, ok := strings.Cut(spec, "=")
		if !ok || key == "" || scopes == "" {
			return nil, fmt.Errorf("--api-key must be key=scope[,scope]")
		}
		keys[key] = strings.Split(scopes, ",")
	}
	return keys, nil
}

// --- HTTP client helpers ---

func apiURL(path string) string {
	base := os.Getenv("ARNE_API")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	return strings.TrimRight(base, "/") + path
}

func getJSON(path string, out any) error {
	resp, err := http.Get(apiURL(path))
	if err != nil {
		return fmt.Errorf("API unreachable (is `arned devnet` running?): %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func postJSON(path string, in any) (map[string]any, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(apiURL(path), "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("API unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return out, nil
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// amountPattern keeps user input to plain decimals; decimal.Parse also
// accepts exponents and a leading plus, which are surprising for money.
var amountPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

func parseAmount(s string, decimals uint8) (uint64, error) {
	s = strings.TrimSpace(s)
	if !amountPattern.MatchString(s) {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	d, err := decimal.Parse(s)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	if d.Cmp(d.Trunc(int(decimals))) != 0 {
		return 0, fmt.Errorf("amount has more than %d decimals", decimals)
	}
	whole, frac, ok := d.Int64(int(decimals))
	if !ok {
		return 0, errors.New("amount too large")
	}
	scale := int64(1)
	for i := uint8(0); i < decimals; i++ {
		scale *= 10
	}
	if whole > (1<<63-1)/scale {
		return 0, errors.New("amount too large")
	}
	return uint64(whole)*uint64(scale) + uint64(frac), nil
}

func formatAmount(v uint64, decimals uint8) string {
	if decimals == 0 {
		return strconv.FormatUint(v, 10)
	}
	scale := uint64(1)
	for i := uint8(0); i < decimals; i++ {
		scale *= 10
	}
	return fmt.Sprintf("%d.%0*d", v/scale, decimals, v%scale)
}

// shortAmount omits unnecessary trailing fractional zeros from a human amount.
func shortAmount(v uint64, decimals uint8) string {
	if decimals == 0 {
		return strconv.FormatUint(v, 10)
	}
	return strings.TrimRight(strings.TrimRight(formatAmount(v, decimals), "0"), ".")
}

type assetInfo struct {
	ID       string `json:"id"`
	Symbol   string `json:"symbol"`
	Decimals uint8  `json:"decimals"`
}

func fetchAsset(id string) (assetInfo, error) {
	var a assetInfo
	if err := getJSON("/v1/assets/"+url.PathEscape(id), &a); err != nil {
		return a, err
	}
	return a, nil
}

// fetchAssetOptional treats a missing asset as unknown instead of an error.
func fetchAssetOptional(id string) (assetInfo, bool, error) {
	resp, err := http.Get(apiURL("/v1/assets/" + url.PathEscape(id)))
	if err != nil {
		return assetInfo{}, false, fmt.Errorf("API unreachable (is `arned devnet` running?): %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return assetInfo{}, false, nil
	}
	if resp.StatusCode >= 300 {
		return assetInfo{}, false, fmt.Errorf("api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var a assetInfo
	if err := json.Unmarshal(body, &a); err != nil {
		return a, false, err
	}
	return a, true, nil
}

type accountInfo struct {
	Address  string            `json:"address"`
	Nonce    uint64            `json:"nonce"`
	Balances map[string]uint64 `json:"balances"`
}

// --- balance ---

func balanceCmd() *cobra.Command {
	var network, mnemonic, address, asset string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "balance",
		Short: "Show account balances",
		RunE: func(cmd *cobra.Command, args []string) error {
			if address == "" {
				kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
				if err != nil {
					return err
				}
				address = kp.Address
			}
			var acc accountInfo
			if err := getJSON("/v1/accounts/"+url.PathEscape(address), &acc); err != nil {
				return err
			}
			if asset != "" {
				a, err := fetchAsset(asset)
				if err != nil {
					return err
				}
				v := acc.Balances[asset]
				if jsonOut {
					return printJSON(map[string]any{"address": address, "asset": asset, "balance": v, "display": formatAmount(v, a.Decimals)})
				}
				fmt.Printf("%s %s\n", formatAmount(v, a.Decimals), a.Symbol)
				return nil
			}
			if jsonOut {
				return printJSON(acc)
			}
			fmt.Println("address:", acc.Address, "nonce:", acc.Nonce)
			for id, v := range acc.Balances {
				a, err := fetchAsset(id)
				if err != nil {
					fmt.Printf("  %-24s %d\n", id, v)
					continue
				}
				fmt.Printf("  %-24s %s %s\n", id, formatAmount(v, a.Decimals), a.Symbol)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	cmd.Flags().StringVar(&mnemonic, "mnemonic", "", "mnemonic (or use --address)")
	cmd.Flags().StringVar(&address, "address", "", "address")
	cmd.Flags().StringVar(&asset, "asset", "", "asset id")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	return cmd
}

// --- pay ---

// paymentRequest is the transfer-shaped input shared by pay and preview.
type paymentRequest struct {
	network, to, asset, amount, memo, intent, refundOf string
	expires                                            uint64
	uri                                                *paymenturi.Payment
}

// applyURI folds --uri into the request and refuses conflicting explicit values.
func (r *paymentRequest) applyURI(raw string, networkSet bool) error {
	if raw == "" {
		return nil
	}
	p, err := paymenturi.Parse(raw)
	if err != nil {
		return err
	}
	if networkSet {
		if err := p.CheckNetwork(r.network); err != nil {
			return err
		}
	}
	if err := p.CheckRecipient(r.to); err != nil {
		return err
	}
	if err := p.CheckAmount(r.amount); err != nil {
		return err
	}
	if err := p.CheckIntent(r.intent); err != nil {
		return err
	}
	if r.asset != "" && r.asset != p.Asset {
		return errors.New("uri asset conflicts with --asset")
	}
	if r.memo != "" && p.Memo != "" && r.memo != p.Memo {
		return errors.New("uri memo conflicts with --memo")
	}
	r.network, r.to, r.asset, r.amount = p.Network, p.To, p.Asset, p.Amount
	r.intent = p.Intent
	if r.memo == "" {
		r.memo = p.Memo
	}
	r.expires = p.Expires
	r.uri = &p
	return nil
}

// transferPayload builds the transfer payload, sealing the memo when a key is given.
func transferPayload(asset, to string, units uint64, memo, memoTo string) (map[string]any, error) {
	payload := map[string]any{"asset": asset, "to": to, "amount": units}
	switch {
	case memo != "" && memoTo != "":
		recipient, err := hex.DecodeString(memoTo)
		if err != nil {
			return nil, fmt.Errorf("--memo-to must be hex: %w", err)
		}
		sealed, err := arnememo.RandomSeal(recipient, []byte(memo))
		if err != nil {
			return nil, err
		}
		payload["memo_enc"] = sealed
	case memo != "":
		fmt.Fprintln(os.Stderr, "warning: --memo is public on chain; use --memo-to to encrypt it")
		payload["memo"] = memo
	}
	return payload, nil
}

func payCmd() *cobra.Command {
	var network, mnemonic, to, asset, amount, memoTo, rawURI string
	var memo, intent, refundOf string
	var wait, yes bool
	cmd := &cobra.Command{
		Use:   "pay",
		Short: "Sign and submit a transfer",
		RunE: func(cmd *cobra.Command, args []string) error {
			req := paymentRequest{network: network, to: to, asset: asset, amount: amount, memo: memo, intent: intent, refundOf: refundOf}
			if err := req.applyURI(rawURI, cmd.Flags().Changed("network")); err != nil {
				return err
			}
			if err := req.resolveRefund(); err != nil {
				return err
			}
			if err := req.resolveIntent(); err != nil {
				return err
			}
			network, to, asset, amount, memo = req.network, req.to, req.asset, req.amount, req.memo
			if req.uri != nil {
				fmt.Println(req.uri.Summary())
				if !yes {
					return errors.New("payment not sent: review the summary above and re-run with --yes")
				}
			}
			if to == "" || asset == "" || amount == "" {
				return errors.New("--to, --asset, and --amount are required without --uri")
			}
			kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
			if err != nil {
				return err
			}
			var acc accountInfo
			if err := getJSON("/v1/accounts/"+url.PathEscape(kp.Address), &acc); err != nil {
				return err
			}
			info, err := fetchAsset(asset)
			if err != nil {
				return err
			}
			units, err := parseAmount(amount, info.Decimals)
			if err != nil {
				return err
			}
			chain, _ := protocol.ChainID(network)
			payload, err := transferPayload(asset, to, units, memo, memoTo)
			if err != nil {
				return err
			}
			if req.intent != "" {
				payload["intent"] = req.intent
			}
			if req.refundOf != "" {
				payload["refund_of"] = req.refundOf
			}
			expiresAt := uint64(time.Now().Unix()) + 3600
			if req.expires != 0 && req.expires < expiresAt {
				expiresAt = req.expires
			}
			tx := protocol.Tx{
				Version: protocol.CurrentTxVersion, Network: network, Chain: chain,
				Type: protocol.TxTransfer, Signer: kp.Address, Nonce: acc.Nonce,
				CreatedAt: uint64(time.Now().Unix()), ExpiresAt: expiresAt,
				FeeAsset: protocol.NativeAsset(network), MaxFee: 10_000, Payload: payload,
			}
			if err := tx.Sign(kp); err != nil {
				return err
			}
			raw, err := protocol.Encode(tx.ToCBOR())
			if err != nil {
				return err
			}
			return submitTx(raw, wait)
		},
	}
	cmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	cmd.Flags().StringVar(&mnemonic, "mnemonic", "", "sender mnemonic")
	cmd.Flags().StringVar(&to, "to", "", "recipient address")
	cmd.Flags().StringVar(&asset, "asset", "", "asset id (for example arce:devnet)")
	cmd.Flags().StringVar(&amount, "amount", "", "human amount, for example 12.5")
	cmd.Flags().StringVar(&memo, "memo", "", "memo text")
	cmd.Flags().StringVar(&memoTo, "memo-to", "", "recipient memo public key (hex); encrypts --memo")
	cmd.Flags().StringVar(&rawURI, "uri", "", "arce:/arne: payment URI; shows a preview first")
	cmd.Flags().StringVar(&intent, "intent", "", "payment intent ID; recipient, asset, and amount must match")
	cmd.Flags().StringVar(&refundOf, "refund-of", "", "original transaction hash to refund; fills recipient, asset, and remaining amount")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the URI preview without prompting")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the receipt")
	_ = cmd.MarkFlagRequired("mnemonic")
	return cmd
}

// previewCmd signs a transfer without submitting it and prints the confirmation.
func previewCmd() *cobra.Command {
	var req paymentRequest
	var mnemonic, memoTo, rawURI string
	var known []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Show a transfer before signing, with safety warnings",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := req.applyURI(rawURI, cmd.Flags().Changed("network")); err != nil {
				return err
			}
			if err := req.resolveRefund(); err != nil {
				return err
			}
			if err := req.resolveIntent(); err != nil {
				return err
			}
			if req.to == "" || req.asset == "" || req.amount == "" {
				return errors.New("--to, --asset, and --amount are required without --uri")
			}
			kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, req.network)
			if err != nil {
				return err
			}
			var acc accountInfo
			if err := getJSON("/v1/accounts/"+url.PathEscape(kp.Address), &acc); err != nil {
				return err
			}
			info, assetKnown, err := fetchAssetOptional(req.asset)
			if err != nil {
				return err
			}
			decimals := info.Decimals
			if !assetKnown {
				decimals = 8 // unknown asset: assume native precision for display only
			}
			units, err := parseAmount(req.amount, decimals)
			if err != nil {
				return err
			}
			payload, err := transferPayload(req.asset, req.to, units, req.memo, memoTo)
			if err != nil {
				return err
			}
			if req.intent != "" {
				payload["intent"] = req.intent
			}
			if req.refundOf != "" {
				payload["refund_of"] = req.refundOf
			}
			expiresAt := uint64(time.Now().Unix()) + 3600
			if req.expires != 0 && req.expires < expiresAt {
				expiresAt = req.expires
			}
			chain, _ := protocol.ChainID(req.network)
			tx := protocol.Tx{
				Version: protocol.CurrentTxVersion, Network: req.network, Chain: chain,
				Type: protocol.TxTransfer, Signer: kp.Address, Nonce: acc.Nonce,
				CreatedAt: uint64(time.Now().Unix()), ExpiresAt: expiresAt,
				FeeAsset: protocol.NativeAsset(req.network), MaxFee: 10_000, Payload: payload,
			}
			if err := tx.Sign(kp); err != nil {
				return err
			}
			raw, err := protocol.Encode(tx.ToCBOR())
			if err != nil {
				return err
			}
			receipt, err := simulateTransfer(raw)
			if err != nil {
				return err
			}
			fee := receipt.Fee // failed transactions are free in v1
			p := preview.Build(preview.Input{
				Network: req.network, Signer: kp.Address, To: req.to, Asset: req.asset,
				Symbol: info.Symbol, Decimals: decimals, Amount: units, Fee: fee,
				Memo: req.memo, PublicMemo: req.memo != "" && memoTo == "",
				AssetKnown: assetKnown, Known: known,
				Simulated: true, Code: string(receipt.Code),
			})
			if jsonOut {
				return printJSON(p)
			}
			fmt.Println(p.Summary())
			return nil
		},
	}
	cmd.Flags().StringVar(&req.network, "network", "arne:devnet", "network id")
	cmd.Flags().StringVar(&mnemonic, "mnemonic", "", "sender mnemonic")
	cmd.Flags().StringVar(&req.to, "to", "", "recipient address")
	cmd.Flags().StringVar(&req.asset, "asset", "", "asset id (for example arce:devnet)")
	cmd.Flags().StringVar(&req.amount, "amount", "", "human amount, for example 12.5")
	cmd.Flags().StringVar(&req.memo, "memo", "", "memo text")
	cmd.Flags().StringVar(&memoTo, "memo-to", "", "recipient memo public key (hex); encrypts --memo")
	cmd.Flags().StringVar(&rawURI, "uri", "", "arce:/arne: payment URI")
	cmd.Flags().StringVar(&req.intent, "intent", "", "payment intent ID; recipient, asset, and amount must match")
	cmd.Flags().StringVar(&req.refundOf, "refund-of", "", "original transaction hash to refund; fills recipient, asset, and remaining amount")
	cmd.Flags().StringArrayVar(&known, "known", nil, "trusted contact address for lookalike checks (repeatable)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	_ = cmd.MarkFlagRequired("mnemonic")
	return cmd
}

// simulateTransfer dry-runs a signed transaction and reports its ledger code.
func simulateTransfer(raw []byte) (ledger.Receipt, error) {
	resp, err := http.Post(apiURL("/v1/transactions/simulate"), "application/cbor", bytes.NewReader(raw))
	if err != nil {
		return ledger.Receipt{}, fmt.Errorf("API unreachable (is `arned devnet` running?): %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var out struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &out) == nil && out.Error.Code != "" {
			return ledger.Receipt{Code: ledger.Code(out.Error.Code)}, nil
		}
		return ledger.Receipt{}, fmt.Errorf("simulate failed: %s", strings.TrimSpace(string(body)))
	}
	var receipt ledger.Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return ledger.Receipt{}, err
	}
	return receipt, nil
}

// --- payment URIs ---

func uriCmd() *cobra.Command {
	var network, to, asset, amount, memo, scheme, raw, intent string
	var expires uint64
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "uri",
		Short: "Parse and build arce:/arne: payment URIs",
	}
	parse := &cobra.Command{
		Use:   "parse",
		Short: "Validate a payment URI and print its summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := paymenturi.Parse(raw)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(map[string]any{
					"scheme": p.Scheme, "network": p.Network, "to": p.To, "asset": p.Asset,
					"amount": p.Amount, "memo": p.Memo, "expires": p.Expires,
					"intent":   p.Intent,
					"warnings": p.Warnings, "canonical": p.String(),
				})
			}
			fmt.Println(p.Summary())
			return nil
		},
	}
	parse.Flags().StringVar(&raw, "uri", "", "payment URI")
	parse.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	_ = parse.MarkFlagRequired("uri")

	build := &cobra.Command{
		Use:   "build",
		Short: "Build a canonical payment URI",
		RunE: func(cmd *cobra.Command, args []string) error {
			if scheme != "arce" && scheme != "arne" {
				return errors.New("--scheme must be arce or arne")
			}
			p, err := paymenturi.ParseAt(
				fmt.Sprintf("%s:%s?network=%s&asset=%s&amount=%s", scheme, to,
					url.QueryEscape(network), url.QueryEscape(asset), url.QueryEscape(amount)),
				uint64(time.Now().Unix()),
			)
			if err != nil {
				return err
			}
			p.Memo, p.Expires, p.Intent = memo, expires, intent
			if memo != "" || expires != 0 || intent != "" {
				if _, err := paymenturi.ParseAt(p.String(), uint64(time.Now().Unix())); err != nil {
					return err
				}
			}
			fmt.Println(p.String())
			return nil
		},
	}
	build.Flags().StringVar(&scheme, "scheme", "arce", "arce or arne")
	build.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	build.Flags().StringVar(&to, "to", "", "recipient address")
	build.Flags().StringVar(&asset, "asset", "", "asset id")
	build.Flags().StringVar(&amount, "amount", "", "decimal amount")
	build.Flags().StringVar(&memo, "memo", "", "optional memo")
	build.Flags().StringVar(&intent, "intent", "", "payment intent ID")
	build.Flags().Uint64Var(&expires, "expires", 0, "unix expiry")
	for _, f := range []string{"to", "asset", "amount"} {
		_ = build.MarkFlagRequired(f)
	}
	cmd.AddCommand(parse, build)
	return cmd
}

// --- memo ---

func memoCmd() *cobra.Command {
	var network, mnemonic, recipient, text, sealed string
	var index uint32
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "memo",
		Short: "Create and open opt-in encrypted memos",
	}
	pub := &cobra.Command{
		Use:   "pub",
		Short: "Print your memo public key to share with senders",
		RunE: func(cmd *cobra.Command, args []string) error {
			kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, index, network)
			if err != nil {
				return err
			}
			pk, err := arnememo.PublicKey(kp.Private.Seed())
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(map[string]any{"memo_public_key": hex.EncodeToString(pk), "address": kp.Address})
			}
			fmt.Println(hex.EncodeToString(pk))
			return nil
		},
	}
	pub.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	pub.Flags().StringVar(&mnemonic, "mnemonic", "", "wallet mnemonic")
	pub.Flags().Uint32Var(&index, "index", 0, "account index")
	pub.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	_ = pub.MarkFlagRequired("mnemonic")

	seal := &cobra.Command{
		Use:   "seal",
		Short: "Encrypt memo text to a recipient memo public key",
		RunE: func(cmd *cobra.Command, args []string) error {
			pk, err := hex.DecodeString(recipient)
			if err != nil {
				return fmt.Errorf("--recipient must be hex: %w", err)
			}
			out, err := arnememo.RandomSeal(pk, []byte(text))
			if err != nil {
				return err
			}
			fmt.Println(hex.EncodeToString(out))
			return nil
		},
	}
	seal.Flags().StringVar(&recipient, "recipient", "", "recipient memo public key (hex)")
	seal.Flags().StringVar(&text, "text", "", "memo text (256 bytes max)")
	_ = seal.MarkFlagRequired("recipient")
	_ = seal.MarkFlagRequired("text")

	open := &cobra.Command{
		Use:   "open",
		Short: "Decrypt a sealed memo",
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := decodeHexOrBase64(sealed)
			if err != nil {
				return err
			}
			kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, index, network)
			if err != nil {
				return err
			}
			plain, err := arnememo.Open(kp.Private.Seed(), raw)
			if err != nil {
				return err
			}
			fmt.Println(string(plain))
			return nil
		},
	}
	open.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	open.Flags().StringVar(&mnemonic, "mnemonic", "", "wallet mnemonic")
	open.Flags().Uint32Var(&index, "index", 0, "account index")
	open.Flags().StringVar(&sealed, "sealed", "", "sealed memo hex or base64")
	_ = open.MarkFlagRequired("mnemonic")
	_ = open.MarkFlagRequired("sealed")

	cmd.AddCommand(pub, seal, open)
	return cmd
}

func decodeHexOrBase64(s string) ([]byte, error) {
	if b, err := hex.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, errors.New("value must be hex or base64")
}

func waitForReceipt(hash string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var r ledger.Receipt
		err := getJSON("/v1/transactions/"+hash, &r)
		if err == nil {
			if r.Success {
				fmt.Printf("confirmed in block %d, fee %d %s\n", r.Height, r.Fee, r.FeeAsset)
				return nil
			}
			return fmt.Errorf("transaction failed: %s %s", r.Code, r.Error)
		}
		time.Sleep(700 * time.Millisecond)
	}
	return errors.New("timed out waiting for receipt")
}

// --- faucet ---

func faucetCmd() *cobra.Command {
	var network, mnemonic, address string
	cmd := &cobra.Command{
		Use:   "faucet",
		Short: "Request devnet funds",
		RunE: func(cmd *cobra.Command, args []string) error {
			if address == "" {
				kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
				if err != nil {
					return err
				}
				address = kp.Address
			}
			out, err := postJSON("/v1/faucet", map[string]string{"address": address})
			if err != nil {
				return err
			}
			fmt.Println(out["hash"])
			return nil
		},
	}
	cmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	cmd.Flags().StringVar(&mnemonic, "mnemonic", "", "mnemonic")
	cmd.Flags().StringVar(&address, "address", "", "address")
	return cmd
}

// --- backup / restore ---

func backupPassphrase(flag string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv("ARNE_BACKUP_PASSPHRASE")
}

func backupCmd() *cobra.Command {
	var home, out, pass string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write an encrypted backup of a stopped node home",
		RunE: func(cmd *cobra.Command, args []string) error {
			pass = backupPassphrase(pass)
			if pass == "" {
				return errors.New("set --passphrase or ARNE_BACKUP_PASSPHRASE")
			}
			f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			if err := backup.Create(home, f, pass); err != nil {
				f.Close()
				os.Remove(out)
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	cmd.Flags().StringVar(&home, "home", ".arne", "node home to back up (stop the node first)")
	cmd.Flags().StringVar(&out, "out", "", "backup file to write")
	cmd.Flags().StringVar(&pass, "passphrase", "", "encryption passphrase (or ARNE_BACKUP_PASSPHRASE)")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func restoreCmd() *cobra.Command {
	var home, in, pass string
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore an encrypted backup into a new node home",
		RunE: func(cmd *cobra.Command, args []string) error {
			pass = backupPassphrase(pass)
			if pass == "" {
				return errors.New("set --passphrase or ARNE_BACKUP_PASSPHRASE")
			}
			f, err := os.Open(in)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := backup.Restore(home, f, pass); err != nil {
				return err
			}
			fmt.Println(home)
			return nil
		},
	}
	cmd.Flags().StringVar(&home, "home", "", "new node home to create")
	cmd.Flags().StringVar(&in, "in", "", "backup file to restore")
	cmd.Flags().StringVar(&pass, "passphrase", "", "encryption passphrase (or ARNE_BACKUP_PASSPHRASE)")
	_ = cmd.MarkFlagRequired("home")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

// --- status ---

func statusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show node status",
		RunE: func(cmd *cobra.Command, args []string) error {
			var st node.Status
			if err := getJSON("/v1/status", &st); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(st)
			}
			fmt.Printf("network: %s\nchain:   %s\nheight:  %d\nroot:    %s\nmempool: %d\n",
				st.Network, st.Chain, st.Height, st.StateRoot, st.Mempool)
			if st.Halted != "" {
				fmt.Println("HALTED: ", st.Halted)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	return cmd
}

// --- asset ---

func assetCmd() *cobra.Command {
	c := &cobra.Command{Use: "asset", Short: "Asset operations"}
	var network, mnemonic, id, symbol, name, policy string
	var decimals uint64
	var maxSupply string
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered assets",
		RunE: func(cmd *cobra.Command, args []string) error {
			var out map[string]any
			if err := getJSON("/v1/assets", &out); err != nil {
				return err
			}
			return printJSON(out["items"])
		},
	}
	register := &cobra.Command{
		Use:   "register",
		Short: "Register a new asset",
		RunE: func(cmd *cobra.Command, args []string) error {
			kp, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
			if err != nil {
				return err
			}
			var acc accountInfo
			if err := getJSON("/v1/accounts/"+url.PathEscape(kp.Address), &acc); err != nil {
				return err
			}
			maxUnits, err := parseAmount(maxSupply, 0)
			if err != nil {
				return err
			}
			assetMap := map[string]any{
				"id": id, "class": "fiat", "symbol": symbol, "name": name,
				"decimals": decimals, "policy": policy, "max_supply": maxUnits,
				"issuer": kp.Address, "registered": 0, "meta": map[string]any{},
			}
			chain, _ := protocol.ChainID(network)
			tx := protocol.Tx{
				Version: protocol.CurrentTxVersion, Network: network, Chain: chain,
				Type: protocol.TxRegisterAsset, Signer: kp.Address, Nonce: acc.Nonce,
				CreatedAt: uint64(time.Now().Unix()), ExpiresAt: uint64(time.Now().Unix()) + 3600,
				FeeAsset: protocol.NativeAsset(network), MaxFee: 10_000,
				Payload: map[string]any{"asset": assetMap},
			}
			if err := tx.Sign(kp); err != nil {
				return err
			}
			raw, err := protocol.Encode(tx.ToCBOR())
			if err != nil {
				return err
			}
			resp, err := http.Post(apiURL("/v1/transactions"), "application/cbor", bytes.NewReader(raw))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 300 {
				return fmt.Errorf("register failed: %s", strings.TrimSpace(string(body)))
			}
			fmt.Println(strings.TrimSpace(string(body)))
			return nil
		},
	}
	register.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	register.Flags().StringVar(&mnemonic, "mnemonic", "", "issuer mnemonic")
	register.Flags().StringVar(&id, "id", "", "asset id, for example eur:devnet")
	register.Flags().StringVar(&symbol, "symbol", "", "symbol, for example EUR")
	register.Flags().StringVar(&name, "name", "", "display name")
	register.Flags().Uint64Var(&decimals, "decimals", 2, "decimal places")
	register.Flags().StringVar(&policy, "policy", "capped", "fixed|capped|mintable|burnable")
	register.Flags().StringVar(&maxSupply, "max-supply", "0", "maximum whole units")
	for _, f := range []string{"mnemonic", "id", "symbol", "name"} {
		_ = register.MarkFlagRequired(f)
	}
	c.AddCommand(list, register)
	return c
}

// --- proof ---

func proofCmd() *cobra.Command {
	var address, asset string
	cmd := &cobra.Command{
		Use:   "proof",
		Short: "Fetch a state proof for an account balance",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/accounts/" + url.PathEscape(address) + "/proof"
			if asset != "" {
				path += "?asset=" + url.QueryEscape(asset)
			}
			var out map[string]any
			if err := getJSON(path, &out); err != nil {
				return err
			}
			return printJSON(out)
		},
	}
	cmd.Flags().StringVar(&address, "address", "", "account address")
	cmd.Flags().StringVar(&asset, "asset", "", "asset id (defaults to account nonce proof)")
	_ = cmd.MarkFlagRequired("address")
	return cmd
}
