package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	cmtnode "github.com/cometbft/cometbft/node"
	cmtp2p "github.com/cometbft/cometbft/p2p"
	cmtprivval "github.com/cometbft/cometbft/privval"
	cmtproxy "github.com/cometbft/cometbft/proxy"
	cmtrpc "github.com/cometbft/cometbft/rpc/client/http"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/api"
	"github.com/anorebel/anorebelcoin/internal/consensus"
	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

func startCmd() *cobra.Command {
	var (
		home           string
		network        string
		mnemonic       string
		apiAddr        string
		rpcAddr        string
		p2pAddr        string
		dataDir        string
		interval       time.Duration
		ephemeral      bool
		apiKeySpecs    []string
		allowedOrigins []string
		rateLimit      int
		keepBlocks     uint64
		webhookURLs    []string
		webhookKey     string
	)
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Run a CometBFT consensus node",
		Long: "Run a single-validator CometBFT node with the ARNE ledger as the ABCI app.\n" +
			"Only devnet genesis is generated automatically; other networks must provide\n" +
			"config/genesis.json.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if ephemeral {
				dir, err := os.MkdirTemp("", "arne-consensus-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(dir)
				home = dir
			}
			cfg := cmtcfg.DefaultConfig()
			cfg.SetRoot(home)
			cmtcfg.EnsureRoot(home)
			cfg.RPC.ListenAddress = rpcAddr
			cfg.P2P.ListenAddress = p2pAddr
			cfg.Consensus.TimeoutCommit = interval
			cfg.Instrumentation.Prometheus = false

			genDoc, created, mnemonic, err := loadOrCreateGenesis(cfg, network, mnemonic)
			if err != nil {
				return err
			}
			appGenesis, err := ledgerGenesis(genDoc)
			if err != nil {
				return err
			}

			apiKeys, err := parseAPIKeys(apiKeySpecs)
			if err != nil {
				return err
			}

			var store storage.Store
			if ephemeral {
				store = storage.NewMemory()
			} else {
				if dataDir == "" {
					dataDir = filepath.Join(home, "app")
				}
				store, err = storage.OpenPebble(dataDir)
				if err != nil {
					return err
				}
				if err := store.Migrate(storage.SchemaVersion); err != nil {
					_ = store.Close()
					return err
				}
				defer store.Close()
			}

			var faucet *protocol.KeyPair
			seed, err := webhookSeed(webhookURLs, webhookKey)
			if err != nil {
				return err
			}
			class, err := protocol.NetworkClass(appGenesis.Network)
			if err != nil {
				return err
			}
			devnet := class == protocol.ClassDevnet
			if devnet && mnemonic != "" {
				faucet, err = protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 1, appGenesis.Network)
				if err != nil {
					return err
				}
			}
			n, err := node.New(node.Config{
				Network: appGenesis.Network, Chain: appGenesis.Chain, Params: appGenesis.Params,
				Faucet: faucet, FaucetAmount: 10_000_000_000, Devnet: devnet,
				KeepBlocks: keepBlocks, Webhooks: webhookURLs, WebhookSeed: seed,
			}, appGenesis, store)
			if err != nil {
				return err
			}
			defer n.CloseWebhooks()
			if len(webhookURLs) > 0 {
				fmt.Printf("webhooks: %d endpoint(s), signing key %s\n", len(webhookURLs), n.WebhookPublicKey())
			}

			pv, err := cmtprivval.LoadOrGenFilePV(cfg.PrivValidatorKeyFile(), cfg.PrivValidatorStateFile(), nil)
			if err != nil {
				return err
			}
			pv.Save()
			nodeKey, err := cmtp2p.LoadOrGenNodeKey(cfg.NodeKeyFile())
			if err != nil {
				return err
			}
			logger := cmtlog.NewTMLogger(cmtlog.NewSyncWriter(os.Stderr))
			app := consensus.NewApp(n, appGenesis.Network, appGenesis.Chain, 500)
			cmtNode, err := cmtnode.NewNode(context.Background(), cfg, pv, nodeKey,
				cmtproxy.NewLocalClientCreator(app), cmtnode.DefaultGenesisDocProviderFunc(cfg),
				cmtcfg.DefaultDBProvider, cmtnode.DefaultMetricsProvider(cfg.Instrumentation), logger)
			if err != nil {
				return err
			}
			if err := cmtNode.Start(); err != nil {
				return err
			}
			defer func() {
				cmtNode.Stop()
				cmtNode.Wait()
			}()

			rpc, err := cmtrpc.New(rpcAddr)
			if err != nil {
				return err
			}
			n.SetBroadcast(func(raw []byte) error {
				res, err := rpc.BroadcastTxSync(context.Background(), cmttypes.Tx(raw))
				if err != nil {
					return err
				}
				if res.Code != 0 {
					return errors.New(res.Log)
				}
				return nil
			})
			n.SetMempoolSize(func() int { return cmtNode.Mempool().Size() })

			srv := api.New(n, api.Config{
				Devnet: devnet, RateLimit: rateLimit,
				APIKeys: apiKeys, AllowedOrigins: allowedOrigins,
			})
			go func() {
				if err := srv.Start(apiAddr); err != nil && !errors.Is(err, io.EOF) {
					fmt.Fprintln(os.Stderr, "api:", err)
				}
			}()

			fmt.Printf("network: %s\nchain:   %s\nhome:    %s\napi:     http://%s\nrpc:     %s\n",
				appGenesis.Network, appGenesis.Chain, home, apiAddr, rpcAddr)
			if created {
				fmt.Printf("genesis: created %s (devnet allocations, %s)\n", cfg.GenesisFile(), mnemonic)
			}

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
			<-stop
			fmt.Println("\nshutting down")
			return nil
		},
	}
	cmd.Flags().StringVar(&home, "home", ".arne", "node home (config, data, keys)")
	cmd.Flags().StringVar(&network, "network", "arne:devnet", "network id used when creating genesis")
	cmd.Flags().StringVar(&mnemonic, "mnemonic", "", "devnet mnemonic for genesis allocations and faucet")
	cmd.Flags().StringVar(&apiAddr, "listen", "127.0.0.1:8080", "API listen address")
	cmd.Flags().StringVar(&rpcAddr, "rpc", "tcp://127.0.0.1:26657", "CometBFT RPC listen address")
	cmd.Flags().StringVar(&p2pAddr, "p2p", "tcp://0.0.0.0:26656", "CometBFT P2P listen address")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "app data directory (default <home>/app)")
	cmd.Flags().DurationVar(&interval, "block-interval", time.Second, "consensus commit timeout")
	cmd.Flags().BoolVar(&ephemeral, "ephemeral", false, "temporary home and in-memory app state")
	cmd.Flags().StringArrayVar(&apiKeySpecs, "api-key", nil, "API key as key=read,write[,admin] (repeatable)")
	cmd.Flags().StringArrayVar(&allowedOrigins, "allow-origin", nil, "CORS origin to allow (repeatable)")
	cmd.Flags().IntVar(&rateLimit, "rate-limit", 200, "requests per second per IP (0 disables)")
	cmd.Flags().Uint64Var(&keepBlocks, "keep-blocks", 0, "keep only the newest N ledger blocks (0 archives all)")
	cmd.Flags().StringArrayVar(&webhookURLs, "webhook", nil, "webhook endpoint URL (repeatable)")
	cmd.Flags().StringVar(&webhookKey, "webhook-key", "", "hex Ed25519 seed for signing webhooks")
	return cmd
}

