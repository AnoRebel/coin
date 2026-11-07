// Package channel stores team channel messages with idempotent publishing,
// cursor catch-up, and per-channel retention.
//
// Publishing assigns a stable ID derived from the channel, sender, and client
// ID, so a sender can retry after a timeout without duplicating a message. A
// retry with a different body is rejected as a conflict. Readers page with a
// sequence cursor, which is resumable until the retention window drops the
// messages they missed. Subscriptions only carry sequence hints; readers must
// still fetch through Read, which is what makes delivery recoverable.
//
// Content is either plaintext (Text, Payment, Notice, File) or an opaque
// Ciphertext. Encrypted channels should store MLS ciphertext in Ciphertext and
// leave the plaintext fields empty. The store does not encrypt anything
// itself.
package channel

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// MaxTextBytes is the largest plaintext body.
	MaxTextBytes = 4096
	// MaxCiphertextBytes is the largest opaque payload.
	MaxCiphertextBytes = 64 << 10
	// DefaultLimit is the page size when none is given.
	DefaultLimit = 100
	// MaxLimit is the largest accepted page size.
	MaxLimit = 500
)

// Kind is a routing hint for a message payload.
type Kind string

const (
	KindText    Kind = "text"
	KindPayment Kind = "payment"
	KindNotice  Kind = "notice"
	KindFile    Kind = "file"
)

var (
	ErrInvalid   = errors.New("channel: invalid message")
	ErrConflict  = errors.New("channel: conflicting retry")
	ErrRetention = errors.New("channel: retention required")
	// ErrExpired reports a cursor whose messages have left the retention
	// window. The caller must restart from the oldest available message.
	ErrExpired = errors.New("channel: cursor expired")
)

// FilePointer points at content stored outside the channel. The store never
// carries file bytes.
type FilePointer struct {
	Hash    string `json:"hash"`
	Pointer string `json:"pointer"`
	Size    uint64 `json:"size,omitempty"`
	Name    string `json:"name,omitempty"`
}

// Message is one ordered channel entry. ID, Seq, Time, and System are assigned
// by the store.
type Message struct {
	ID       string       `json:"id,omitempty"`
	Channel  string       `json:"channel"`
	Sender   string       `json:"sender"`
	ClientID string       `json:"client_id"`
	Seq      uint64       `json:"seq,omitempty"`
	Time     uint64       `json:"time,omitempty"`
	Kind     Kind         `json:"kind,omitempty"`
	Text     string       `json:"text,omitempty"`
	Payment  string       `json:"payment,omitempty"`
	Notice   string       `json:"notice,omitempty"`
	File     *FilePointer `json:"file,omitempty"`
	System   bool         `json:"system,omitempty"`
	// Ciphertext is an opaque encrypted payload. When set, the plaintext
	// fields above must be empty.
	Ciphertext []byte `json:"ciphertext,omitempty"`
}

// MessageID derives the stable ID for a sender retry.
func MessageID(channel, sender, clientID string) string {
	h := sha256.New()
	h.Write([]byte("arne-message-v1\x00"))
	for _, part := range []string{channel, sender, clientID} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(part)))
		h.Write(size[:])
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Validate checks the content fields. Assigned fields are ignored.
func (m Message) Validate() error {
	if !validLabel(m.Channel) {
		return fmt.Errorf("%w: channel", ErrInvalid)
	}
	if !validLabel(m.Sender) {
		return fmt.Errorf("%w: sender", ErrInvalid)
	}
	if !validClientID(m.ClientID) {
		return fmt.Errorf("%w: client_id", ErrInvalid)
	}
	if len(m.Ciphertext) > 0 {
		if len(m.Ciphertext) > MaxCiphertextBytes {
			return fmt.Errorf("%w: ciphertext too large", ErrInvalid)
		}
		if m.Text != "" || m.Payment != "" || m.Notice != "" || m.File != nil {
			return fmt.Errorf("%w: ciphertext with plaintext fields", ErrInvalid)
		}
		if !validKind(m.Kind) {
			return fmt.Errorf("%w: kind", ErrInvalid)
		}
		return nil
	}
	if m.Kind != "" && !validKind(m.Kind) {
		return fmt.Errorf("%w: kind", ErrInvalid)
	}
	if len(m.Text) > MaxTextBytes || !utf8.ValidString(m.Text) {
		return fmt.Errorf("%w: text", ErrInvalid)
	}
	if m.Payment != "" && !validHash(m.Payment) {
		return fmt.Errorf("%w: payment reference", ErrInvalid)
	}
	if m.File != nil {
		if !validHash(m.File.Hash) || m.File.Pointer == "" || len(m.File.Pointer) > 2048 ||
			hasControl(m.File.Pointer) || len(m.File.Name) > 255 || hasControl(m.File.Name) {
			return fmt.Errorf("%w: file pointer", ErrInvalid)
		}
	}
	if m.Notice != "" && (len(m.Notice) > MaxTextBytes || !utf8.ValidString(m.Notice)) {
		return fmt.Errorf("%w: notice", ErrInvalid)
	}
	if m.Payment != "" && m.File != nil {
		return fmt.Errorf("%w: payment and file in one message", ErrInvalid)
	}
	if m.Text == "" && m.Payment == "" && m.Notice == "" && m.File == nil {
		return fmt.Errorf("%w: empty message", ErrInvalid)
	}
	return nil
}

// Kind returns the payload kind, deriving it for plaintext messages.
func (m Message) PayloadKind() Kind {
	if m.Kind != "" {
		return m.Kind
	}
	switch {
	case m.Notice != "":
		return KindNotice
	case m.Payment != "":
		return KindPayment
	case m.File != nil:
		return KindFile
	default:
		return KindText
	}
}

