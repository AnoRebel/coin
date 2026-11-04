// Package node wires the ledger, mempool, block production, and persistence.
package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/smt"
	"github.com/anorebel/anorebelcoin/internal/storage"
	"github.com/anorebel/anorebelcoin/internal/webhook"
)

// Config configures a node.
type Config struct {
	Network       string
	Chain         string
	Params        ledger.Params
	Proposer      string
	MaxBlockTxs   int
	BlockInterval time.Duration
	Faucet        *protocol.KeyPair
	FaucetAmount  uint64
	Devnet        bool
	// KeepBlocks keeps the newest N blocks and prunes older ones; 0 archives
	// all blocks.
	KeepBlocks uint64
	// Webhooks delivers signed transaction notifications to these URLs.
	Webhooks []string
	// WebhookSeed is the 32-byte Ed25519 seed used to sign webhook bodies.
	WebhookSeed []byte
	// BlockSink receives committed blocks. Submit must not block; a nil sink
	// disables indexing.
	BlockSink BlockSink
}

// BlockSink is an optional, non-consensus consumer of committed blocks.
type BlockSink interface {
	Submit(ledger.Block)
}

// Event is a broadcast block notification.
type Event struct {
	Type     string          `json:"type"`
	Height   uint64          `json:"height"`
	Time     uint64          `json:"time"`
	Hash     string          `json:"hash"`
	TxHashes []string        `json:"tx_hashes,omitempty"`
	Receipt  *ledger.Receipt `json:"receipt,omitempty"`
	Index    int             `json:"index,omitempty"`
}

// Stream maps an event to the subscription stream that carries it.
func (e Event) Stream() string {
	switch e.Type {
	case "transaction":
		return "transactions"
	case "message":
		return "messages"
	default:
		return "blocks"
	}
}

// Node is a single-process ARNE node. Consensus is pluggable; this service owns
// deterministic block production and all query surfaces.
type Node struct {
	mu       sync.RWMutex
	cfg      Config
	state    *ledger.State
	store    storage.Store
	mempool  map[string]protocol.Tx
	receipts map[string]ledger.Receipt
	blocks   map[uint64]ledger.Block
	subs     map[chan Event]struct{}
	webhooks *webhook.Dispatcher
	halted   string

	// submit and mempoolSize let an external consensus engine own block
	// production and the mempool (see internal/consensus).
	submit      func(raw []byte) error
	mempoolSize func() int
	pending     *ledger.Block
}

// New opens or initializes a node.
func New(cfg Config, genesis ledger.Genesis, store storage.Store) (*Node, error) {
	if cfg.MaxBlockTxs <= 0 {
		cfg.MaxBlockTxs = 500
	}
	n := &Node{
		cfg: cfg, store: store,
		mempool:  map[string]protocol.Tx{},
		receipts: map[string]ledger.Receipt{},
		blocks:   map[uint64]ledger.Block{},
		subs:     map[chan Event]struct{}{},
	}
	snap, height, ok, err := store.LoadState()
	if err != nil {
		return nil, err
	}
	if ok && height > 0 {
		s, err := ledger.StateFromSnapshot(cfg.Network, cfg.Chain, cfg.Params, snap)
		if err != nil {
			return nil, err
		}
		n.state = s
		if err := n.loadBlocks(); err != nil {
			return nil, err
		}
		n.checkStoredRoot(height)
		if err := n.startWebhooks(); err != nil {
			return nil, err
		}
		return n, nil
	}
	s, err := ledger.NewGenesis(genesis)
	if err != nil {
		return nil, err
	}
	n.state = s
	if err := store.SaveState(0, s.Snapshot()); err != nil {
		return nil, err
	}
	if err := n.startWebhooks(); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *Node) startWebhooks() error {
	if len(n.cfg.Webhooks) == 0 {
		return nil
	}
	signer, err := webhook.NewSigner(n.cfg.WebhookSeed)
	if err != nil {
		return err
	}
	d, err := webhook.New(webhook.Config{Endpoints: n.cfg.Webhooks, Signer: signer})
	if err != nil {
		return err
	}
	d.Start()
	n.webhooks = d
	return nil
}

