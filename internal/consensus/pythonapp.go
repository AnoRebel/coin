package consensus

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	abci "github.com/cometbft/cometbft/abci/types"
)

// PythonApp runs the Python parity node as the ABCI application.
//
// CometBFT owns consensus and networking. This adapter forwards each ABCI call
// over a line-delimited JSON pipe to `python -m arne.abci`, which executes the
// Python ledger and returns its app hash. A Go validator and a Python validator
// on the same chain therefore agree on every finalized block, or consensus
// stops because their app hashes differ.
type PythonApp struct {
	*abci.BaseApplication

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	network string
	chain   string
}

// pyResponse carries every ABCI response shape; unused fields stay zero.
type pyResponse struct {
	Error     string       `json:"error"`
	Height    int64        `json:"height"`
	AppHash   string       `json:"app_hash"`
	Code      uint32       `json:"code"`
	Codespace string       `json:"codespace"`
	Log       string       `json:"log"`
	Info      string       `json:"info"`
	Txs       []string     `json:"txs"`
	Status    string       `json:"status"`
	Value     string       `json:"value"`
	TxResults []pyTxResult `json:"tx_results"`
}

type pyTxResult struct {
	Code      uint32 `json:"code"`
	Codespace string `json:"codespace"`
	Log       string `json:"log"`
	Info      string `json:"info"`
}

// NewPythonApp starts the Python app process. command is run through the
// shell, so operators can pass flags and environment assignments.
func NewPythonApp(command, network, chain string) (*PythonApp, error) {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("python abci: %w", err)
	}
	return &PythonApp{
		BaseApplication: abci.NewBaseApplication(),
		cmd:             cmd,
		stdin:           stdin,
		stdout:          bufio.NewReaderSize(stdout, 1<<20),
		network:         network,
		chain:           chain,
	}, nil
}

// Close stops the child process.
func (a *PythonApp) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return nil
	}
	_ = a.stdin.Close()
	err := a.cmd.Process.Kill()
	_ = a.cmd.Wait()
	a.cmd = nil
	return err
}

