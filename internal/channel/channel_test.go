package channel

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type messagingVectors struct {
	Identity []struct {
		Channel  string `json:"channel"`
		Sender   string `json:"sender"`
		ClientID string `json:"client_id"`
		ID       string `json:"id"`
	} `json:"identity"`
	Valid   []vectorMessage `json:"valid"`
	Invalid []vectorMessage `json:"invalid"`
}

type vectorMessage struct {
	Name    string  `json:"name"`
	Message Message `json:"message"`
	Error   string  `json:"error"`
}

func TestMessagingVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/messaging.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors messagingVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, test := range vectors.Identity {
		if got := MessageID(test.Channel, test.Sender, test.ClientID); got != test.ID {
			t.Fatalf("%s/%s/%s: %s, want %s", test.Channel, test.Sender, test.ClientID, got, test.ID)
		}
	}
	for _, test := range vectors.Valid {
		if err := test.Message.Validate(); err != nil {
			t.Fatalf("%s: %v", test.Name, err)
		}
	}
	for _, test := range vectors.Invalid {
		if err := test.Message.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: want invalid, got %v", test.Name, err)
		}
	}
}

func testStore(t *testing.T, now *time.Time, retention time.Duration) *Store {
	t.Helper()
	store, err := New(Config{Retention: retention, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func publish(t *testing.T, store *Store, message Message) Message {
	t.Helper()
	stored, _, err := store.Publish(message)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestPublishDeduplicatesRetry(t *testing.T) {
	now := time.Unix(1000, 0)
	store := testStore(t, &now, time.Hour)
	message := Message{Channel: "team", Sender: "alice", ClientID: "c1", Text: "hello"}
	first := publish(t, store, message)
	now = now.Add(time.Second)
	second, duplicate, err := store.Publish(message)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate || second.ID != first.ID || second.Seq != first.Seq {
		t.Fatalf("retry was not deduplicated: %+v", second)
	}
	if messages, _ := store.Read("team", 0, 0); len(messages) != 1 {
		t.Fatalf("stored %d messages, want 1", len(messages))
	}
	changed := Message{Channel: "team", Sender: "alice", ClientID: "c1", Text: "changed"}
	if _, _, err := store.Publish(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestOrderingAndCatchUp(t *testing.T) {
	now := time.Unix(1000, 0)
	store := testStore(t, &now, time.Hour)
	publish(t, store, Message{Channel: "team", Sender: "alice", ClientID: "a", Text: "one"})
	publish(t, store, Message{Channel: "team", Sender: "bob", ClientID: "b", Text: "two"})
	publish(t, store, Message{Channel: "other", Sender: "carol", ClientID: "c", Text: "three"})
	publish(t, store, Message{Channel: "team", Sender: "alice", ClientID: "d", Text: "four"})

	page, err := store.Read("team", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 || page[0].Seq != 1 || page[1].Seq != 2 || page[2].Seq != 3 {
		t.Fatalf("unexpected page: %+v", page)
	}
	page, err = store.Read("team", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Text != "two" {
		t.Fatalf("unexpected catch-up: %+v", page)
	}
	if page, _ := store.Read("team", 3, 0); len(page) != 0 {
		t.Fatalf("caught-up read returned %d messages", len(page))
	}
}

func TestRetentionExpiryAndCursor(t *testing.T) {
	now := time.Unix(0, 0)
	store := testStore(t, &now, 10*time.Second)
	publish(t, store, Message{Channel: "team", Sender: "alice", ClientID: "a", Text: "one"})
	now = time.Unix(100, 0)
	publish(t, store, Message{Channel: "team", Sender: "alice", ClientID: "b", Text: "two"})

	page, err := store.Read("team", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Text != "two" {
		t.Fatalf("retention returned %+v", page)
	}
	if _, err := store.Read("team", 1, 0); err != nil {
		t.Fatalf("cursor at the retained message: %v", err)
	}
	if removed := store.Prune(); removed != 1 {
		t.Fatalf("pruned %d, want 1", removed)
	}
	now = time.Unix(120, 0)
	if page, err := store.Read("team", 0, 0); err != nil || len(page) != 0 {
		t.Fatalf("read after expiry: %+v %v", page, err)
	}
	if _, err := store.Read("team", 1, 0); !errors.Is(err, ErrExpired) {
		t.Fatalf("want cursor expired, got %v", err)
	}
}

func TestPerChannelRetention(t *testing.T) {
	now := time.Unix(100, 0)
	store := testStore(t, &now, time.Hour)
	if err := store.SetRetention("short", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	publish(t, store, Message{Channel: "short", Sender: "alice", ClientID: "a", Text: "gone"})
	publish(t, store, Message{Channel: "long", Sender: "alice", ClientID: "b", Text: "kept"})
	now = time.Unix(120, 0)
	if page, _ := store.Read("short", 0, 0); len(page) != 0 {
		t.Fatalf("short channel retained %d messages", len(page))
	}
	if page, _ := store.Read("long", 0, 0); len(page) != 1 {
		t.Fatalf("long channel dropped its message")
	}
}

func TestSubscribeHints(t *testing.T) {
	now := time.Unix(100, 0)
	store := testStore(t, &now, time.Hour)
	hints, cancel := store.Subscribe("team")
	defer cancel()
	publish(t, store, Message{Channel: "team", Sender: "alice", ClientID: "a", Text: "one"})
	select {
	case seq := <-hints:
		if seq != 1 {
			t.Fatalf("hint %d, want 1", seq)
		}
	case <-time.After(time.Second):
		t.Fatal("no hint")
	}
}

func TestOversizeTextAndCiphertext(t *testing.T) {
	base := Message{Channel: "team", Sender: "alice", ClientID: "a"}
	base.Text = strings.Repeat("a", MaxTextBytes+1)
	if err := base.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize text: %v", err)
	}
	sealed := Message{Channel: "team", Sender: "alice", ClientID: "b", Kind: KindText}
	sealed.Ciphertext = make([]byte, MaxCiphertextBytes+1)
	if err := sealed.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize ciphertext: %v", err)
	}
}