// WebhookPublicKey returns the key receivers pin, or empty when disabled.
func (n *Node) WebhookPublicKey() string {
	if n.webhooks == nil {
		return ""
	}
	return n.webhooks.PublicKey()
}

// CloseWebhooks drains queued deliveries. Call after the API server stops.
func (n *Node) CloseWebhooks() {
	if n.webhooks != nil {
		n.webhooks.Close()
	}
}

func (n *Node) loadBlocks() error {
	height := n.state.Height()
	for h := uint64(1); h <= height; h++ {
		b, ok, err := n.store.Block(h)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		n.blocks[h] = b
		for i, hash := range b.TxHashes {
			if i < len(b.Receipts) {
				n.receipts[hash] = b.Receipts[i]
			}
		}
	}
	return nil
}

// checkStoredRoot halts when the persisted last block does not match the state
// that was loaded. A mismatch means the data directory was corrupted or a
// divergent snapshot was restored; continuing would silently fork.
func (n *Node) checkStoredRoot(height uint64) {
	last, ok, err := n.store.LastBlock()
	if err != nil {
		n.halted = "storage failure: " + err.Error()
		return
	}
	if !ok {
		n.halted = fmt.Sprintf("state-root divergence: state height %d has no stored block", height)
		return
	}
	root := n.state.Root()
	if last.Height != height || last.StateRoot != hex.EncodeToString(root[:]) {
		n.halted = fmt.Sprintf(
			"state-root divergence: block %d root %s, state %d root %s",
			last.Height, last.StateRoot, height, hex.EncodeToString(root[:]),
		)
	}
}

// State exposes the ledger for read-only callers. Callers must not mutate it.
func (n *Node) State() *ledger.State {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state
}

// Status is the node status read model.
type Status struct {
	Network   string `json:"network"`
	Chain     string `json:"chain"`
	Height    uint64 `json:"height"`
	StateRoot string `json:"state_root"`
	Time      uint64 `json:"time"`
	Mempool   int    `json:"mempool"`
	Halted    string `json:"halted,omitempty"`
	Devnet    bool   `json:"devnet"`
	Protocol  string `json:"protocol_version"`
}

// Status returns a snapshot of node state.
func (n *Node) Status() Status {
	n.mu.RLock()
	defer n.mu.RUnlock()
	root := n.state.Root()
	mempool := len(n.mempool)
	if n.mempoolSize != nil {
		mempool = n.mempoolSize()
	}
	return Status{
		Network: n.cfg.Network, Chain: n.cfg.Chain, Height: n.state.Height(),
		StateRoot: hex.EncodeToString(root[:]), Time: n.state.BlockTime(),
		Mempool: mempool, Halted: n.halted, Devnet: n.cfg.Devnet,
		Protocol: "arne/1",
	}
}

// Root returns the raw state root, for consensus engines that need the app hash.
func (n *Node) Root() []byte {
	n.mu.RLock()
	defer n.mu.RUnlock()
	root := n.state.Root()
	out := make([]byte, len(root))
	copy(out, root[:])
	return out
}

// SetBlockSink sets an optional, non-consensus consumer of committed blocks.
func (n *Node) SetBlockSink(sink BlockSink) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.cfg.BlockSink = sink
}

// SetBroadcast routes submitted transactions to an external consensus engine.
// When unset, the node keeps its own mempool and ProduceBlock builds blocks.
func (n *Node) SetBroadcast(fn func(raw []byte) error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.submit = fn
}

// SetMempoolSize reports the external mempool size in /v1/status.
func (n *Node) SetMempoolSize(fn func() int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.mempoolSize = fn
}

// Balance reads an account balance under the node lock.
func (n *Node) Balance(address, asset string) uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state.Balance(address, asset)
}

