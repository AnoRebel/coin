// Package ledger implements the ARNE multi-asset account ledger: assets,
// balances, fees, escrow, allowances, batches, and supply conservation.
package ledger

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/smt"
)

// State is the consensus state. All reads are deterministic; all writes go
// through a journal so a failed transaction rolls back completely.
type State struct {
	tree    *smt.Tree
	journal []journalEntry
	depth   int

	Network string
	Chain   string
	Params  Params
}

type journalEntry struct {
	key   string
	prev  []byte
	had   bool
	depth int
}

// NewState returns an empty state for a network.
func NewState(network, chain string, params Params) (*State, error) {
	if _, err := protocol.NetworkClass(network); err != nil {
		return nil, err
	}
	if want, err := protocol.ChainID(network); err != nil || want != chain {
		return nil, fmt.Errorf("%w: chain %q", protocol.ErrNetwork, chain)
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	return &State{tree: smt.New(), Network: network, Chain: chain, Params: params}, nil
}

// StateFromSnapshot restores a state from a key/value snapshot.
func StateFromSnapshot(network, chain string, params Params, values map[string][]byte) (*State, error) {
	s, err := NewState(network, chain, params)
	if err != nil {
		return nil, err
	}
	s.tree.Load(values)
	return s, nil
}

// Tree exposes the underlying tree for proofs and snapshots.
func (s *State) Tree() *smt.Tree { return s.tree }

// Root returns the current state root.
func (s *State) Root() smt.Hash { return s.tree.Root() }

// Snapshot returns live state as a map.
func (s *State) Snapshot() map[string][]byte { return s.tree.Snapshot() }

// Begin starts an atomic section.
func (s *State) Begin() { s.depth++ }

// Commit closes the innermost atomic section, keeping its writes.
func (s *State) Commit() {
	if s.depth == 0 {
		return
	}
	for len(s.journal) > 0 && s.journal[len(s.journal)-1].depth >= s.depth {
		s.journal = s.journal[:len(s.journal)-1]
	}
	s.depth--
}

// Rollback discards every write in the innermost atomic section.
func (s *State) Rollback() {
	if s.depth == 0 {
		return
	}
	for len(s.journal) > 0 && s.journal[len(s.journal)-1].depth >= s.depth {
		e := s.journal[len(s.journal)-1]
		s.journal = s.journal[:len(s.journal)-1]
		if e.had {
			s.tree.Set(e.key, e.prev)
		} else {
			s.tree.Delete(e.key)
		}
	}
	s.depth--
}

// journalDepth reports whether writes are currently journaled.
func (s *State) set(key string, value []byte) {
	if s.depth > 0 {
		prev, had := s.tree.Get(key)
		s.journal = append(s.journal, journalEntry{key: key, prev: prev, had: had, depth: s.depth})
	}
	s.tree.Set(key, value)
}

func (s *State) del(key string) {
	if s.depth > 0 {
		if prev, had := s.tree.Get(key); had {
			s.journal = append(s.journal, journalEntry{key: key, prev: prev, had: true, depth: s.depth})
		}
	}
	s.tree.Delete(key)
}

func (s *State) get(key string) ([]byte, bool) { return s.tree.Get(key) }

func (s *State) setUint(key string, v uint64) { s.set(key, encodeUint(v)) }

func (s *State) getUint(key string) uint64 {
	b, _ := s.get(key)
	return decodeUint(b)
}

// Height returns the committed block height.
func (s *State) Height() uint64 { return s.getUint(fmt.Sprintf(kMeta, "height")) }

// SetHeight records the committed block height.
func (s *State) SetHeight(h uint64) { s.setUint(fmt.Sprintf(kMeta, "height"), h) }

// BlockTime returns the last committed block time.
func (s *State) BlockTime() uint64 { return s.getUint(fmt.Sprintf(kMeta, "time")) }

// SetBlockTime records the last committed block time.
func (s *State) SetBlockTime(t uint64) { s.setUint(fmt.Sprintf(kMeta, "time"), t) }

// Balance returns an account balance in base units.
func (s *State) Balance(addr, asset string) uint64 {
	return s.getUint(fmt.Sprintf(kBalance, addr, asset))
}

func (s *State) setBalance(addr, asset string, amount uint64) {
	s.setUint(fmt.Sprintf(kBalance, addr, asset), amount)
}

func (s *State) addBalance(addr, asset string, delta uint64) {
	s.setBalance(addr, asset, s.Balance(addr, asset)+delta)
}

func (s *State) subBalance(addr, asset string, delta uint64) error {
	cur := s.Balance(addr, asset)
	if cur < delta {
		return txErr(CodeInsufficientFunds, "insufficient %s balance", asset)
	}
	s.setBalance(addr, asset, cur-delta)
	return nil
}

// PoolBalance returns a protocol pool balance.
func (s *State) PoolBalance(pool, asset string) uint64 {
	return s.getUint(fmt.Sprintf(kPool, pool, asset))
}

func (s *State) addPool(pool, asset string, delta uint64) {
	k := fmt.Sprintf(kPool, pool, asset)
	s.setUint(k, s.getUint(k)+delta)
}

func (s *State) subPool(pool, asset string, delta uint64) error {
	k := fmt.Sprintf(kPool, pool, asset)
	cur := s.getUint(k)
	if cur < delta {
		return txErr(CodeInsufficientFunds, "pool %s/%s underfunded", pool, asset)
	}
	s.setUint(k, cur-delta)
	return nil
}

// Supply returns total ever-minted units of an asset.
func (s *State) Supply(asset string) uint64 { return s.getUint(fmt.Sprintf(kSupply, asset)) }

// Burned returns total burned units of an asset.
func (s *State) Burned(asset string) uint64 { return s.getUint(fmt.Sprintf(kBurned, asset)) }

func (s *State) addBurned(asset string, n uint64) {
	k := fmt.Sprintf(kBurned, asset)
	s.setUint(k, s.getUint(k)+n)
}

// Asset returns an asset record.
func (s *State) Asset(id string) (protocol.Asset, bool, error) {
	b, ok := s.get(fmt.Sprintf(kAsset, id))
	if !ok {
		return protocol.Asset{}, false, nil
	}
	m, err := protocol.DecodeMap(b)
	if err != nil {
		return protocol.Asset{}, false, fmt.Errorf("asset %s: %w", id, err)
	}
	a, err := protocol.AssetFromCBOR(m)
	if err != nil {
		return protocol.Asset{}, false, err
	}
	return a, true, nil
}

func (s *State) putAsset(a protocol.Asset) error {
	b, err := protocol.Encode(a.ToCBOR())
	if err != nil {
		return err
	}
	s.set(fmt.Sprintf(kAsset, a.ID), b)
	return nil
}

// Assets returns every registered asset ID, sorted.
func (s *State) Assets() []string {
	var out []string
	prefix := "asset/"
	for _, k := range s.tree.Keys() {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out
}

// Escrow returns an escrow record.
func (s *State) Escrow(id string) (protocol.Escrow, bool, error) {
	b, ok := s.get(fmt.Sprintf(kEscrow, id))
	if !ok {
		return protocol.Escrow{}, false, nil
	}
	m, err := protocol.DecodeMap(b)
	if err != nil {
		return protocol.Escrow{}, false, err
	}
	e, err := protocol.EscrowFromCBOR(m)
	if err != nil {
		return protocol.Escrow{}, false, err
	}
	return e, true, nil
}

func (s *State) putEscrow(e protocol.Escrow) error {
	b, err := protocol.Encode(e.ToCBOR())
	if err != nil {
		return err
	}
	s.set(fmt.Sprintf(kEscrow, e.ID), b)
	return nil
}

// Allowance returns the remaining allowance for spender on owner's asset.
func (s *State) Allowance(owner, spender, asset string) uint64 {
	return s.getUint(fmt.Sprintf(kAllowance, owner, spender, asset))
}

// AllowanceExpiry returns the allowance expiry (0 = none).
func (s *State) AllowanceExpiry(owner, spender, asset string) uint64 {
	return s.getUint(fmt.Sprintf(kAllowExp, owner, spender, asset))
}

// Route returns the ARCE conversion rate for a fee asset: ARCE base units per
// 1e8 base units of the asset (0 = no route).
func (s *State) Route(asset string) uint64 { return s.getUint(fmt.Sprintf(kRoute, asset)) }

// SetRoute installs a fee-conversion route. Governance-gated in production;
// exposed as an admin operation for devnet bootstrap.
func (s *State) SetRoute(asset string, rate uint64) {
	if rate == 0 {
		s.del(fmt.Sprintf(kRoute, asset))
		return
	}
	s.setUint(fmt.Sprintf(kRoute, asset), rate)
}

// AccountInfo is a read model for an account.
type AccountInfo struct {
	Address  string            `json:"address"`
	Nonce    uint64            `json:"nonce"`
	Auth     string            `json:"auth"`
	Balances map[string]uint64 `json:"balances"`
}

// Account returns an account read model. Created is false when the account has
// no state yet.
func (s *State) Account(addr string) (AccountInfo, bool) {
	info := AccountInfo{Address: addr, Balances: map[string]uint64{}}
	auth, ok := s.get(fmt.Sprintf(kAuth, addr))
	if ok {
		info.Auth = accountAuth(auth)
	}
	info.Nonce = s.getUint(fmt.Sprintf(kNonce, addr))
	prefix := "balance/" + addr + "/"
	for _, k := range s.tree.Keys() {
		if strings.HasPrefix(k, prefix) {
			info.Balances[strings.TrimPrefix(k, prefix)] = s.getUint(k)
		}
	}
	return info, ok || info.Nonce > 0 || len(info.Balances) > 0
}

func (s *State) ensureAccount(addr string) {
	if _, ok := s.get(fmt.Sprintf(kAuth, addr)); !ok {
		s.set(fmt.Sprintf(kAuth, addr), []byte(protocol.SuiteEd25519))
	}
}

// CheckConservation verifies sum(balances)+pools+escrow+burned == supply for
// one asset. It is consensus-critical and never optional.
func (s *State) CheckConservation(asset string) error {
	var balances uint64
	for _, k := range s.tree.Keys() {
		if strings.HasPrefix(k, "balance/") && strings.HasSuffix(k, "/"+asset) {
			balances += s.getUint(k)
		}
		if strings.HasPrefix(k, "pool/") && strings.HasSuffix(k, "/"+asset) {
			balances += s.getUint(k)
		}
	}
	held := s.getUint(fmt.Sprintf(kEscrowAmt, asset))
	burned := s.Burned(asset)
	supply := s.Supply(asset)
	total := balances + held + burned
	if total != supply {
		return fmt.Errorf("%w: asset %s: balances(%d)+escrow(%d)+burned(%d)=%d != supply(%d)",
			ErrConservation, asset, balances, held, burned, total, supply)
	}
	return nil
}

// CheckAll verifies conservation for every registered asset.
func (s *State) CheckAll() error {
	for _, id := range s.Assets() {
		if err := s.CheckConservation(id); err != nil {
			return err
		}
	}
	return nil
}
