package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anorebel/anorebelcoin/internal/channel"
	"github.com/anorebel/anorebelcoin/internal/protocol"
)

// replaySSE calls the stream with an already-cancelled request context, so the
// handler writes the replay and returns instead of waiting for live events.
func replaySSE(t *testing.T, handler http.Handler, path string, headers map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body)
	}
	return recorder.Body.String()
}

func TestBlockStreamReplaysAfterCursor(t *testing.T) {
	n, _ := newTestNode(t, 0)
	now := uint64(time.Now().Unix())
	for i := 0; i < 3; i++ {
		if _, err := n.ProduceBlock(now + uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	e := New(n, Config{})

	body := replaySSE(t, e, "/v1/events?from=2", nil)
	if !strings.Contains(body, "id: 2\nevent: block") || !strings.Contains(body, "id: 3\nevent: block") {
		t.Fatalf("replay = %s", body)
	}
	if strings.Contains(body, "id: 1\nevent: block") {
		t.Fatalf("replay included the cursor block: %s", body)
	}

	exclusive := replaySSE(t, e, "/v1/events?from=3", nil)
	header := replaySSE(t, e, "/v1/events?from=1", map[string]string{"Last-Event-ID": "2"})
	if header != exclusive {
		t.Fatalf("Last-Event-ID ignored\nheader: %s\nfrom: %s", header, exclusive)
	}
	if strings.Contains(header, "id: 1\nevent: block") || strings.Contains(header, "id: 2\nevent: block") {
		t.Fatalf("header resumed before the cursor: %s", header)
	}
}

func TestExpiredCursorReportsEarliestBlock(t *testing.T) {
	n, _ := newTestNode(t, 2)
	now := uint64(time.Now().Unix())
	for i := 0; i < 5; i++ {
		if _, err := n.ProduceBlock(now + uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	body := replaySSE(t, New(n, Config{}), "/v1/events?from=1", nil)
	if !strings.Contains(body, "event: cursor_expired") {
		t.Fatalf("missing cursor_expired: %s", body)
	}
	var earliest struct {
		Stream   string `json:"stream"`
		Earliest string `json:"earliest"`
	}
	var data string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "earliest") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if data == "" {
		t.Fatal("no expiry payload")
	}
	if err := json.Unmarshal([]byte(data), &earliest); err != nil {
		t.Fatalf("expiry payload %q: %v", data, err)
	}
	if earliest.Stream != "blocks" || earliest.Earliest != "4" {
		t.Fatalf("expiry = %+v", earliest)
	}
	if strings.Contains(body, "id: 1\nevent: block") || strings.Contains(body, "id: 3\nevent: block") {
		t.Fatalf("pruned blocks replayed: %s", body)
	}
	for _, height := range []string{"4", "5"} {
		if !strings.Contains(body, "id: "+height+"\nevent: block") {
			t.Fatalf("retained block %s missing: %s", height, body)
		}
	}
}

func TestTransactionStreamReplayAndResume(t *testing.T) {
	n, signer := newTestNode(t, 0)
	now := uint64(time.Now().Unix())
	tx := protocol.Tx{
		Version: protocol.CurrentTxVersion, Network: testNetwork, Chain: "arne-devnet-1",
		Type: protocol.TxTransfer, Signer: signer.Address, Nonce: 0,
		CreatedAt: now, ExpiresAt: now + 3600,
		FeeAsset: protocol.NativeAsset(testNetwork), MaxFee: 1000,
		Payload: map[string]any{"asset": protocol.NativeAsset(testNetwork), "to": signer.Address, "amount": uint64(7)},
	}
	if err := tx.Sign(signer); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.Encode(tx.ToCBOR())
	if err != nil {
		t.Fatal(err)
	}
	e := New(n, Config{})
	submit := httptest.NewRecorder()
	e.ServeHTTP(submit, httptest.NewRequest(http.MethodPost, "/v1/transactions", bytes.NewReader(raw)))
	if submit.Code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", submit.Code, submit.Body)
	}
	if _, err := n.ProduceBlock(now + 1); err != nil {
		t.Fatal(err)
	}
	hash, _ := tx.Hash()

	body := replaySSE(t, e, "/v1/events?stream=transactions&from=1", nil)
	if !strings.Contains(body, "id: 1-0\nevent: transaction") {
		t.Fatalf("transaction replay = %s", body)
	}
	if !strings.Contains(body, `"hash":"`+hash+`"`) {
		t.Fatalf("receipt hash missing: %s", body)
	}
	if strings.Contains(body, "event: block") {
		t.Fatalf("transaction stream carried a block: %s", body)
	}

	resumed := replaySSE(t, e, "/v1/events?stream=transactions&from=1-0", nil)
	if strings.Contains(resumed, "event: transaction") {
		t.Fatalf("resume duplicated the transaction: %s", resumed)
	}

	blockStream := replaySSE(t, e, "/v1/events?from=0", nil)
	if strings.Contains(blockStream, "transaction") {
		t.Fatalf("block stream carried a transaction: %s", blockStream)
	}
}

func TestEventStreamValidation(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{})
	for _, path := range []string{
		"/v1/events?stream=messages",
		"/v1/events?stream=messages&channel=team",
		"/v1/events?from=abc",
		"/v1/events?stream=transactions&from=1-x",
		"/v1/events?stream=transactions&from=1--1",
	} {
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body)
		}
		var body struct{ Error struct{ Code string } }
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "INVALID_ARGUMENT" {
			t.Fatalf("%s code = %q", path, body.Error.Code)
		}
	}
}