// loadOrCreateGenesis loads config/genesis.json, creating a funded devnet the
// first time. It returns the mnemonic used for the allocations.
func loadOrCreateGenesis(cfg *cmtcfg.Config, network, mnemonic string) (*cmttypes.GenesisDoc, bool, string, error) {
	path := cfg.GenesisFile()
	if _, err := os.Stat(path); err == nil {
		doc, err := cmttypes.GenesisDocFromFile(path)
		return doc, false, mnemonic, err
	}
	if network != "arne:devnet" {
		return nil, false, "", fmt.Errorf("no genesis at %s; copy the %s genesis file there", path, network)
	}
	if mnemonic == "" {
		_, phrase, err := protocol.NewKeyPair(network)
		if err != nil {
			return nil, false, "", err
		}
		mnemonic = phrase
	}
	funder, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 0, network)
	if err != nil {
		return nil, false, "", err
	}
	faucet, err := protocol.KeyPairFromMnemonic(mnemonic, "", 0, 0, 1, network)
	if err != nil {
		return nil, false, "", err
	}
	genesis := node.DevnetGenesis(network, uint64(time.Now().Unix()), funder, faucet)
	state, err := json.Marshal(genesis)
	if err != nil {
		return nil, false, "", err
	}
	pv, err := cmtprivval.LoadOrGenFilePV(cfg.PrivValidatorKeyFile(), cfg.PrivValidatorStateFile(), nil)
	if err != nil {
		return nil, false, "", err
	}
	pub, err := pv.GetPubKey()
	if err != nil {
		return nil, false, "", err
	}
	doc := &cmttypes.GenesisDoc{
		GenesisTime:     time.Now().UTC(),
		ChainID:         genesis.Chain,
		ConsensusParams: cmttypes.DefaultConsensusParams(),
		Validators: []cmttypes.GenesisValidator{{
			Address: pub.Address(), PubKey: pub, Power: 1, Name: "arne",
		}},
		AppState: state,
	}
	if err := doc.SaveAs(path); err != nil {
		return nil, false, "", err
	}
	return doc, true, mnemonic, nil
}

func ledgerGenesis(doc *cmttypes.GenesisDoc) (ledger.Genesis, error) {
	var g ledger.Genesis
	if len(doc.AppState) == 0 {
		return g, errors.New("genesis app_state is empty")
	}
	if err := json.Unmarshal(doc.AppState, &g); err != nil {
		return g, fmt.Errorf("genesis app_state: %w", err)
	}
	if g.Network == "" {
		return g, errors.New("genesis app_state has no network")
	}
	if g.Chain != doc.ChainID {
		return g, fmt.Errorf("genesis app_state chain %q does not match %q", g.Chain, doc.ChainID)
	}
	return g, nil
}
