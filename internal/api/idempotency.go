package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

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
