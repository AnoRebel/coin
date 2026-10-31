// Package storage persists blocks and state snapshots. Pebble is the production
// engine; Memory is used by tests and single-process devnets.
package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/anorebel/anorebelcoin/internal/ledger"
	"github.com/cockroachdb/pebble"
)

// Store is the persistence contract.
type Store interface {
	SaveBlock(b ledger.Block) error
	Block(height uint64) (ledger.Block, bool, error)
	LastBlock() (ledger.Block, bool, error)
	SaveState(height uint64, snapshot map[string][]byte) error
	// SaveCommit writes a block and its state snapshot in one atomic batch: a
	// crash leaves both at the previous height, never a block without state.
	SaveCommit(b ledger.Block, snapshot map[string][]byte) error
	// Prune deletes blocks below before. State and the latest block remain.
	Prune(before uint64) error
	// Migrate moves the on-disk schema to target; it is a no-op at target.
	Migrate(target uint32) error
	// SchemaVersion reports the on-disk schema.
	SchemaVersion() (uint32, error)
	LoadState() (map[string][]byte, uint64, bool, error)
	Close() error
}

// ErrCorrupt is returned for checksum failures.
var ErrCorrupt = errors.New("storage: corrupt record")

func encodeRecord(v any) ([]byte, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	out := make([]byte, 0, 32+len(payload))
	out = append(out, sum[:]...)
	return append(out, payload...), nil
}

func decodeRecord(data []byte, v any) error {
	if len(data) < 32 {
		return ErrCorrupt
	}
	sum := sha256.Sum256(data[32:])
	if string(sum[:]) != string(data[:32]) {
		return ErrCorrupt
	}
	return json.Unmarshal(data[32:], v)
}

func blockKey(h uint64) []byte {
	k := make([]byte, 9)
	k[0] = 'b'
	binary.BigEndian.PutUint64(k[1:], h)
	return k
}

var (
	keyLast  = []byte("meta/last")
	keyState = []byte("state/current")
)

// Pebble is the production store. Every block write is a single atomic batch,
// so a crash leaves the last fully committed height.
type Pebble struct {
	db *pebble.DB
	mu sync.Mutex
}

// OpenPebble opens or creates a store at dir.
func OpenPebble(dir string) (*Pebble, error) {
	if err := os.MkdirAll(filepath.Clean(dir), 0o750); err != nil {
		return nil, err
	}
	db, err := pebble.Open(filepath.Clean(dir), &pebble.Options{})
	if err != nil {
		return nil, err
	}
	return &Pebble{db: db}, nil
}

// SaveBlock writes a block atomically and marks it as last.
func (p *Pebble) SaveBlock(b ledger.Block) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, err := encodeRecord(b)
	if err != nil {
		return err
	}
	h, err := json.Marshal(b.Height)
	if err != nil {
		return err
	}
	batch := p.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(blockKey(b.Height), rec, nil); err != nil {
		return err
	}
	if err := batch.Set(keyLast, h, nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// Block loads a block by height.
func (p *Pebble) Block(height uint64) (ledger.Block, bool, error) {
	rec, closer, err := p.db.Get(blockKey(height))
	if errors.Is(err, pebble.ErrNotFound) {
		return ledger.Block{}, false, nil
	}
	if err != nil {
		return ledger.Block{}, false, err
	}
	defer closer.Close()
	var b ledger.Block
	if err := decodeRecord(rec, &b); err != nil {
		return ledger.Block{}, false, err
	}
	return b, true, nil
}

// LastBlock returns the highest committed block.
func (p *Pebble) LastBlock() (ledger.Block, bool, error) {
	rec, closer, err := p.db.Get(keyLast)
	if errors.Is(err, pebble.ErrNotFound) {
		return ledger.Block{}, false, nil
	}
	if err != nil {
		return ledger.Block{}, false, err
	}
	var h uint64
	if err := json.Unmarshal(rec, &h); err != nil {
		closer.Close()
		return ledger.Block{}, false, err
	}
	closer.Close()
	return p.Block(h)
}

func encodeState(height uint64, snapshot map[string][]byte) ([]byte, error) {
	keys := make([]string, 0, len(snapshot))
	for k := range snapshot {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string][]byte, len(snapshot))
	for _, k := range keys {
		ordered[k] = snapshot[k]
	}
	return encodeRecord(struct {
		Height uint64            `json:"height"`
		State  map[string][]byte `json:"state"`
	}{height, ordered})
}

// SaveState writes a state snapshot atomically.
func (p *Pebble) SaveState(height uint64, snapshot map[string][]byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, err := encodeState(height, snapshot)
	if err != nil {
		return err
	}
	return p.db.Set(keyState, rec, pebble.Sync)
}

