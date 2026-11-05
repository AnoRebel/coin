package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func writeRequest(t *testing.T, handler http.Handler, method, path, token, key string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestIdempotentReplaysAWriteWithoutRepeatingIt(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{APIKeys: map[string][]string{"operator-key": {"admin"}}})

	first := writeRequest(t, e, http.MethodPost, "/v1/blocks/produce", "operator-key", "produce-once", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	if n.Status().Height != 1 {
		t.Fatalf("height = %d", n.Status().Height)
	}

	retry := writeRequest(t, e, http.MethodPost, "/v1/blocks/produce", "operator-key", "produce-once", nil)
	if retry.Code != first.Code || retry.Body.String() != first.Body.String() {
		t.Fatalf("retry: %d %s; first: %s", retry.Code, retry.Body, first.Body)
	}
	if retry.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay header = %q", retry.Header().Get("Idempotent-Replay"))
	}
	if n.Status().Height != 1 {
		t.Fatalf("retry produced another block: height = %d", n.Status().Height)
	}
}

func TestIdempotencyKeyConflictAcrossWrites(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{Devnet: true, APIKeys: map[string][]string{"operator-key": {"admin"}}})

	first := writeRequest(t, e, http.MethodPost, "/v1/blocks/produce", "operator-key", "shared-key", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	conflict := writeRequest(t, e, http.MethodPost, "/v1/faucet", "operator-key", "shared-key",
		[]byte(`{"address":"arce-devnet1someone"}`))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict: %d %s", conflict.Code, conflict.Body)
	}
	var body struct{ Error struct{ Code string } }
	if err := json.Unmarshal(conflict.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestFailedWriteIsNotCached(t *testing.T) {
	ws := newWorkspace(t)
	n, _ := newTestNode(t, 0)
	e := New(n, Config{Workspace: ws})

	bad := writeRequest(t, e, http.MethodPut, "/v1/workspace/members/bob", "key-alice", "role-change",
		[]byte(`{"role":"wizard"}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad role: %d %s", bad.Code, bad.Body)
	}
	good := writeRequest(t, e, http.MethodPut, "/v1/workspace/members/bob", "key-alice", "role-change",
		[]byte(`{"role":"auditor"}`))
	if good.Code != http.StatusOK {
		t.Fatalf("retry after failure: %d %s", good.Code, good.Body)
	}
	if role, _ := ws.RoleOf("bob"); role != "auditor" {
		t.Fatalf("role = %q", role)
	}
}

func TestIdempotencyKeyValidation(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{})

	for name, header := range map[string][]string{
		"space":    {"has space"},
		"too long": {string(bytes.Repeat([]byte("x"), 129))},
		"two":      {"one", "two"},
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/blocks/produce", nil)
		for _, value := range header {
			request.Header.Add("Idempotency-Key", value)
		}
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, recorder.Code, recorder.Body)
		}
	}
}

func TestWriteCacheExpiryAndInFlight(t *testing.T) {
	var cache writeCache
	scope, fingerprint := [32]byte{1}, [32]byte{2}
	now := time.Now()

	if _, seen, err := cache.reserve(scope, fingerprint, now); err != nil || seen {
		t.Fatalf("first reserve = seen %v, %v", seen, err)
	}
	if previous, seen, err := cache.reserve(scope, fingerprint, now); err != nil || !seen || previous.status != 0 {
		t.Fatalf("in-flight reserve = %+v, seen %v, %v", previous, seen, err)
	}
	cache.complete(scope, cachedWrite{fingerprint: fingerprint, status: http.StatusOK}, now)
	if previous, seen, err := cache.reserve(scope, fingerprint, now.Add(time.Second)); err != nil || !seen || previous.status != http.StatusOK {
		t.Fatalf("completed reserve = %+v, seen %v, %v", previous, seen, err)
	}
	if _, seen, err := cache.reserve(scope, fingerprint, now.Add(idempotencyTTL+time.Second)); err != nil || seen {
		t.Fatalf("expired reserve = seen %v, %v", seen, err)
	}
}
