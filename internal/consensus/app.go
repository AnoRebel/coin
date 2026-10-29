// Package consensus adapts the ARNE node to CometBFT's ABCI 2.0 application
// interface. CometBFT owns the validator set, networking, and finality; this
// package owns transaction validation, ordering, and state.
package consensus

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"

	"github.com/anorebel/anorebelcoin/internal/node"
)

// App is the ABCI application. It is safe for concurrent use; the node holds
// the lock and CometBFT serializes consensus calls.
type App struct {
	*abci.BaseApplication
	node    *node.Node
	network string
	chain   string
	maxTxs  int
}

// NewApp wraps a node. maxTxs bounds a proposal; zero uses the node default.
func NewApp(n *node.Node, network, chain string, maxTxs int) *App {
	if maxTxs <= 0 {
		maxTxs = 500
	}
	return &App{BaseApplication: abci.NewBaseApplication(), node: n, network: network, chain: chain, maxTxs: maxTxs}
}

// Info reports the last committed height and app hash.
func (a *App) Info(context.Context, *abci.InfoRequest) (*abci.InfoResponse, error) {
	st := a.node.Status()
	return &abci.InfoResponse{
		Data:             "arne/1",
		Version:          "0.1.0",
		AppVersion:       1,
		LastBlockHeight:  int64(st.Height),
		LastBlockAppHash: a.node.Root(),
	}, nil
}

// InitChain is called once for a fresh chain. The node already built genesis
// state from the same file, so there is nothing to do.
func (a *App) InitChain(_ context.Context, req *abci.InitChainRequest) (*abci.InitChainResponse, error) {
	if a.chain != "" && req.ChainId != "" && req.ChainId != a.chain {
		return nil, fmt.Errorf("genesis chain %q does not match node chain %q", req.ChainId, a.chain)
	}
	return &abci.InitChainResponse{AppHash: a.node.Root()}, nil
}

// Query serves read-only app queries. Consensus does not use it; it lets an
// operator inspect state through CometBFT's /abci_query.
func (a *App) Query(_ context.Context, req *abci.QueryRequest) (*abci.QueryResponse, error) {
	switch req.Path {
	case "/status":
		st := a.node.Status()
		b, err := json.Marshal(st)
		if err != nil {
			return &abci.QueryResponse{Code: 1, Log: err.Error()}, nil
		}
		return &abci.QueryResponse{Code: 0, Value: b, Height: int64(st.Height)}, nil
	case "/balance":
		var q struct{ Address, Asset string }
		if err := json.Unmarshal(req.Data, &q); err != nil {
			return &abci.QueryResponse{Code: 1, Log: "balance query wants JSON {address,asset}"}, nil
		}
		b, err := json.Marshal(map[string]uint64{"balance": a.node.Balance(q.Address, q.Asset)})
		if err != nil {
			return &abci.QueryResponse{Code: 1, Log: err.Error()}, nil
		}
		return &abci.QueryResponse{Code: 0, Value: b, Height: int64(a.node.Status().Height)}, nil
	default:
		return &abci.QueryResponse{Code: 1, Log: "unknown query path: " + req.Path}, nil
	}
}

// CheckTx admits a transaction to the mempool.
func (a *App) CheckTx(_ context.Context, req *abci.CheckTxRequest) (*abci.CheckTxResponse, error) {
	if _, err := a.node.CheckRaw(req.Tx); err != nil {
		return &abci.CheckTxResponse{Code: 1, Codespace: "arne", Log: err.Error()}, nil
	}
	return &abci.CheckTxResponse{Codespace: "arne"}, nil
}

// PrepareProposal orders and filters the mempool transactions.
func (a *App) PrepareProposal(_ context.Context, req *abci.PrepareProposalRequest) (*abci.PrepareProposalResponse, error) {
	txs := a.node.PrepareTxs(req.Txs, a.maxTxs, int(req.MaxTxBytes))
	return &abci.PrepareProposalResponse{Txs: txs}, nil
}

// ProcessProposal accepts a block whose transactions all decode for this
// network. Invalid transactions are allowed to fail during execution (they are
// free and roll back), so this only rejects malformed data.
func (a *App) ProcessProposal(_ context.Context, req *abci.ProcessProposalRequest) (*abci.ProcessProposalResponse, error) {
	for _, raw := range req.Txs {
		if _, err := a.node.DecodeTx(raw); err != nil {
			return &abci.ProcessProposalResponse{Status: abci.PROCESS_PROPOSAL_STATUS_REJECT}, nil
		}
	}
	return &abci.ProcessProposalResponse{Status: abci.PROCESS_PROPOSAL_STATUS_ACCEPT}, nil
}

// FinalizeBlock executes the decided block and returns the new app hash.
func (a *App) FinalizeBlock(_ context.Context, req *abci.FinalizeBlockRequest) (*abci.FinalizeBlockResponse, error) {
	proposer := hex.EncodeToString(req.ProposerAddress)
	_, receipts, err := a.node.FinalizeTxs(req.Txs, uint64(req.Height), uint64(req.Time.Unix()), proposer)
	if err != nil {
		return nil, err
	}
	results := make([]*abci.ExecTxResult, len(receipts))
	for i, r := range receipts {
		res := &abci.ExecTxResult{Codespace: "arne", Log: r.Error}
		if !r.Success {
			res.Code = 1
			res.Info = string(r.Code)
		}
		results[i] = res
	}
	return &abci.FinalizeBlockResponse{TxResults: results, AppHash: a.node.Root()}, nil
}

// Commit persists the block and returns the app hash.
func (a *App) Commit(context.Context, *abci.CommitRequest) (*abci.CommitResponse, error) {
	if err := a.node.CommitBlock(); err != nil {
		return nil, err
	}
	return &abci.CommitResponse{}, nil
}
