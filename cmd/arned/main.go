// Command arned runs an ARNE node and provides wallet, payment, asset, and
// operator commands.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/api"
	"github.com/anorebel/anorebelcoin/internal/backup"
	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
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
		faucetCmd(), statusCmd(), assetCmd(), proofCmd(), backupCmd(), restoreCmd())
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
			n, err := node.New(node.Config{
				Network: network, Chain: chain, Params: ledger.DefaultParams(),
				Proposer: funder.Address, Faucet: faucetKP, FaucetAmount: 100_000_000_00,
				Devnet: true, BlockInterval: interval,
			}, node.DevnetGenesis(network, uint64(time.Now().Unix()), funder, faucetKP), store)
			if err != nil {
				return err
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

func parseAmount(s string, decimals uint8) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") {
		return 0, errors.New("invalid amount")
	}
	parts := strings.SplitN(s, ".", 2)
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	scale := uint64(1)
	for i := uint8(0); i < decimals; i++ {
		scale *= 10
	}
	if whole > (1<<63)/scale {
		return 0, errors.New("amount too large")
	}
	out := whole * scale
	if len(parts) == 2 {
		frac := parts[1]
		if uint8(len(frac)) > decimals {
			return 0, fmt.Errorf("amount has more than %d decimals", decimals)
		}
		frac += strings.Repeat("0", int(decimals)-len(frac))
		f, err := strconv.ParseUint(frac, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid amount %q", s)
		}
		out += f
	}
	return out, nil
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

func payCmd() *cobra.Command {
	var network, mnemonic, to, asset, amount string
	var memo string
	var wait bool
	cmd := &cobra.Command{
		Use:   "pay",
		Short: "Sign and submit a transfer",
		RunE: func(cmd *cobra.Command, args []string) error {
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
			payload := map[string]any{"asset": asset, "to": to, "amount": units}
			if memo != "" {
				payload["memo"] = memo
			}
			tx := protocol.Tx{
				Version: protocol.CurrentTxVersion, Network: network, Chain: chain,
				Type: protocol.TxTransfer, Signer: kp.Address, Nonce: acc.Nonce,
				CreatedAt: uint64(time.Now().Unix()), ExpiresAt: uint64(time.Now().Unix()) + 3600,
				FeeAsset: protocol.NativeAsset(network), MaxFee: 10_000, Payload: payload,
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
		},
	}
	cmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	cmd.Flags().StringVar(&mnemonic, "mnemonic", "", "sender mnemonic")
	cmd.Flags().StringVar(&to, "to", "", "recipient address")
	cmd.Flags().StringVar(&asset, "asset", "", "asset id (for example arce:devnet)")
	cmd.Flags().StringVar(&amount, "amount", "", "human amount, for example 12.5")
	cmd.Flags().StringVar(&memo, "memo", "", "plaintext memo (public on chain)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the receipt")
	for _, f := range []string{"mnemonic", "to", "asset", "amount"} {
		_ = cmd.MarkFlagRequired(f)
	}
	return cmd
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