func validKind(kind Kind) bool {
	switch kind {
	case KindText, KindPayment, KindNotice, KindFile:
		return true
	}
	return false
}

func validLabel(value string) bool {
	return value != "" && len(value) <= 128 && !hasControl(value)
}

func validClientID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Config configures a Store.
type Config struct {
	// Retention is the default window for channels without an override.
	Retention time.Duration
	Now       func() time.Time
}

// Store is a concurrency-safe in-memory channel log.
type Store struct {
	mu        sync.Mutex
	now       func() time.Time
	retention time.Duration
	overrides map[string]time.Duration
	next      map[string]uint64
	logs      map[string][]Message
	seen      map[string]Message
	subs      map[string]map[chan uint64]struct{}
}

// New creates a store. Retention must be positive.
func New(cfg Config) (*Store, error) {
	if cfg.Retention <= 0 {
		return nil, ErrRetention
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Store{
		now:       now,
		retention: cfg.Retention,
		overrides: map[string]time.Duration{},
		next:      map[string]uint64{},
		logs:      map[string][]Message{},
		seen:      map[string]Message{},
		subs:      map[string]map[chan uint64]struct{}{},
	}, nil
}

// SetRetention overrides the retention window for one channel.
func (s *Store) SetRetention(channel string, retention time.Duration) error {
	if !validLabel(channel) || retention <= 0 {
		return ErrRetention
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides[channel] = retention
	return nil
}

// Publish validates and appends a message. The returned bool is true when the
// client ID was already seen with the same content, in which case the stored
// message is returned and nothing is appended.
func (s *Store) Publish(m Message) (Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := m.Validate(); err != nil {
		return Message{}, false, err
	}
	m.Kind = m.PayloadKind()
	m.ID = MessageID(m.Channel, m.Sender, m.ClientID)
	if existing, ok := s.seen[m.ID]; ok {
		if !sameContent(existing, m) {
			return Message{}, false, fmt.Errorf("%w: client_id reused", ErrConflict)
		}
		return existing, true, nil
	}
	s.next[m.Channel]++
	m.Seq = s.next[m.Channel]
	m.Time = uint64(s.now().Unix())
	m.System = m.Notice != ""
	m.Kind = m.PayloadKind()
	s.logs[m.Channel] = append(s.logs[m.Channel], m)
	s.seen[m.ID] = m
	for hint := range s.subs[m.Channel] {
		select {
		case hint <- m.Seq:
		default: // the reader catches up from the store
		}
	}
	return m, false, nil
}

// Read returns messages after a sequence cursor, oldest first. Limit defaults
// to DefaultLimit and is capped at MaxLimit. Expired messages are never
// returned. If the cursor misses messages that retention removed, Read returns
// ErrExpired and the caller should restart from the oldest message.
func (s *Store) Read(channel string, after uint64, limit int) ([]Message, error) {
	if !validLabel(channel) {
		return nil, fmt.Errorf("%w: channel", ErrInvalid)
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	logs := s.logs[channel]
	cutoff := uint64(0)
	retention := s.retentionFor(channel)
	if now := s.now().Unix(); now > int64(retention/time.Second) {
		cutoff = uint64(now - int64(retention/time.Second))
	}
	first := -1
	for i := range logs {
		if logs[i].Time >= cutoff {
			first = i
			break
		}
	}
	if after > 0 {
		if first == -1 {
			if after < s.next[channel] {
				return nil, ErrExpired
			}
		} else if after+1 < logs[first].Seq {
			return nil, ErrExpired
		}
	}
	out := make([]Message, 0, limit)
	for i := first; i >= 0 && i < len(logs) && len(out) < limit; i++ {
		if logs[i].Time >= cutoff && logs[i].Seq > after {
			out = append(out, logs[i])
		}
	}
	return out, nil
}

// Prune removes messages older than their channel retention and reports how
// many were dropped. The sequence cursor keeps increasing, so a reader that
// falls behind gets ErrExpired rather than silently skipping.
func (s *Store) Prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().Unix()
	removed := 0
	for channel, logs := range s.logs {
		cutoff := uint64(0)
		retention := s.retentionFor(channel)
		if now > int64(retention/time.Second) {
			cutoff = uint64(now - int64(retention/time.Second))
		}
		kept := logs[:0]
		for _, m := range logs {
			if m.Time < cutoff {
				delete(s.seen, m.ID)
				removed++
				continue
			}
			kept = append(kept, m)
		}
		s.logs[channel] = kept
	}
	return removed
}

// Subscribe returns sequence hints for new messages in a channel. Hints are
// best-effort and may be dropped when the buffer is full; use Read with the
// last sequence to recover. Cancel before discarding the channel.
func (s *Store) Subscribe(channel string) (<-chan uint64, func()) {
	hint := make(chan uint64, 1)
	s.mu.Lock()
	if s.subs[channel] == nil {
		s.subs[channel] = map[chan uint64]struct{}{}
	}
	s.subs[channel][hint] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		delete(s.subs[channel], hint)
		if len(s.subs[channel]) == 0 {
			delete(s.subs, channel)
		}
		s.mu.Unlock()
	}
	return hint, cancel
}

func (s *Store) retentionFor(channel string) time.Duration {
	if retention, ok := s.overrides[channel]; ok {
		return retention
	}
	return s.retention
}

func sameContent(a, b Message) bool {
	a.ID, a.Seq, a.Time, a.System = "", 0, 0, false
	b.ID, b.Seq, b.Time, b.System = "", 0, 0, false
	return reflect.DeepEqual(a, b)
}
