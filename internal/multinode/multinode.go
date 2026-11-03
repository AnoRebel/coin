// Package multinode initializes and runs a local CometBFT devnet.
//
// Every node gets its own home, validator key, node key, and ports, plus the
// same genesis document and a peer list pointing at the other nodes. Apps are
// supplied by the caller, so one devnet can mix the Go app and the Python app.
package multinode

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtcfg "github.com/cometbft/cometbft/config"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	cmtnode "github.com/cometbft/cometbft/node"
	cmtp2p "github.com/cometbft/cometbft/p2p"
	cmtprivval "github.com/cometbft/cometbft/privval"
	cmtproxy "github.com/cometbft/cometbft/proxy"
	cmttypes "github.com/cometbft/cometbft/types"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Node is one initialized devnet home.
type Node struct {
	Index   int
	Home    string
	Config  *cmtcfg.Config
	PV      *cmtprivval.FilePV
	NodeKey *cmtp2p.NodeKey
}

// Cluster is an initialized devnet. Start the nodes with Start.
type Cluster struct {
	Nodes   []*Node
	Genesis ledger.Genesis
	Doc     *cmttypes.GenesisDoc
}

// Init creates count homes with a shared genesis and mutual persistent peers.
// Zero ports pick free localhost ports.
func Init(root, network string, count int, interval time.Duration, p2pBase, rpcBase int) (*Cluster, error) {
	if count < 1 {
		return nil, fmt.Errorf("multinode: at least one node is required")
	}
	if _, err := protocol.NetworkClass(network); err != nil {
		return nil, err
	}
	funder, _, err := protocol.NewKeyPair(network)
	if err != nil {
		return nil, err
	}
	faucet, _, err := protocol.NewKeyPair(network)
	if err != nil {
		return nil, err
	}
	genesis := node.DevnetGenesis(network, uint64(time.Now().Unix()), funder, faucet)
	appState, err := json.Marshal(genesis)
	if err != nil {
		return nil, err
	}

	nodes := make([]*Node, 0, count)
	for i := 0; i < count; i++ {
		home := filepath.Join(root, fmt.Sprintf("node-%d", i))
		cfg := cmtcfg.DefaultConfig()
		cfg.SetRoot(home)
		cmtcfg.EnsureRoot(home)
		p2pPort, err := port(p2pBase, i)
		if err != nil {
			return nil, err
		}
		rpcPort, err := port(rpcBase, i)
		if err != nil {
			return nil, err
		}
		cfg.P2P.ListenAddress = fmt.Sprintf("tcp://127.0.0.1:%d", p2pPort)
		cfg.RPC.ListenAddress = fmt.Sprintf("tcp://127.0.0.1:%d", rpcPort)
		cfg.Consensus.TimeoutCommit = interval
		cfg.P2P.AllowDuplicateIP = true
		cfg.P2P.AddrBookStrict = false
		cfg.P2P.PexReactor = false
		cfg.Instrumentation.Prometheus = false

		pv, err := cmtprivval.LoadOrGenFilePV(cfg.PrivValidatorKeyFile(), cfg.PrivValidatorStateFile(), nil)
		if err != nil {
			return nil, err
		}
		pv.Save()
		key, err := cmtp2p.LoadOrGenNodeKey(cfg.NodeKeyFile())
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, &Node{Index: i, Home: home, Config: cfg, PV: pv, NodeKey: key})
	}

	validators := make([]cmttypes.GenesisValidator, 0, count)
	for _, n := range nodes {
		pub, err := n.PV.GetPubKey()
		if err != nil {
			return nil, err
		}
		validators = append(validators, cmttypes.GenesisValidator{
			Address: pub.Address(), PubKey: pub, Power: 1,
			Name: fmt.Sprintf("node-%d", n.Index),
		})
	}
	doc := &cmttypes.GenesisDoc{
		GenesisTime:     time.Now().UTC(),
		ChainID:         genesis.Chain,
		ConsensusParams: cmttypes.DefaultConsensusParams(),
		Validators:      validators,
		AppState:        appState,
	}

	for _, n := range nodes {
		peers := make([]string, 0, count-1)
		for _, other := range nodes {
			if other.Index == n.Index {
				continue
			}
			_, port, err := net.SplitHostPort(strings.TrimPrefix(other.Config.P2P.ListenAddress, "tcp://"))
			if err != nil {
				return nil, err
			}
			peers = append(peers, fmt.Sprintf("%s@127.0.0.1:%s", other.NodeKey.ID(), port))
		}
		n.Config.P2P.PersistentPeers = strings.Join(peers, ",")
		if err := doc.SaveAs(n.Config.GenesisFile()); err != nil {
			return nil, err
		}
	}
	return &Cluster{Nodes: nodes, Genesis: genesis, Doc: doc}, nil
}

func port(base, index int) (int, error) {
	if base > 0 {
		return base + index, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// Start launches every node. makeApp must return an app and an optional
// closer; the caller closes apps after the nodes stop.
func (c *Cluster) Start(makeApp func(index int) (abci.Application, func() error, error), logger cmtlog.Logger) ([]*cmtnode.Node, error) {
	if logger == nil {
		logger = cmtlog.NewNopLogger()
	}
	started := make([]*cmtnode.Node, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		app, closer, err := makeApp(n.Index)
		if err != nil {
			stopAll(started)
			return nil, err
		}
		_ = closer // the caller owns app lifetime
		cmtNode, err := cmtnode.NewNode(context.Background(), n.Config, n.PV, n.NodeKey,
			cmtproxy.NewLocalClientCreator(app), cmtnode.DefaultGenesisDocProviderFunc(n.Config),
			cmtcfg.DefaultDBProvider, cmtnode.DefaultMetricsProvider(n.Config.Instrumentation), logger)
		if err != nil {
			stopAll(started)
			return nil, err
		}
		if err := cmtNode.Start(); err != nil {
			stopAll(started)
			return nil, err
		}
		started = append(started, cmtNode)
	}
	return started, nil
}

func stopAll(nodes []*cmtnode.Node) {
	for _, n := range nodes {
		n.Stop()
	}
	for _, n := range nodes {
		n.Wait()
	}
}

// Stop shuts every node down.
func Stop(nodes []*cmtnode.Node) { stopAll(nodes) }
