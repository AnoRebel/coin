package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSignAndVerify(t *testing.T) {
	s := testSigner(t)
	body := []byte(`{"hello":"world"}`)
	now := time.Unix(1_700_000_000, 0)
	sig := s.Sign(uint64(now.Unix()), body)
	if err := Verify(s.PublicKey(), "1700000000", body, sig, now, DefaultMaxSkew); err != nil {
		t.Fatal(err)
	}
	if err := Verify(s.PublicKey(), "1700000000", []byte(`{"hello":"evil"}`), sig, now, DefaultMaxSkew); err != ErrSignature {
		t.Fatalf("tampered body error = %v", err)
	}
	if err := Verify(s.PublicKey(), "1699999000", body, sig, now, DefaultMaxSkew); err != ErrReplay {
		t.Fatalf("stale timestamp error = %v", err)
	}
	if err := Verify(s.PublicKey(), "1700001000", body, sig, now, DefaultMaxSkew); err != ErrReplay {
		t.Fatalf("future timestamp error = %v", err)
	}
}

func TestVerifyRequest(t *testing.T) {
	s := testSigner(t)
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{}`)
	h := http.Header{}
	h.Set(HeaderDelivery, "d1")
	h.Set(HeaderTimestamp, "1700000000")
	h.Set(HeaderSignature, s.Sign(uint64(now.Unix()), body))
	id, err := VerifyRequest(h, body, s.PublicKey(), now, 0)
	if err != nil || id != "d1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, err := VerifyRequest(http.Header{}, body, s.PublicKey(), now, 0); err != ErrHeaders {
		t.Fatalf("missing headers error = %v", err)
	}
}

func TestDispatcherRetriesWithSameDeliveryID(t *testing.T) {
	var attempts atomic.Int32
	var ids []string
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		ids = append(ids, r.Header.Get(HeaderDelivery))
		if attempts.Load() < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, err := New(Config{
		Endpoints: []string{srv.URL}, Signer: testSigner(t), MaxAttempts: 3,
		Sleep: func(time.Duration) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Start()
	if !d.Enqueue("payment", map[string]string{"tx": "h1"}) {
		t.Fatal("enqueue failed")
	}
	d.Close()

	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[0] || string(bodies[i]) != string(bodies[0]) {
			t.Fatal("retry changed delivery ID or body")
		}
	}
	var event Event
	if err := json.Unmarshal(bodies[0], &event); err != nil || event.Type != "payment" {
		t.Fatalf("event = %+v err=%v", event, err)
	}
}

func TestDispatcherDoesNotRetryClientError(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	d, _ := New(Config{Endpoints: []string{srv.URL}, Signer: testSigner(t), MaxAttempts: 5, Sleep: func(time.Duration) {}})
	d.Start()
	d.Enqueue("payment", nil)
	d.Close()
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
}

func TestDispatcherDropsWhenQueueFull(t *testing.T) {
	d, _ := New(Config{Endpoints: []string{"http://example.invalid"}, Signer: testSigner(t), QueueSize: 1})
	if !d.Enqueue("a", nil) {
		t.Fatal("first enqueue failed")
	}
	if d.Enqueue("b", nil) {
		t.Fatal("second enqueue accepted")
	}
	if d.Dropped() != 1 {
		t.Fatalf("dropped = %d", d.Dropped())
	}
	d.Close()
}
