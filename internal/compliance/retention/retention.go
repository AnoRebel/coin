// Package retention applies data-retention policy to off-chain personal data.
//
// Policies are per data class. Expired records are deleted or anonymized, but a
// deletion leaves a tombstone with the ledger reference and a content hash, so
// on-chain hashes and proofs stay verifiable. A legal hold suspends deletion and
// anonymization for its record, and every action is logged.
package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Action is what happens to a record at the end of its retention period.
type Action string

const (
	Delete    Action = "delete"
	Anonymize Action = "anonymize"
)

var (
	ErrInvalid = errors.New("retention: invalid policy or record")
	ErrExists  = errors.New("retention: record or hold already exists")
	ErrUnknown = errors.New("retention: unknown record or hold")
	ErrOnChain = errors.New("retention: on-chain records are immutable")
)

// Policy sets a retention period for one data class.
type Policy struct {
	Class           string   `json:"class"`
	Days            int      `json:"days"`
	Action          Action   `json:"action"`
	AnonymizeFields []string `json:"anonymize_fields,omitempty"`
}

// Record is one off-chain data record.
type Record struct {
	ID        string            `json:"id"`
	Class     string            `json:"class"`
	CreatedAt int64             `json:"created_at"`
	OnChain   bool              `json:"on_chain,omitempty"`
	LedgerRef string            `json:"ledger_ref,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// Hold suspends deletion or anonymization for one record.
type Hold struct {
	ID       string `json:"id"`
	Record   string `json:"record"`
	Reason   string `json:"reason"`
	PlacedBy string `json:"placed_by"`
	PlacedAt int64  `json:"placed_at"`
}

// Tombstone replaces a deleted record.
type Tombstone struct {
	Record      string `json:"record"`
	Class       string `json:"class"`
	LedgerRef   string `json:"ledger_ref,omitempty"`
	ContentHash string `json:"content_hash"`
	Action      Action `json:"action"`
	DeletedAt   int64  `json:"deleted_at"`
}

// Event is one retention or hold log record.
type Event struct {
	Seq    uint64 `json:"seq"`
	Time   int64  `json:"time"`
	Record string `json:"record"`
	Class  string `json:"class,omitempty"`
	Action string `json:"action"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

// Result summarizes one Apply pass. IDs are sorted.
type Result struct {
	Deleted    []string    `json:"deleted"`
	Anonymized []string    `json:"anonymized"`
	Retained   []string    `json:"retained"`
	Held       []string    `json:"held"`
	Tombstones []Tombstone `json:"tombstones"`
}

// Manager holds policies, records, holds, and the log.
type Manager struct {
	mu         sync.Mutex
	policies   map[string]Policy
	records    map[string]Record
	holds      map[string]Hold
	tombstones []Tombstone
	events     []Event
	seq        uint64
}

// NewManager validates policies and returns an empty manager.
func NewManager(policies []Policy) (*Manager, error) {
	m := &Manager{
		policies: make(map[string]Policy, len(policies)),
		records:  map[string]Record{},
		holds:    map[string]Hold{},
	}
	for i, p := range policies {
		p.Class = strings.TrimSpace(p.Class)
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("%w: policies[%d]: %v", ErrInvalid, i, err)
		}
		if _, dup := m.policies[p.Class]; dup {
			return nil, fmt.Errorf("%w: duplicate class %q", ErrInvalid, p.Class)
		}
		m.policies[p.Class] = p
	}
	return m, nil
}