// SaveCommit writes the block, last-height marker, and state snapshot in one
// batch, so a crash cannot expose a block without its state.
func (p *Pebble) SaveCommit(b ledger.Block, snapshot map[string][]byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, err := encodeRecord(b)
	if err != nil {
		return err
	}
	h, err := json.Marshal(b.Height)
	if err != nil {
		return err
	}
	state, err := encodeState(b.Height, snapshot)
	if err != nil {
		return err
	}
	batch := p.db.NewBatch()
	defer batch.Close()
	for _, kv := range []struct {
		key []byte
		val []byte
	}{
		{blockKey(b.Height), rec},
		{keyLast, h},
		{keyState, state},
	} {
		if err := batch.Set(kv.key, kv.val, nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

// LoadState reads the latest snapshot.
func (p *Pebble) LoadState() (map[string][]byte, uint64, bool, error) {
	rec, closer, err := p.db.Get(keyState)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	defer closer.Close()
	var snap struct {
		Height uint64            `json:"height"`
		State  map[string][]byte `json:"state"`
	}
	if err := decodeRecord(rec, &snap); err != nil {
		return nil, 0, false, err
	}
	return snap.State, snap.Height, true, nil
}

// Close closes the database.
func (p *Pebble) Close() error { return p.db.Close() }

// Prune deletes blocks below before in one synced batch.
func (p *Pebble) Prune(before uint64) error {
	if before <= 1 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	batch := p.db.NewBatch()
	defer batch.Close()
	if err := batch.DeleteRange(blockKey(0), blockKey(before), nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// Memory is an in-memory store for tests and ephemeral devnets.
type Memory struct {
	mu     sync.RWMutex
	blocks map[uint64]ledger.Block
	order  []uint64
	state  map[string][]byte
	height uint64
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{blocks: map[uint64]ledger.Block{}}
}

// SaveBlock stores a block.
func (m *Memory) SaveBlock(b ledger.Block) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.blocks[b.Height]; !exists {
		m.order = append(m.order, b.Height)
		sort.Slice(m.order, func(i, j int) bool { return m.order[i] < m.order[j] })
	}
	m.blocks[b.Height] = b
	m.height = b.Height
	return nil
}

// Block loads a block.
func (m *Memory) Block(height uint64) (ledger.Block, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.blocks[height]
	return b, ok, nil
}

// LastBlock returns the highest block.
func (m *Memory) LastBlock() (ledger.Block, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.order) == 0 {
		return ledger.Block{}, false, nil
	}
	return m.blocks[m.order[len(m.order)-1]], true, nil
}

// SaveState stores a snapshot.
func (m *Memory) SaveState(height uint64, snapshot map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setSnapshot(height, snapshot)
	return nil
}

func (m *Memory) setSnapshot(height uint64, snapshot map[string][]byte) {
	cp := make(map[string][]byte, len(snapshot))
	for k, v := range snapshot {
		cp[k] = append([]byte(nil), v...)
	}
	m.state = cp
	m.height = height
}

// Prune drops blocks below before.
func (m *Memory) Prune(before uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.order[:0]
	for _, h := range m.order {
		if h < before {
			delete(m.blocks, h)
			continue
		}
		kept = append(kept, h)
	}
	m.order = kept
	return nil
}

// SaveCommit stores a block and its snapshot together.
func (m *Memory) SaveCommit(b ledger.Block, snapshot map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.blocks[b.Height]; !exists {
		m.order = append(m.order, b.Height)
		sort.Slice(m.order, func(i, j int) bool { return m.order[i] < m.order[j] })
	}
	m.blocks[b.Height] = b
	m.setSnapshot(b.Height, snapshot)
	return nil
}

// LoadState loads the snapshot.
func (m *Memory) LoadState() (map[string][]byte, uint64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state == nil {
		return nil, 0, false, nil
	}
	cp := make(map[string][]byte, len(m.state))
	for k, v := range m.state {
		cp[k] = append([]byte(nil), v...)
	}
	return cp, m.height, true, nil
}

// Close is a no-op.
func (m *Memory) Close() error { return nil }

// SchemaVersion is always current for an in-memory store.
func (m *Memory) SchemaVersion() (uint32, error) { return SchemaVersion, nil }

// Migrate is a no-op for an in-memory store.
func (m *Memory) Migrate(target uint32) error {
	if target > SchemaVersion {
		return ErrSchemaTarget
	}
	return nil
}

var _ = fmt.Sprintf
