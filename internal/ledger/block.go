package ledger

import (
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// Allocation credits an account at genesis.
type Allocation struct {
	Address string `json:"address"`
	Asset   string `json:"asset"`
	Amount  uint64 `json:"amount"`
}

// Genesis is the deterministic network bootstrap.
type Genesis struct {
	Network     string                       `json:"network"`
	Chain       string                       `json:"chain"`
	Time        uint64                       `json:"time"`
	Params      Params                       `json:"params"`
	Assets      []protocol.Asset             `json:"assets,omitempty"`
	Allocations []Allocation                 `json:"allocations,omitempty"`
	Pools       map[string]map[string]uint64 `json:"pools,omitempty"`
}

// NewGenesis builds the initial state. Supply is the sum of allocations and
// pools per asset; nothing is implicitly minted.
func NewGenesis(g Genesis) (*State, error) {
	s, err := NewState(g.Network, g.Chain, g.Params)
	if err != nil {
		return nil, err
	}
	native := protocol.NativeAsset(g.Network)
	arce := protocol.Asset{
		ID: native, Class: "native", Symbol: protocol.NativeAssetSymbol,
		Name: "AnoRebelCoin", Decimals: 8, Policy: "capped",
		MaxSupply: g.Params.NativeMaxSupply, Issuer: "",
	}
	if err := s.putAsset(arce); err != nil {
		return nil, err
	}
	for _, a := range g.Assets {
		if err := a.Validate(g.Network); err != nil {
			return nil, err
		}
		if a.Class == "native" {
			continue
		}
		if _, exists, _ := s.Asset(a.ID); exists {
			return nil, fmt.Errorf("genesis: duplicate asset %s", a.ID)
		}
		if err := s.putAsset(a); err != nil {
			return nil, err
		}
	}
	supply := map[string]uint64{}
	credit := func(addr, asset string, amount uint64) error {
		if amount == 0 {
			return nil
		}
		if err := s.requireAsset(asset); err != nil {
			return fmt.Errorf("genesis: %w", err)
		}
		if err := protocol.ValidateAddress(addr, g.Network); err != nil {
			return fmt.Errorf("genesis: %w", err)
		}
		s.ensureAccount(addr)
		s.addBalance(addr, asset, amount)
		supply[asset] += amount
		return nil
	}
	for _, a := range g.Allocations {
		if err := credit(a.Address, a.Asset, a.Amount); err != nil {
			return nil, err
		}
	}
	pools := make([]string, 0, len(g.Pools))
	for name := range g.Pools {
		pools = append(pools, name)
	}
	sort.Strings(pools)
	for _, name := range pools {
		assets := make([]string, 0, len(g.Pools[name]))
		for asset := range g.Pools[name] {
			assets = append(assets, asset)
		}
		sort.Strings(assets)
		for _, asset := range assets {
			amount := g.Pools[name][asset]
			if amount == 0 {
				continue
			}
			if err := s.requireAsset(asset); err != nil {
				return nil, fmt.Errorf("genesis: %w", err)
			}
			s.addPool(name, asset, amount)
			supply[asset] += amount
		}
	}
	for asset, total := range supply {
		s.setUint(fmt.Sprintf(kSupply, asset), total)
	}
	if arce.MaxSupply > 0 && supply[native] > arce.MaxSupply {
		return nil, fmt.Errorf("%w: genesis ARCE above cap", protocol.ErrValidation)
	}
	s.SetBlockTime(g.Time)
	if err := s.CheckAll(); err != nil {
		return nil, err
	}
	return s, nil
}

// Block is a finalized block.
type Block struct {
	Height    uint64    `json:"height"`
	Time      uint64    `json:"time"`
	Proposer  string    `json:"proposer"`
	PrevRoot  string    `json:"prev_root"`
	StateRoot string    `json:"state_root"`
	TxRoot    string    `json:"tx_root"`
	TxHashes  []string  `json:"tx_hashes"`
	Txs       [][]byte  `json:"txs"`
	Receipts  []Receipt `json:"receipts"`
}

// Hash returns the canonical block hash. Receipts are not part of the header.
func (b Block) Hash() (string, error) {
	hashes := make([]any, len(b.TxHashes))
	for i, h := range b.TxHashes {
		hashes[i] = h
	}
	enc, err := protocol.Encode(map[string]any{
		"height": b.Height, "time": b.Time, "proposer": b.Proposer,
		"prev_root": b.PrevRoot, "state_root": b.StateRoot,
		"tx_root": b.TxRoot, "tx_hashes": hashes,
	})
	if err != nil {
		return "", err
	}
	return protocol.HashHex(enc), nil
}

// TxRoot commits to the ordered transaction hash list.
func TxRoot(hashes []string) string {
	raw := make([]byte, 0, len(hashes)*32)
	for _, h := range hashes {
		b, err := hex.DecodeString(h)
		if err != nil {
			return ""
		}
		raw = append(raw, b...)
	}
	return protocol.HashHex(raw)
}

// OrderTxs returns the deterministic block order. Nonces are ascending inside a
// signer group so a signer's sequence never breaks; groups are ordered by their
// highest priority fee, then by address.
func OrderTxs(txs []protocol.Tx) []protocol.Tx {
	groups := map[string][]protocol.Tx{}
	signers := make([]string, 0, len(txs))
	for _, tx := range txs {
		if _, ok := groups[tx.Signer]; !ok {
			signers = append(signers, tx.Signer)
		}
		groups[tx.Signer] = append(groups[tx.Signer], tx)
	}
	for _, signer := range signers {
		group := groups[signer]
		sort.Slice(group, func(i, j int) bool {
			if group[i].Nonce != group[j].Nonce {
				return group[i].Nonce < group[j].Nonce
			}
			hi, _ := group[i].Hash()
			hj, _ := group[j].Hash()
			return hi < hj
		})
	}
	maxFee := func(signer string) uint64 {
		var m uint64
		for _, tx := range groups[signer] {
			if tx.PriorityFee > m {
				m = tx.PriorityFee
			}
		}
		return m
	}
	sort.SliceStable(signers, func(i, j int) bool {
		fi, fj := maxFee(signers[i]), maxFee(signers[j])
		if fi != fj {
			return fi > fj
		}
		return signers[i] < signers[j]
	})
	out := make([]protocol.Tx, 0, len(txs))
	for _, signer := range signers {
		out = append(out, groups[signer]...)
	}
	return out
}

// ApplyBlock executes transactions at height and commits height/time.
// Invalid transactions appear in the receipts with Success=false and leave no
// state change.
func (s *State) ApplyBlock(txs []protocol.Tx, now, height uint64, proposer string) (Block, []Receipt, error) {
	return s.ApplyOrderedBlock(OrderTxs(txs), now, height, proposer)
}

// ApplyOrderedBlock is ApplyBlock for a caller that already ordered the
// transactions (consensus decides the order). The given order is preserved.
func (s *State) ApplyOrderedBlock(txs []protocol.Tx, now, height uint64, proposer string) (Block, []Receipt, error) {
	if height != s.Height()+1 {
		return Block{}, nil, fmt.Errorf("%w: height %d after %d", ErrInvariant, height, s.Height())
	}
	prev := s.Root()
	b := Block{Height: height, Time: now, Proposer: proposer, PrevRoot: hex.EncodeToString(prev[:])}
	s.SetHeight(height)
	s.SetBlockTime(now)
	for _, tx := range txs {
		r := s.ApplyTx(tx, now, height)
		enc, err := protocol.Encode(tx.ToCBOR())
		if err != nil {
			return Block{}, nil, err
		}
		b.TxHashes = append(b.TxHashes, r.Hash)
		b.Txs = append(b.Txs, enc)
		b.Receipts = append(b.Receipts, r)
	}
	if err := s.CheckAll(); err != nil {
		return Block{}, nil, err
	}
	root := s.Root()
	b.StateRoot = hex.EncodeToString(root[:])
	b.TxRoot = TxRoot(b.TxHashes)
	return b, b.Receipts, nil
}