// SubmitTx validates a raw canonical transaction and adds it to the mempool.
// In consensus mode it is and forwarded to the external engine instead.
func (n *Node) SubmitTx(raw []byte) (string, error) {
	m, err := protocol.DecodeMap(raw)
	if err != nil {
		return "", fmt.Errorf("%w: decode: %v", protocol.ErrValidation, err)
	}
	tx, err := protocol.TxFromCBOR(m)
	if err != nil {
		return "", err
	}
	hash, err := tx.Hash()
	if err != nil {
		return "", err
	}
	n.mu.Lock()
	if n.halted != "" {
		n.mu.Unlock()
		return "", errors.New("node is halted: " + n.halted)
	}
	now := uint64(time.Now().Unix())
	if err := n.state.CheckTx(tx, now); err != nil {
		n.mu.Unlock()
		return "", err
	}
	if n.submit != nil {
		submit := n.submit
		n.mu.Unlock()
		if err := submit(raw); err != nil {
			return "", err
		}
		return hash, nil
	}
	if _, exists := n.mempool[hash]; exists {
		n.mu.Unlock()
		return hash, nil
	}
	// One live transaction per signer+nonce; the higher priority fee wins.
	for h, other := range n.mempool {
		if other.Signer == tx.Signer && other.Nonce == tx.Nonce {
			if other.PriorityFee >= tx.PriorityFee {
				n.mu.Unlock()
				return "", &ledger.Error{Code: ledger.CodeMempoolConflict, Message: "pending transaction at this nonce has equal or higher priority fee"}
			}
			delete(n.mempool, h)
		}
	}
	if len(n.mempool) >= 10_000 {
		n.mu.Unlock()
		return "", errors.New("mempool full")
	}
	n.mempool[hash] = tx
	n.mu.Unlock()
	return hash, nil
}

// DecodeTx decodes and checks the network/chain binding without touching state.
func (n *Node) DecodeTx(raw []byte) (protocol.Tx, error) {
	m, err := protocol.DecodeMap(raw)
	if err != nil {
		return protocol.Tx{}, err
	}
	tx, err := protocol.TxFromCBOR(m)
	if err != nil {
		return protocol.Tx{}, err
	}
	if tx.Network != n.cfg.Network || tx.Chain != n.cfg.Chain {
		return tx, fmt.Errorf("%w: expected %s/%s", protocol.ErrValidation, n.cfg.Network, n.cfg.Chain)
	}
	return tx, nil
}

