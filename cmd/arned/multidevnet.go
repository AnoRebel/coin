package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/consensus"
	"github.com/anorebel/anorebelcoin/internal/multinode"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

func multiDevnetCmd() *cobra.Command {
	var (
		root        string
		network     string
		count       int
		pythonNodes int
		pythonCmd   string
		interval    time.Duration
		p2pBase     int
		rpcBase     int
		keepBlocks  uint64
	)
	cmd := &cobra.Command{
		Use:   "multi-devnet",
		Short: "Run a local multi-node CometBFT devnet",
		Long: "Initialize several CometBFT validators that share one genesis and peer " +
			"with each other. With --python-nodes, the first N validators run the " +
			"Python ABCI app instead of the Go app, so a mixed devnet must agree on " +
			"every block hash or consensus stops.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if pythonNodes < 0 || pythonNodes > count {
				return fmt.Errorf("--python-nodes must be between 0 and --nodes")
			}
			cluster, err := multinode.Init(root, network, count, interval, p2pBase, rpcBase)
			if err != nil {
				return err
			}

			closers := make([]func(), 0, count)
			defer func() {
				for i := len(closers) - 1; i >= 0; i-- {
					closers[i]()
				}
			}()
			nodes, err := cluster.Start(func(index int) (abci.Application, func() error, error) {
				home := cluster.Nodes[index].Home
				if index < pythonNodes {
					genesis := cluster.Nodes[index].Config.GenesisFile()
					dataDir := filepath.Join(home, "python")
					command := strings.TrimSpace(pythonCmd)
					if command == "" {
						command = "uv run python -m arne.abci"
					}
					command = fmt.Sprintf("%s --genesis %q --data-dir %q", command, genesis, dataDir)
					app, err := consensus.NewPythonApp(command, cluster.Genesis.Network, cluster.Genesis.Chain)
					if err != nil {
						return nil, nil, err
					}
					closers = append(closers, func() { _ = app.Close() })
					return app, app.Close, nil
				}
				store, err := storage.OpenPebble(filepath.Join(home, "app"))
				if err != nil {
					return nil, nil, err
				}
				if err := store.Migrate(storage.SchemaVersion); err != nil {
					_ = store.Close()
					return nil, nil, err
				}
				closers = append(closers, func() { _ = store.Close() })
				n, err := node.New(node.Config{
					Network: cluster.Genesis.Network, Chain: cluster.Genesis.Chain,
					Params: cluster.Genesis.Params, Devnet: true, KeepBlocks: keepBlocks,
				}, cluster.Genesis, store)
				if err != nil {
					return nil, nil, err
				}
				return consensus.NewApp(n, cluster.Genesis.Network, cluster.Genesis.Chain, 500), nil, nil
			}, cmtlog.NewTMLogger(cmtlog.NewSyncWriter(os.Stderr)))
			if err != nil {
				return err
			}
			defer multinode.Stop(nodes)

			fmt.Printf("network: %s\nchain:   %s\nroot:    %s\n",
				cluster.Genesis.Network, cluster.Genesis.Chain, root)
			for _, n := range cluster.Nodes {
				kind := "go"
				if n.Index < pythonNodes {
					kind = "python"
				}
				fmt.Printf("node %d (%s): home=%s p2p=%s rpc=%s\n", n.Index, kind, n.Home,
					n.Config.P2P.ListenAddress, n.Config.RPC.ListenAddress)
			}
			fmt.Println("press Ctrl-C to stop")

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
			<-stop
			fmt.Println("\nshutting down")
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", ".arne-multidevnet", "devnet root directory")
	cmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	cmd.Flags().IntVar(&count, "nodes", 2, "number of validators")
	cmd.Flags().IntVar(&pythonNodes, "python-nodes", 0, "first N validators run the Python app")
	cmd.Flags().StringVar(&pythonCmd, "python-cmd", "uv run python -m arne.abci",
		"Python ABCI command (run from the repository root)")
	cmd.Flags().DurationVar(&interval, "block-interval", 300*time.Millisecond, "consensus commit timeout")
	cmd.Flags().IntVar(&p2pBase, "p2p-base", 0, "first P2P port (0 picks free ports)")
	cmd.Flags().IntVar(&rpcBase, "rpc-base", 0, "first RPC port (0 picks free ports)")
	cmd.Flags().Uint64Var(&keepBlocks, "keep-blocks", 0, "keep only the newest N ledger blocks (0 archives all)")
	return cmd
}
