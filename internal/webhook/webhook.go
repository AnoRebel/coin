// Package webhook signs and delivers node notifications with bounded retries.
//
// Delivery is at-least-once. Each event carries a stable delivery ID so a
// receiver can deduplicate retries, and every attempt is signed with a fresh
// timestamp so old signatures fail replay checks.
package webhook

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	HeaderDelivery  = "X-Arne-Delivery"
	HeaderTimestamp = "X-Arne-Timestamp"
	HeaderSignature = "X-Arne-Signature"

	DefaultMaxSkew = 5 * time.Minute
)

var (
	ErrReplay    = errors.New("webhook: stale timestamp")
	ErrSignature = errors.New("webhook: bad signature")
	ErrHeaders   = errors.New("webhook: missing delivery headers")
)

// Event is the signed body delivered to endpoints.
type Event struct {
	DeliveryID string `json:"delivery_id"`
	Type       string `json:"type"`
	CreatedAt  uint64 `json:"created_at"`
	Data       any    `json:"data"`
}

// Signer signs webhook bodies with Ed25519.
type Signer struct {
	private ed25519.PrivateKey
}

// NewSigner builds a signer from a 32-byte seed.
func NewSigner(seed []byte) (*Signer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("webhook: seed must be %d bytes", ed25519.SeedSize)
	}
	return &Signer{private: ed25519.NewKeyFromSeed(seed)}, nil
}

// PublicKey returns the hex-encoded public key receivers pin.
func (s *Signer) PublicKey() string {
	return hex.EncodeToString(s.private.Public().(ed25519.PublicKey))
}

// Sign returns the hex signature over timestamp and body.
func (s *Signer) Sign(timestamp uint64, body []byte) string {
	return hex.EncodeToString(ed25519.Sign(s.private, signingPayload(timestamp, body)))
}

func signingPayload(timestamp uint64, body []byte) []byte {
	head := strconv.FormatUint(timestamp, 10) + "\n"
	return append([]byte(head), body...)
}

// Verify checks a detached signature and rejects timestamps outside maxSkew.
func Verify(publicKey, timestamp string, body []byte, signature string, now time.Time, maxSkew time.Duration) error {
	if maxSkew <= 0 {
		maxSkew = DefaultMaxSkew
	}
	ts, err := strconv.ParseUint(timestamp, 10, 64)
	if err != nil {
		return ErrHeaders
	}
	delta := now.Unix() - int64(ts)
	if delta > int64(maxSkew.Seconds()) || delta < -int64(maxSkew.Seconds()) {
		return ErrReplay
	}
	pub, err := hex.DecodeString(publicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return ErrSignature
	}
	sig, err := hex.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrSignature
	}
	if !ed25519.Verify(pub, signingPayload(ts, body), sig) {
		return ErrSignature
	}
	return nil
}

// VerifyRequest verifies the standard headers and returns the delivery ID.
func VerifyRequest(h http.Header, body []byte, publicKey string, now time.Time, maxSkew time.Duration) (string, error) {
	delivery := h.Get(HeaderDelivery)
	if delivery == "" {
		return "", ErrHeaders
	}
	if err := Verify(publicKey, h.Get(HeaderTimestamp), body, h.Get(HeaderSignature), now, maxSkew); err != nil {
		return "", err
	}
	return delivery, nil
}

// Config configures a Dispatcher.
type Config struct {
	Endpoints   []string
	Signer      *Signer
	Client      *http.Client
	MaxAttempts int           // default 5
	BaseDelay   time.Duration // default 500ms, doubled per retry, capped at 30s
	QueueSize   int           // default 256
	Now         func() time.Time
	Sleep       func(time.Duration) // tests replace this
}

// Dispatcher delivers events to every endpoint in order, retrying retryable
// failures. One event is delivered to all endpoints before the next is taken
// from the queue; a slow endpoint therefore delays later events.
type Dispatcher struct {
	cfg     Config
	mu      sync.RWMutex
	queue   chan job
	wg      sync.WaitGroup
	dropped atomic.Uint64
}

type job struct {
	kind string
	data any
}

// New validates the configuration and creates a stopped dispatcher.
func New(cfg Config) (*Dispatcher, error) {
	if len(cfg.Endpoints) == 0 {
		return nil, errors.New("webhook: no endpoints")
	}
	if cfg.Signer == nil {
		return nil, errors.New("webhook: signer required")
	}
	for _, endpoint := range cfg.Endpoints {
		if endpoint == "" {
			return nil, errors.New("webhook: empty endpoint")
		}
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 500 * time.Millisecond
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	return &Dispatcher{cfg: cfg, queue: make(chan job, cfg.QueueSize)}, nil
}

// Start launches the delivery goroutine.
func (d *Dispatcher) Start() {
	queue := d.queue
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for item := range queue {
			d.deliver(item)
		}
	}()
}

// Close stops accepting events and waits for the queue to drain.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if d.queue == nil {
		d.mu.Unlock()
		return
	}
	queue := d.queue
	d.queue = nil
	close(queue)
	d.mu.Unlock()
	d.wg.Wait()
}

// Enqueue queues an event without blocking; false means the queue was full or
// the dispatcher is closed.
func (d *Dispatcher) Enqueue(kind string, data any) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.queue == nil {
		return false
	}
	select {
	case d.queue <- job{kind: kind, data: data}:
		return true
	default:
		d.dropped.Add(1)
		return false
	}
}

// Dropped reports events discarded because the queue was full.
func (d *Dispatcher) Dropped() uint64 { return d.dropped.Load() }

// PublicKey exposes the signing key receivers pin.
func (d *Dispatcher) PublicKey() string { return d.cfg.Signer.PublicKey() }

func (d *Dispatcher) deliver(item job) {
	id := randomID()
	body, err := json.Marshal(Event{
		DeliveryID: id, Type: item.kind, CreatedAt: uint64(d.cfg.Now().Unix()), Data: item.data,
	})
	if err != nil {
		return
	}
	for _, endpoint := range d.cfg.Endpoints {
		d.post(endpoint, id, body)
	}
}

func (d *Dispatcher) post(endpoint, id string, body []byte) {
	delay := d.cfg.BaseDelay
	for attempt := 1; attempt <= d.cfg.MaxAttempts; attempt++ {
		timestamp := d.cfg.Now().Unix()
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderDelivery, id)
		req.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
		req.Header.Set(HeaderSignature, d.cfg.Signer.Sign(uint64(timestamp), body))
		resp, err := d.cfg.Client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
			if !retryable(resp.StatusCode) {
				return
			}
		}
		if attempt == d.cfg.MaxAttempts {
			return
		}
		d.cfg.Sleep(delay)
		delay *= 2
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}
