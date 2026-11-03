package multinode_test

import (
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtnode "github.com/cometbft/cometbft/node"

	"github.com/anorebel/anorebelcoin/internal/consensus"
	"github.com/anorebel/anorebelcoin/internal/multinode"
	"github.com/anorebel/anorebelcoin/internal/node"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

const testNetwork = "arne:devnet"

func waitHeight(t *testing.T, nodes []*cmtnode.Node, height int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ready := true
		for _, n := range nodes {
			if n.BlockStore().Height() < height {
				ready = false
			}
		}
		if ready {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for i, n := range nodes {
		t.Logf("node %d height %d", i, n.BlockStore().Height())
	}
	t.Fatalf("nodes did not reach height %d", height)
}

func assertSameChain(t *testing.T, nodes []*cmtnode.Node, height int64) {
	t.Helper()
	for h := int64(1); h <= height; h++ {
		var want string
		for i, n := range nodes {
			block, _ := n.BlockStore().LoadBlock(h)
			if block == nil {
				t.Fatalf("node %d has no block %d", i, h)
			}
			if i == 0 {
				want = block.Hash().String()
				continue
			}
			if got := block.Hash().String(); got != want {
				t.Fatalf("block %d hash %s on node %d, want %s", h, got, i, want)
			}
		}
	}
}

func TestTwoNodeDevnetFinalizesSameChain(t *testing.T) {
	cluster, err := multinode.Init(t.TempDir(), testNetwork, 2, 200*time.Millisecond, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := cluster.Start(func(int) (abci.Application, func() error, error) {
		n, err := node.New(node.Config{
			Network: cluster.Genesis.Network, Chain: cluster.Genesis.Chain,
			Params: cluster.Genesis.Params, Devnet: true,
		}, cluster.Genesis, storage.NewMemory())
		if err != nil {
			return nil, nil, err
		}
		return consensus.NewApp(n, cluster.Genesis.Network, cluster.Genesis.Chain, 500), nil, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer multinode.Stop(nodes)

	waitHeight(t, nodes, 3)
	assertSameChain(t, nodes, 3)
}

func TestMixedGoPythonDevnetFinalizesSameChain(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv is not installed")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := multinode.Init(t.TempDir(), testNetwork, 2, 200*time.Millisecond, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	var python *consensus.PythonApp
	defer func() {
		if python != nil {
			_ = python.Close()
		}
	}()

	nodes, err := cluster.Start(func(index int) (abci.Application, func() error, error) {
		if index == 0 {
			n, err := node.New(node.Config{
				Network: cluster.Genesis.Network, Chain: cluster.Genesis.Chain,
				Params: cluster.Genesis.Params, Devnet: true,
			}, cluster.Genesis, storage.NewMemory())
			if err != nil {
				return nil, nil, err
			}
			return consensus.NewApp(n, cluster.Genesis.Network, cluster.Genesis.Chain, 500), nil, nil
		}
		genesis := cluster.Nodes[index].Config.GenesisFile()
		command := "uv run --project " + repo + " python -m arne.abci --genesis " + genesis
		python, err = consensus.NewPythonApp(command, cluster.Genesis.Network, cluster.Genesis.Chain)
		if err != nil {
			return nil, nil, err
		}
		return python, python.Close, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer multinode.Stop(nodes)

	waitHeight(t, nodes, 3)
	assertSameChain(t, nodes, 3)
	t.Logf("mixed devnet finalized 3 blocks at %s", hex.EncodeToString(mustHash(t, nodes[0], 3)))
}

func mustHash(t *testing.T, n *cmtnode.Node, height int64) []byte {
	t.Helper()
	block, _ := n.BlockStore().LoadBlock(height)
	if block == nil {
		t.Fatalf("no block %d", height)
	}
	return block.Hash()
}