// CheckRaw decodes and validates a transaction against current state. It is the
// app-side CheckTx used by an external consensus engine.
func (n *Node) CheckRaw(raw []byte) (protocol.Tx, error) {
	tx, err := n.DecodeTx(raw)
	if err != nil {
		return tx, err
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.halted != "" {
		return tx, errors.New("node is halted: " + n.halted)
	}
	return tx, n.state.CheckTx(tx, uint64(time.Now().Unix()))
}

// PrepareTxs decodes, filters, and canonically orders candidate transactions
// for a proposal. It never includes more than maxTxs or maxBytes.
func (n *Node) PrepareTxs(raws [][]byte, maxTxs, maxBytes int) [][]byte {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.halted != "" || len(raws) == 0 {
		return nil
	}
	txs := make([]protocol.Tx, 0, len(raws))
	byHash := make(map[string][]byte, len(raws))
	for _, raw := range raws {
		m, err := protocol.DecodeMap(raw)
		if err != nil {
			continue
		}
		tx, err := protocol.TxFromCBOR(m)
		if err != nil || tx.Network != n.cfg.Network || tx.Chain != n.cfg.Chain {
			continue
		}
		h, err := tx.Hash()
		if err != nil {
			continue
		}
		txs = append(txs, tx)
		byHash[h] = raw
	}
	ordered := ledger.OrderTxs(txs)
	expected := map[string]uint64{}
	next := func(signer string) uint64 {
		if v, ok := expected[signer]; ok {
			return v
		}
		info, _ := n.state.Account(signer)
		return info.Nonce
	}
	now := uint64(time.Now().Unix())
	out := make([][]byte, 0, len(ordered))
	size := 0
	for _, tx := range ordered {
		if len(out) >= maxTxs {
			break
		}
		if tx.Nonce != next(tx.Signer) {
			continue
		}
		if r := n.state.SimulateTx(tx, now, n.state.Height()+1); !r.Success {
			continue
		}
		h, err := tx.Hash()
		if err != nil {
			continue
		}
		raw := byHash[h]
		if maxBytes > 0 && size+len(raw) > maxBytes {
			continue
		}
		out = append(out, raw)
		size += len(raw)
		expected[tx.Signer] = tx.Nonce + 1
	}
	return out
}

// FinalizeTxs applies decided transactions in the given order and stages the
// block. CommitBlock persists it.
func (n *Node) FinalizeTxs(raws [][]byte, height, now uint64, proposer string) (ledger.Block, []ledger.Receipt, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.halted != "" {
		return ledger.Block{}, nil, errors.New("node is halted: " + n.halted)
	}
	txs := make([]protocol.Tx, 0, len(raws))
	for _, raw := range raws {
		m, err := protocol.DecodeMap(raw)
		if err != nil {
			return ledger.Block{}, nil, fmt.Errorf("decided tx: %w", err)
		}
		tx, err := protocol.TxFromCBOR(m)
		if err != nil {
			return ledger.Block{}, nil, fmt.Errorf("decided tx: %w", err)
		}
		txs = append(txs, tx)
	}
	block, receipts, err := n.state.ApplyOrderedBlock(txs, now, height, proposer)
	if err != nil {
		if errors.Is(err, ledger.ErrConservation) || errors.Is(err, ledger.ErrInvariant) {
			n.halted = err.Error()
		}
		return ledger.Block{}, nil, err
	}
	n.pending = &block
	return block, receipts, nil
}

// CommitBlock persists the block staged by FinalizeTxs and broadcasts it.
func (n *Node) CommitBlock() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.pending
	if b == nil {
		return nil
	}
	if err := n.store.SaveCommit(*b, n.state.Snapshot()); err != nil {
		n.halted = "storage failure: " + err.Error()
		return err
	}
	n.prune()
	for i, h := range b.TxHashes {
		if i < len(b.Receipts) {
			n.receipts[h] = b.Receipts[i]
		}
	}
	n.pending = nil
	n.submitBlock(*b)
	n.broadcast(Event{
		Type: "block", Height: b.Height, Time: b.Time,
		Hash: mustHash(*b), TxHashes: b.TxHashes,
	})
	n.dispatchReceipts(*b, b.Receipts)
	return nil
}

// submitBlock hands a committed block to the optional index sink. Submit must
// not block block production.
func (n *Node) submitBlock(b ledger.Block) {
	if n.cfg.BlockSink != nil {
		n.cfg.BlockSink.Submit(b)
	}
}

// ProduceBlock finalizes a block. It returns the block (possibly empty).
func (n *Node) ProduceBlock(now uint64) (ledger.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.halted != "" {
		return ledger.Block{}, errors.New("node is halted: " + n.halted)
	}
	selected := n.selectTxs()
	prevMempool := n.mempool
	// ApplyTx is atomic per transaction, so a failed tx leaves state intact.
	block, receipts, err := n.state.ApplyBlock(selected, now, n.state.Height()+1, n.cfg.Proposer)
	if err != nil {
		if errors.Is(err, ledger.ErrConservation) || errors.Is(err, ledger.ErrInvariant) {
			n.halted = err.Error()
		}
		return ledger.Block{}, err
	}
	n.mempool = map[string]protocol.Tx{}
	for h, tx := range prevMempool {
		kept := true
		for _, r := range receipts {
			if r.Hash == h && r.Success {
				kept = false
				break
			}
		}
		if kept {
			n.mempool[h] = tx
		}
	}
	if err := n.store.SaveCommit(block, n.state.Snapshot()); err != nil {
		n.halted = "storage failure: " + err.Error()
		return ledger.Block{}, err
	}
	n.prune()
	n.blocks[block.Height] = block
	for i, h := range block.TxHashes {
		if i < len(receipts) {
			n.receipts[h] = receipts[i]
		}
	}
	n.submitBlock(block)
	n.broadcast(Event{
		Type: "block", Height: block.Height, Time: block.Time,
		Hash: mustHash(block), TxHashes: block.TxHashes,
	})
	for index, receipt := range receipts {
		r := receipt
		n.broadcast(Event{
			Type: "transaction", Height: block.Height, Time: block.Time,
			Hash: receipt.Hash, Receipt: &r, Index: index,
		})
	}
	n.dispatchReceipts(block, receipts)
	return block, nil
}