func (a *PythonApp) call(request any) (pyResponse, error) {
	var response pyResponse
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil {
		return response, errors.New("python abci: process is closed")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	if _, err := a.stdin.Write(append(body, '\n')); err != nil {
		return response, fmt.Errorf("python abci: write: %w", err)
	}
	line, err := a.stdout.ReadBytes('\n')
	if err != nil {
		return response, fmt.Errorf("python abci: read: %w", err)
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return response, fmt.Errorf("python abci: bad response %q: %w", line, err)
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

// Info reports the last committed height and app hash.
func (a *PythonApp) Info(context.Context, *abci.InfoRequest) (*abci.InfoResponse, error) {
	response, err := a.call(map[string]any{"method": "info"})
	if err != nil {
		return nil, err
	}
	hash, err := hex.DecodeString(response.AppHash)
	if err != nil {
		return nil, fmt.Errorf("python abci: app hash: %w", err)
	}
	return &abci.InfoResponse{
		Data: "arne/1", Version: "0.1.0", AppVersion: 1,
		LastBlockHeight: response.Height, LastBlockAppHash: hash,
	}, nil
}

// InitChain verifies the chain id and returns the genesis app hash.
func (a *PythonApp) InitChain(_ context.Context, req *abci.InitChainRequest) (*abci.InitChainResponse, error) {
	response, err := a.call(map[string]any{
		"method": "init_chain", "chain_id": req.ChainId,
	})
	if err != nil {
		return nil, err
	}
	hash, err := hex.DecodeString(response.AppHash)
	if err != nil {
		return nil, fmt.Errorf("python abci: app hash: %w", err)
	}
	return &abci.InitChainResponse{AppHash: hash}, nil
}

// CheckTx admits a transaction to the mempool.
func (a *PythonApp) CheckTx(_ context.Context, req *abci.CheckTxRequest) (*abci.CheckTxResponse, error) {
	response, err := a.call(map[string]any{
		"method": "check_tx", "tx": hex.EncodeToString(req.Tx),
	})
	if err != nil {
		return nil, err
	}
	return &abci.CheckTxResponse{
		Code: response.Code, Codespace: response.Codespace,
		Log: response.Log, Info: response.Info,
	}, nil
}

// PrepareProposal orders and filters the mempool transactions.
func (a *PythonApp) PrepareProposal(_ context.Context, req *abci.PrepareProposalRequest) (*abci.PrepareProposalResponse, error) {
	txs := make([]string, len(req.Txs))
	for i, raw := range req.Txs {
		txs[i] = hex.EncodeToString(raw)
	}
	response, err := a.call(map[string]any{
		"method": "prepare_proposal", "txs": txs, "max_tx_bytes": req.MaxTxBytes,
	})
	if err != nil {
		return nil, err
	}
	out := make([][]byte, len(response.Txs))
	for i, tx := range response.Txs {
		raw, err := hex.DecodeString(tx)
		if err != nil {
			return nil, fmt.Errorf("python abci: proposal tx: %w", err)
		}
		out[i] = raw
	}
	return &abci.PrepareProposalResponse{Txs: out}, nil
}

// ProcessProposal accepts a block whose transactions all decode for this
// network. Invalid transactions are allowed to fail during execution.
func (a *PythonApp) ProcessProposal(_ context.Context, req *abci.ProcessProposalRequest) (*abci.ProcessProposalResponse, error) {
	txs := make([]string, len(req.Txs))
	for i, raw := range req.Txs {
		txs[i] = hex.EncodeToString(raw)
	}
	response, err := a.call(map[string]any{
		"method": "process_proposal", "txs": txs,
	})
	if err != nil {
		return nil, err
	}
	status := abci.PROCESS_PROPOSAL_STATUS_REJECT
	if response.Status == "accept" {
		status = abci.PROCESS_PROPOSAL_STATUS_ACCEPT
	}
	return &abci.ProcessProposalResponse{Status: status}, nil
}

// FinalizeBlock executes the decided block and returns the new app hash.
func (a *PythonApp) FinalizeBlock(_ context.Context, req *abci.FinalizeBlockRequest) (*abci.FinalizeBlockResponse, error) {
	txs := make([]string, len(req.Txs))
	for i, raw := range req.Txs {
		txs[i] = hex.EncodeToString(raw)
	}
	response, err := a.call(map[string]any{
		"method": "finalize_block", "txs": txs,
		"height": req.Height, "time": req.Time.Unix(),
		"proposer": hex.EncodeToString(req.ProposerAddress),
	})
	if err != nil {
		return nil, err
	}
	hash, err := hex.DecodeString(response.AppHash)
	if err != nil {
		return nil, fmt.Errorf("python abci: app hash: %w", err)
	}
	results := make([]*abci.ExecTxResult, len(response.TxResults))
	for i, result := range response.TxResults {
		results[i] = &abci.ExecTxResult{
			Code: result.Code, Codespace: result.Codespace,
			Log: result.Log, Info: result.Info,
		}
	}
	return &abci.FinalizeBlockResponse{TxResults: results, AppHash: hash}, nil
}

// Commit persists the block staged by FinalizeBlock.
func (a *PythonApp) Commit(context.Context, *abci.CommitRequest) (*abci.CommitResponse, error) {
	if _, err := a.call(map[string]any{"method": "commit"}); err != nil {
		return nil, err
	}
	return &abci.CommitResponse{}, nil
}

// Query serves read-only app queries.
func (a *PythonApp) Query(_ context.Context, req *abci.QueryRequest) (*abci.QueryResponse, error) {
	response, err := a.call(map[string]any{
		"method": "query", "path": req.Path, "data": hex.EncodeToString(req.Data),
	})
	if err != nil {
		return nil, err
	}
	value, err := hex.DecodeString(response.Value)
	if err != nil {
		return nil, fmt.Errorf("python abci: query value: %w", err)
	}
	return &abci.QueryResponse{
		Code: response.Code, Log: response.Log,
		Value: value, Height: response.Height,
	}, nil
}