func TestMessageStreamReplaysAndExpires(t *testing.T) {
	n, _ := newTestNode(t, 0)
	now := time.Unix(1000, 0)
	store, err := channel.New(channel.Config{
		Retention: 10 * time.Second,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, _, err := store.Publish(channel.Message{
			Channel: "team", Sender: "alice", ClientID: id, Text: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	e := New(n, Config{Messages: store})

	inclusive := replaySSE(t, e, "/v1/events?stream=messages&channel=team&from=2", nil)
	if !strings.Contains(inclusive, "id: 2\nevent: message") || !strings.Contains(inclusive, "id: 3\nevent: message") {
		t.Fatalf("message replay = %s", inclusive)
	}
	if strings.Contains(inclusive, "id: 1\nevent: message") {
		t.Fatalf("inclusive replay started before from: %s", inclusive)
	}
	header := replaySSE(t, e, "/v1/events?stream=messages&channel=team", map[string]string{"Last-Event-ID": "1"})
	if !strings.Contains(header, "id: 2\nevent: message") || strings.Contains(header, "id: 1\nevent: message") {
		t.Fatalf("header replay = %s", header)
	}

	now = now.Add(20 * time.Second)
	expired := replaySSE(t, e, "/v1/events?stream=messages&channel=team", map[string]string{"Last-Event-ID": "1"})
	if !strings.Contains(expired, "event: cursor_expired") || !strings.Contains(expired, `"earliest":"0"`) {
		t.Fatalf("expired cursor = %s", expired)
	}
}

func TestLiveMessageStream(t *testing.T) {
	n, _ := newTestNode(t, 0)
	store, err := channel.New(channel.Config{Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(n, Config{Messages: store}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/v1/events?stream=messages&channel=team", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, _, err := store.Publish(channel.Message{
		Channel: "team", Sender: "alice", ClientID: "live", Text: "hello",
	}); err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(response.Body)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(line) == "event: message" {
			return
		}
	}
	t.Fatal("no message event")
}

func TestLiveBlockStream(t *testing.T) {
	n, _ := newTestNode(t, 0)
	server := httptest.NewServer(New(n, Config{}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	// Give the handler time to subscribe before the block is produced.
	time.Sleep(200 * time.Millisecond)
	if _, err := n.ProduceBlock(uint64(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if scanner.Text() == "event: block" {
			return
		}
	}
	t.Fatalf("no live block event: %v", scanner.Err())
}
