package api

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

func keyedTx(t *testing.T, signer *protocol.KeyPair, nonce uint64) []byte {
	t.Helper()
	now := uint64(time.Now().Unix())
	tx := protocol.Tx{
		Version: 1, Network: testNetwork, Chain: "arne-devnet-1", Type: protocol.TxTransfer,
		Signer: signer.Address, Nonce: nonce, CreatedAt: now, ExpiresAt: now + 3600,
		FeeAsset: protocol.NativeAsset(testNetwork), MaxFee: 1000,
		Payload: map[string]any{"asset": protocol.NativeAsset(testNetwork), "to": signer.Address, "amount": uint64(42)},
	}
	if err := tx.Sign(signer); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestKeyedSubmitConcurrencyAndScope(t *testing.T) {
	n, signer := newTestNode(t, 0)
	var calls atomic.Int32
	n.SetBroadcast(func([]byte) error { calls.Add(1); return nil })
	s := &Server{node: n}
	raw := keyedTx(t, signer, 0)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.submitWithKey(raw, "invoice", "Bearer one"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent retry submitted %d times", calls.Load())
	}
	if _, replay, err := s.submitWithKey(raw, "invoice", "Bearer two"); err != nil || replay {
		t.Fatalf("credentials shared cache: replay=%v err=%v", replay, err)
	}
	other, _, err := protocol.NewKeyPair(testNetwork)
	if err != nil {
		t.Fatal(err)
	}
	// A separate signer with the same key has no cached result from the funder.
	if _, replay, err := s.submitWithKey(keyedTx(t, other, 0), "invoice", "Bearer one"); err == nil || replay {
		t.Fatalf("signers shared cache: replay=%v err=%v", replay, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("broadcast calls = %d", calls.Load())
	}
}

func TestKeyedSubmitExpiryCapacityAndFailure(t *testing.T) {
	n, signer := newTestNode(t, 0)
	s := &Server{node: n}
	raw := keyedTx(t, signer, 0)
	n.SetBroadcast(func([]byte) error { return errors.New("unavailable") })
	if _, _, err := s.submitWithKey(raw, "invoice", ""); err == nil {
		t.Fatal("failed broadcast accepted")
	}
	n.SetBroadcast(func([]byte) error { return nil })
	if _, replay, err := s.submitWithKey(raw, "invoice", ""); err != nil || replay {
		t.Fatalf("failure reserved key: replay=%v err=%v", replay, err)
	}
	for scope, entry := range s.submissions.entries {
		entry.expires = time.Now().Add(-time.Second)
		s.submissions.entries[scope] = entry
	}
	if _, replay, err := s.submitWithKey(keyedTx(t, signer, 1), "invoice", ""); err != nil || replay {
		t.Fatalf("expired key not reusable: replay=%v err=%v", replay, err)
	}
	for i := len(s.submissions.entries); i < idempotencyLimit; i++ {
		scope := sha256.Sum256([]byte(fmt.Sprint(i)))
		s.submissions.entries[scope] = submission{expires: time.Now().Add(time.Hour)}
	}
	_, _, err := s.submitWithKey(raw, "new", "")
	var ae *apiError
	if !errors.As(err, &ae) || ae.status != http.StatusServiceUnavailable || ae.code != "IDEMPOTENCY_FULL" {
		t.Fatalf("full cache = %v", err)
	}
	for scope, entry := range s.submissions.entries {
		entry.expires = time.Now().Add(-time.Second)
		s.submissions.entries[scope] = entry
		break
	}
	if _, _, err := s.submitWithKey(raw, "new", ""); err != nil {
		t.Fatalf("expired capacity not reclaimed: %v", err)
	}
}

func TestKeyedMempoolConflictDoesNotReserveKey(t *testing.T) {
	n, signer := newTestNode(t, 0)
	s := &Server{node: n}
	first := keyedTx(t, signer, 0)
	if _, _, err := s.submitWithKey(first, "first", ""); err != nil {
		t.Fatal(err)
	}
	m, _ := protocol.DecodeMap(first)
	tx, _ := protocol.TxFromCBOR(m)
	tx.Payload["amount"] = uint64(43)
	tx.Signatures = nil
	if err := tx.Sign(signer); err != nil {
		t.Fatal(err)
	}
	second, _ := protocol.Encode(tx.ToCBOR())
	if _, _, err := s.submitWithKey(second, "second", ""); err == nil {
		t.Fatal("transaction dropped from mempool was reported accepted")
	}
	if _, err := n.ProduceBlock(uint64(time.Now().Unix()) + 1); err != nil {
		t.Fatal(err)
	}
	tx.Nonce = 1
	tx.Signatures = nil
	if err := tx.Sign(signer); err != nil {
		t.Fatal(err)
	}
	corrected, _ := protocol.Encode(tx.ToCBOR())
	if _, replay, err := s.submitWithKey(corrected, "second", ""); err != nil || replay {
		t.Fatalf("rejected submission reserved key: replay=%v err=%v", replay, err)
	}
}
