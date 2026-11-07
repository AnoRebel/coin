// Package audit implements an append-only, hash-chained audit log.
//
// Every record commits to the previous record's hash, so changing, reordering,
// or removing an entry breaks verification. The hash covers the sequence, time,
// actor, action, target, result, and detail. Truncating the newest records is
// not detectable from the records alone; publish or sign Head to anchor the log
// so a shorter export can be compared against it.
//
// Export writes one JSON record per line and Open verifies the whole chain
// before returning a log. The chain is not signed: signatures and external
// anchoring belong to the deployment.
package audit

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// Genesis is the link before the first record.
	Genesis = "0000000000000000000000000000000000000000000000000000000000000000"
	// MaxDetail bounds free-form detail.
	MaxDetail = 1024
)

var (
	ErrInvalid  = errors.New("audit: invalid entry")
	ErrSequence = errors.New("audit: sequence gap or reordering")
	ErrChain    = errors.New("audit: broken link")
	ErrHash     = errors.New("audit: record hash mismatch")
)

// Entry is a caller-supplied audit event. The log assigns sequence, time, and
// links.
type Entry struct {
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// Record is one chained audit record.
type Record struct {
	Seq    uint64 `json:"seq"`
	Time   uint64 `json:"time"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
	Prev   string `json:"prev"`
	Hash   string `json:"hash"`
}

// Log is a concurrency-safe in-memory chain.
type Log struct {
	mu      sync.Mutex
	now     func() uint64
	records []Record
	head    string
}

// New creates an empty chain. A nil clock uses the wall clock.
func New(now func() uint64) *Log {
	if now == nil {
		now = func() uint64 { return uint64(time.Now().Unix()) }
	}
	return &Log{now: now, head: Genesis}
}

// Append validates an entry and links it after the current head.
func (l *Log) Append(entry Entry) (Record, error) {
	if err := entry.validate(); err != nil {
		return Record{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	record := Record{
		Seq:    uint64(len(l.records)) + 1,
		Time:   l.now(),
		Actor:  entry.Actor,
		Action: entry.Action,
		Target: entry.Target,
		Result: entry.Result,
		Detail: entry.Detail,
		Prev:   l.head,
	}
	record.Hash = chainHash(record)
	l.records = append(l.records, record)
	l.head = record.Hash
	return record, nil
}

// Records returns a copy of the chain in order.
func (l *Log) Records() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Record(nil), l.records...)
}

// Head returns the newest hash, or Genesis for an empty chain.
func (l *Log) Head() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.head
}

// Export writes the chain as newline-delimited JSON.
func (l *Log) Export(w io.Writer) error {
	encoder := json.NewEncoder(w)
	for _, record := range l.Records() {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return nil
}

// Open decodes and verifies a newline-delimited export. The returned log
// continues after the imported head.
func Open(r io.Reader, now func() uint64) (*Log, error) {
	decoder := json.NewDecoder(r)
	records := []Record{}
	for {
		var record Record
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		records = append(records, record)
	}
	if err := Verify(records); err != nil {
		return nil, err
	}
	log := New(now)
	log.records = records
	if len(records) > 0 {
		log.head = records[len(records)-1].Hash
	}
	return log, nil
}

// Verify checks sequence, links, and hashes for a chain in order.
func Verify(records []Record) error {
	head := Genesis
	for index, record := range records {
		if record.Seq != uint64(index)+1 {
			return fmt.Errorf("%w: record %d has sequence %d", ErrSequence, index, record.Seq)
		}
		if record.Prev != head {
			return fmt.Errorf("%w: record %d", ErrChain, index)
		}
		if record.Hash != chainHash(record) {
			return fmt.Errorf("%w: record %d", ErrHash, index)
		}
		head = record.Hash
	}
	return nil
}

func (e Entry) validate() error {
	if !validLabel(e.Actor, 128) || !validLabel(e.Action, 128) || !validLabel(e.Result, 128) {
		return fmt.Errorf("%w: actor, action, or result", ErrInvalid)
	}
	if e.Target != "" && !validLabel(e.Target, 1024) {
		return fmt.Errorf("%w: target", ErrInvalid)
	}
	if len(e.Detail) > MaxDetail || !utf8.ValidString(e.Detail) {
		return fmt.Errorf("%w: detail", ErrInvalid)
	}
	return nil
}

func chainHash(record Record) string {
	digest := sha256.New()
	digest.Write([]byte("arne-audit-v1\x00"))
	prev, _ := hex.DecodeString(record.Prev)
	digest.Write(prev)
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], record.Seq)
	digest.Write(number[:])
	binary.BigEndian.PutUint64(number[:], record.Time)
	digest.Write(number[:])
	var size [4]byte
	for _, value := range []string{record.Actor, record.Action, record.Target, record.Result, record.Detail} {
		binary.BigEndian.PutUint32(size[:], uint32(len(value)))
		digest.Write(size[:])
		digest.Write([]byte(value))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func validLabel(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