// Add registers an off-chain record. On-chain records are rejected.
func (m *Manager) Add(r Record) error {
	if r.OnChain {
		return ErrOnChain
	}
	if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.Class) == "" || r.CreatedAt <= 0 {
		return fmt.Errorf("%w: id, class, and created_at are required", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.records[r.ID]; dup {
		return fmt.Errorf("%w: record %q", ErrExists, r.ID)
	}
	m.records[r.ID] = r
	return nil
}

// Record returns a record by ID.
func (m *Manager) Record(id string) (Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[id]
	return r, ok
}

// Hold places a legal hold. The ID is derived from the hold fields.
func (m *Manager) Hold(h Hold) (Hold, error) {
	if strings.TrimSpace(h.Record) == "" || strings.TrimSpace(h.Reason) == "" ||
		strings.TrimSpace(h.PlacedBy) == "" || h.PlacedAt <= 0 {
		return Hold{}, fmt.Errorf("%w: record, reason, placed_by, and placed_at are required", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.records[h.Record]
	if !ok {
		return Hold{}, fmt.Errorf("%w: record %q", ErrUnknown, h.Record)
	}
	if h.ID == "" {
		h.ID = holdID(h)
	}
	if _, dup := m.holds[h.ID]; dup {
		return Hold{}, fmt.Errorf("%w: hold %q", ErrExists, h.ID)
	}
	m.holds[h.ID] = h
	m.appendLocked(Event{
		Time: h.PlacedAt, Record: r.ID, Class: r.Class, Action: "hold",
		Result: "placed", Reason: h.Reason,
	})
	return h, nil
}

// Release removes a hold.
func (m *Manager) Release(id, by string, at int64) error {
	if at <= 0 {
		at = now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holds[id]
	if !ok {
		return fmt.Errorf("%w: hold %q", ErrUnknown, id)
	}
	delete(m.holds, id)
	m.appendLocked(Event{
		Time: at, Record: h.Record, Action: "release", Result: "released",
		Reason: "released by " + strings.TrimSpace(by),
	})
	return nil
}

// Apply deletes or anonymizes expired records at the given unix time.
func (m *Manager) Apply(at int64) Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := Result{Deleted: []string{}, Anonymized: []string{}, Retained: []string{}, Held: []string{}, Tombstones: []Tombstone{}}
	ids := make([]string, 0, len(m.records))
	for id := range m.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := m.records[id]
		p, ok := m.policies[r.Class]
		if !ok {
			res.Retained = append(res.Retained, id)
			m.appendLocked(Event{Time: at, Record: id, Class: r.Class, Action: "retain", Result: "retained", Reason: "no retention policy"})
			continue
		}
		if at-r.CreatedAt < int64(p.Days)*86400 {
			res.Retained = append(res.Retained, id)
			m.appendLocked(Event{Time: at, Record: id, Class: r.Class, Action: "retain", Result: "retained", Reason: "within retention"})
			continue
		}
		if hold, held := m.holdForLocked(id); held {
			res.Held = append(res.Held, id)
			m.appendLocked(Event{Time: at, Record: id, Class: r.Class, Action: "retain", Result: "held", Reason: "legal hold " + hold.ID})
			continue
		}
		switch p.Action {
		case Delete:
			res.Tombstones = append(res.Tombstones, Tombstone{
				Record: id, Class: r.Class, LedgerRef: r.LedgerRef,
				ContentHash: contentHash(r), Action: Delete, DeletedAt: at,
			})
			delete(m.records, id)
			res.Deleted = append(res.Deleted, id)
			m.appendLocked(Event{Time: at, Record: id, Class: r.Class, Action: "delete", Result: "deleted"})
		case Anonymize:
			for _, field := range p.AnonymizeFields {
				if _, ok := r.Fields[field]; ok {
					r.Fields[field] = ""
				}
			}
			m.records[id] = r
			res.Anonymized = append(res.Anonymized, id)
			m.appendLocked(Event{Time: at, Record: id, Class: r.Class, Action: "anonymize", Result: "anonymized"})
		}
	}
	m.tombstones = append(m.tombstones, res.Tombstones...)
	return res
}

// Tombstones returns all tombstones in deletion order.
func (m *Manager) Tombstones() []Tombstone {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Tombstone(nil), m.tombstones...)
}

// Events returns log records with Seq > after, up to limit (default 100).
func (m *Manager) Events(after uint64, limit int) []Event {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Event{}
	for _, e := range m.events {
		if e.Seq <= after {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out
}

func (p Policy) validate() error {
	if p.Class == "" {
		return fmt.Errorf("class is required")
	}
	if p.Days <= 0 {
		return fmt.Errorf("class %q: days must be positive", p.Class)
	}
	switch p.Action {
	case Delete:
		if len(p.AnonymizeFields) != 0 {
			return fmt.Errorf("class %q: delete takes no anonymize_fields", p.Class)
		}
	case Anonymize:
		if len(p.AnonymizeFields) == 0 {
			return fmt.Errorf("class %q: anonymize requires anonymize_fields", p.Class)
		}
	default:
		return fmt.Errorf("class %q: action must be delete or anonymize", p.Class)
	}
	return nil
}

func (m *Manager) holdForLocked(record string) (Hold, bool) {
	var found Hold
	ok := false
	for _, h := range m.holds {
		if h.Record != record {
			continue
		}
		if !ok || h.ID < found.ID {
			found, ok = h, true
		}
	}
	return found, ok
}

func (m *Manager) appendLocked(e Event) {
	m.seq++
	e.Seq = m.seq
	if e.Time == 0 {
		e.Time = now()
	}
	m.events = append(m.events, e)
}

func holdID(h Hold) string {
	sum := sha256.Sum256([]byte("arne-hold-v1\x00" + h.Record + "\x00" + h.Reason + "\x00" + h.PlacedBy + "\x00" + strconv.FormatInt(h.PlacedAt, 10)))
	return hex.EncodeToString(sum[:16])
}

func contentHash(r Record) string {
	h := sha256.New()
	_, _ = h.Write([]byte("arne-retention-record-v1"))
	for _, part := range []string{r.ID, r.Class, strconv.FormatInt(r.CreatedAt, 10), r.LedgerRef} {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(part))
	}
	keys := make([]string, 0, len(r.Fields))
	for k := range r.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(r.Fields[k]))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:])
}

func now() int64 { return time.Now().Unix() }
