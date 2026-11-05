package api

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

const (
	idempotencyTTL   = 24 * time.Hour
	idempotencyLimit = 10_000
)

var idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type submission struct {
	fingerprint [32]byte
	hash        string
	expires     time.Time
}

type submissions struct {
	mu      sync.Mutex
	entries map[[32]byte]submission
}

// submitWithKey serializes admission and caches only accepted transactions.
// shortcut: this cache is process-local; persist it before multi-instance API deployment.
func (s *Server) submitWithKey(raw []byte, key, principal string) (string, bool, error) {
	if key == "" {
		hash, err := s.node.SubmitTx(raw)
		return hash, false, err
	}
	if !idempotencyKey.MatchString(key) {
		return "", false, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "Idempotency-Key must be 1-128 letters, digits, _, ., :, or -")
	}
	m, err := protocol.DecodeMap(raw)
	if err != nil {
		return "", false, fmt.Errorf("%w: decode: %v", protocol.ErrValidation, err)
	}
	tx, err := protocol.TxFromCBOR(m)
	if err != nil {
		return "", false, err
	}
	scope := sha256.Sum256([]byte(strings.Join([]string{principal, tx.Network, tx.Signer, key}, "\x00")))
	fingerprint := sha256.Sum256(raw)
	s.submissions.mu.Lock()
	defer s.submissions.mu.Unlock()
	now := time.Now()
	if previous, ok := s.submissions.entries[scope]; ok {
		if now.Before(previous.expires) {
			if previous.fingerprint != fingerprint {
				return "", false, apiErr(http.StatusConflict, "IDEMPOTENCY_CONFLICT", "key already used for another signed transaction")
			}
			return previous.hash, true, nil
		}
		delete(s.submissions.entries, scope)
	}
	if len(s.submissions.entries) >= idempotencyLimit {
		for scope, previous := range s.submissions.entries {
			if !now.Before(previous.expires) {
				delete(s.submissions.entries, scope)
			}
		}
		if len(s.submissions.entries) >= idempotencyLimit {
			return "", false, apiErr(http.StatusServiceUnavailable, "IDEMPOTENCY_FULL", "idempotency cache full; retry later")
		}
	}
	hash, err := s.node.SubmitTx(raw)
	if err != nil {
		return "", false, err
	}
	if s.submissions.entries == nil {
		s.submissions.entries = make(map[[32]byte]submission)
	}
	s.submissions.entries[scope] = submission{fingerprint: fingerprint, hash: hash, expires: time.Now().Add(idempotencyTTL)}
	return hash, false, nil
}

// cachedWrite is the outcome of one successful write. A zero status marks an
// in-flight request so a concurrent retry does not run the handler twice.
type cachedWrite struct {
	fingerprint [32]byte
	status      int
	header      http.Header
	body        []byte
	expires     time.Time
}

const idempotencyPending = time.Minute

// writeCache stores successful non-transaction writes. Transaction submission
// has its own cache because a replay reports the finalized receipt.
//
// shortcut: like submissions, this cache is process-local; persist it before
// running multiple API instances behind one address.
type writeCache struct {
	mu      sync.Mutex
	entries map[[32]byte]cachedWrite
}

// reserve claims a key. It returns an existing entry when the key was seen, and
// otherwise inserts an in-flight placeholder before the handler runs.
func (w *writeCache) reserve(scope, fingerprint [32]byte, now time.Time) (cachedWrite, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.entries == nil {
		w.entries = make(map[[32]byte]cachedWrite)
	}
	if previous, ok := w.entries[scope]; ok {
		if now.Before(previous.expires) {
			return previous, true, nil
		}
		delete(w.entries, scope)
	}
	for key, previous := range w.entries {
		if !now.Before(previous.expires) {
			delete(w.entries, key)
		}
	}
	if len(w.entries) >= idempotencyLimit {
		return cachedWrite{}, false, apiErr(http.StatusServiceUnavailable, "IDEMPOTENCY_FULL",
			"idempotency cache full; retry later")
	}
	w.entries[scope] = cachedWrite{fingerprint: fingerprint, expires: now.Add(idempotencyPending)}
	return cachedWrite{}, false, nil
}

func (w *writeCache) complete(scope [32]byte, entry cachedWrite, now time.Time) {
	entry.expires = now.Add(idempotencyTTL)
	w.mu.Lock()
	w.entries[scope] = entry
	w.mu.Unlock()
}

func (w *writeCache) release(scope [32]byte) {
	w.mu.Lock()
	delete(w.entries, scope)
	w.mu.Unlock()
}

// responseCapture forwards writes to the real response while keeping a copy
// that can be replayed for a retry.
type responseCapture struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (w *responseCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseCapture) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body = append(w.body, p...)
	return w.ResponseWriter.Write(p)
}

// idempotent makes a non-transaction write safe to retry. The key is scoped to
// the credential, and a reused key with a different request is a conflict.
// Only successful responses are cached, so a caller can retry a failure.
func (s *Server) idempotent(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		values := c.Request().Header.Values("Idempotency-Key")
		if len(values) == 0 {
			return next(c)
		}
		key := values[0]
		if len(values) > 1 || !idempotencyKey.MatchString(key) {
			return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT",
				"Idempotency-Key must be 1-128 letters, digits, _, ., :, or -")
		}

		var body []byte
		if c.Request().Body != nil {
			limited, err := io.ReadAll(io.LimitReader(c.Request().Body, maxBody+1))
			if err != nil {
				return err
			}
			if len(limited) > maxBody {
				return apiErr(http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "request body above 2 MiB")
			}
			body = limited
			c.Request().Body = io.NopCloser(bytes.NewReader(body))
		}

		fingerprint := sha256.Sum256([]byte(c.Request().Method + "\x00" + c.Request().URL.Path + "\x00" + string(body)))
		scope := sha256.Sum256([]byte(rateKey(c) + "\x00" + key))
		now := time.Now()
		previous, seen, err := s.writes.reserve(scope, fingerprint, now)
		if err != nil {
			return err
		}
		if seen {
			if previous.status == 0 {
				return apiErr(http.StatusConflict, "IDEMPOTENCY_CONFLICT",
					"a request with this key is in progress")
			}
			if previous.fingerprint != fingerprint {
				return apiErr(http.StatusConflict, "IDEMPOTENCY_CONFLICT",
					"key already used for a different request")
			}
			for name, values := range previous.header {
				c.Response().Header()[name] = append([]string(nil), values...)
			}
			c.Response().Header().Set("Idempotent-Replay", "true")
			c.Response().WriteHeader(previous.status)
			_, err := c.Response().Write(previous.body)
			return err
		}

		original := c.Response()
		capture := &responseCapture{ResponseWriter: original}
		c.SetResponse(capture)
		handlerErr := next(c)
		c.SetResponse(original)
		if handlerErr != nil {
			s.writes.release(scope)
			return handlerErr
		}
		if capture.status < 200 || capture.status >= 300 {
			s.writes.release(scope)
			return nil
		}
		header := make(http.Header, len(capture.Header()))
		for name, values := range capture.Header() {
			header[name] = append([]string(nil), values...)
		}
		s.writes.complete(scope, cachedWrite{
			fingerprint: fingerprint, status: capture.status,
			header: header, body: append([]byte(nil), capture.body...),
		}, now)
		return nil
	}
}