// dispatchReceipts queues one signed webhook per transaction. Enqueue never
// blocks; a full queue drops events and receivers reconcile from the chain.
func (n *Node) dispatchReceipts(b ledger.Block, receipts []ledger.Receipt) {
	if n.webhooks == nil {
		return
	}
	hash := mustHash(b)
	for _, receipt := range receipts {
		n.webhooks.Enqueue("transaction", map[string]any{
			"block_height": b.Height, "block_hash": hash, "time": b.Time, "receipt": receipt,
		})
	}
}

// selectTxs picks executable transactions in canonical order.
func (n *Node) selectTxs() []protocol.Tx {
	all := make([]protocol.Tx, 0, len(n.mempool))
	for _, tx := range n.mempool {
		all = append(all, tx)
	}
	ordered := ledger.OrderTxs(all)
	expected := map[string]uint64{}
	next := func(signer string) uint64 {
		if v, ok := expected[signer]; ok {
			return v
		}
		info, _ := n.state.Account(signer)
		return info.Nonce
	}
	out := make([]protocol.Tx, 0, len(ordered))
	for _, tx := range ordered {
		if len(out) >= n.cfg.MaxBlockTxs {
			break
		}
		if tx.Nonce != next(tx.Signer) {
			continue
		}
		// Simulate against current state so obviously invalid txs do not block.
		r := n.state.SimulateTx(tx, uint64(time.Now().Unix()), n.state.Height()+1)
		if !r.Success {
			continue
		}
		out = append(out, tx)
		expected[tx.Signer] = tx.Nonce + 1
	}
	return out
}

func mustHash(b ledger.Block) string {
	h, err := b.Hash()
	if err != nil {
		return ""
	}
	return h
}

// prune drops blocks outside the configured retention window. The caller holds
// the node lock.
func (n *Node) prune() {
	if n.cfg.KeepBlocks == 0 {
		return
	}
	height := n.state.Height()
	if height <= n.cfg.KeepBlocks {
		return
	}
	before := height - n.cfg.KeepBlocks + 1
	if err := n.store.Prune(before); err != nil {
		n.halted = "prune failure: " + err.Error()
		return
	}
	for h, b := range n.blocks {
		if h >= before {
			continue
		}
		for _, hash := range b.TxHashes {
			delete(n.receipts, hash)
		}
		delete(n.blocks, h)
	}
}

// Block returns a block by height.
func (n *Node) Block(height uint64) (ledger.Block, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	b, ok := n.blocks[height]
	return b, ok
}

// LastBlock returns the newest committed block.
func (n *Node) LastBlock() (ledger.Block, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	b, ok := n.blocks[n.state.Height()]
	return b, ok
}

// OldestBlock returns the lowest retained block height, or 0 when none is kept.
func (n *Node) OldestBlock() uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var oldest uint64
	for h := range n.blocks {
		if oldest == 0 || h < oldest {
			oldest = h
		}
	}
	return oldest
}

// Blocks returns blocks at or below from, newest first, up to limit.
func (n *Node) Blocks(limit int, before uint64) []ledger.Block {
	n.mu.RLock()
	defer n.mu.RUnlock()
	heights := make([]uint64, 0, len(n.blocks))
	for h := range n.blocks {
		if before == 0 || h < before {
			heights = append(heights, h)
		}
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] > heights[j] })
	if limit <= 0 || limit > len(heights) {
		limit = len(heights)
	}
	out := make([]ledger.Block, 0, limit)
	for _, h := range heights[:limit] {
		out = append(out, n.blocks[h])
	}
	return out
}

// ListBlocks returns one page of blocks and the next cursor. Cursor 0 starts
// at the newest block when descending or the oldest when ascending; a non-zero
// cursor is the last height already returned. proposer limits the page to one
// proposer and is applied before the page is taken.
func (n *Node) ListBlocks(limit int, cursor uint64, ascending bool, proposer string) ([]ledger.Block, uint64) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	heights := make([]uint64, 0, len(n.blocks))
	for h, b := range n.blocks {
		if cursor != 0 {
			if ascending && h <= cursor || !ascending && h >= cursor {
				continue
			}
		}
		if proposer != "" && b.Proposer != proposer {
			continue
		}
		heights = append(heights, h)
	}
	sort.Slice(heights, func(i, j int) bool {
		if ascending {
			return heights[i] < heights[j]
		}
		return heights[i] > heights[j]
	})
	next := uint64(0)
	if limit > 0 && len(heights) > limit {
		heights = heights[:limit]
		next = heights[len(heights)-1]
	}
	out := make([]ledger.Block, 0, len(heights))
	for _, h := range heights {
		out = append(out, n.blocks[h])
	}
	return out, next
}

// Receipt returns a transaction receipt.
func (n *Node) Receipt(hash string) (ledger.Receipt, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	r, ok := n.receipts[hash]
	return r, ok
}

// Intent returns a payment intent.
func (n *Node) Intent(id string) (protocol.Intent, bool, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state.Intent(id)
}

// Subscription returns a recurring-payment authorization.
func (n *Node) Subscription(id string) (protocol.Subscription, bool, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state.Subscription(id)
}

// Transfer returns the index entry for a finalized transfer.
func (n *Node) Transfer(hash string) (protocol.Transfer, bool, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state.Transfer(hash)
}

// Refunded returns the total refunded against a transfer.
func (n *Node) Refunded(hash string) uint64 {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state.Refunded(hash)
}

// Simulate dry-runs a transaction without touching state.
func (n *Node) Simulate(raw []byte) (ledger.Receipt, error) {
	m, err := protocol.DecodeMap(raw)
	if err != nil {
		return ledger.Receipt{}, err
	}
	tx, err := protocol.TxFromCBOR(m)
	if err != nil {
		return ledger.Receipt{}, err
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state.SimulateTx(tx, uint64(time.Now().Unix()), n.state.Height()+1), nil
}

// Subscribe returns an event channel and a cancel function.
func (n *Node) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	n.mu.Lock()
	n.subs[ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		if _, ok := n.subs[ch]; ok {
			delete(n.subs, ch)
			close(ch)
		}
		n.mu.Unlock()
	}
}

func (n *Node) broadcast(e Event) {
	for ch := range n.subs {
		select {
		case ch <- e:
		default: // slow consumer: drop rather than block consensus
		}
	}
}

// Faucet sends devnet ARCE to an address from the node's faucet key.
func (n *Node) Faucet(addr string) (string, error) {
	if n.cfg.Faucet == nil || !n.cfg.Devnet {
		return "", errors.New("faucet is only available on devnet")
	}
	if err := protocol.ValidateAddress(addr, n.cfg.Network); err != nil {
		return "", err
	}
	n.mu.RLock()
	info, _ := n.state.Account(n.cfg.Faucet.Address)
	n.mu.RUnlock()
	amount := n.cfg.FaucetAmount
	if amount == 0 {
		amount = 10_000_000_000 // 100 ARCE
	}
	native := protocol.NativeAsset(n.cfg.Network)
	balance := info.Balances[native]
	if balance <= amount {
		return "", errors.New("faucet empty")
	}
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: n.cfg.Network, Chain: n.cfg.Chain,
		Type: protocol.TxTransfer, Signer: n.cfg.Faucet.Address, Nonce: info.Nonce,
		CreatedAt: uint64(time.Now().Unix()), ExpiresAt: uint64(time.Now().Unix()) + 3600,
		FeeAsset: native, MaxFee: n.cfg.Params.BaseFee,
		Payload: map[string]any{"asset": native, "to": addr, "amount": amount},
	}
	if err := tx.Sign(n.cfg.Faucet); err != nil {
		return "", err
	}
	enc, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		return "", err
	}
	return n.SubmitTx(enc)
}

// Proof returns a state proof for a namespaced key.
func (n *Node) Proof(key string) (smt.Proof, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state.Tree().Prove(key), nil
}
