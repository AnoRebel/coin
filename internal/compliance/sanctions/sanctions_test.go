package sanctions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type vector struct {
	List struct {
		Name      string  `json:"name"`
		Version   string  `json:"version"`
		UpdatedAt int64   `json:"updated_at"`
		Entries   []Entry `json:"entries"`
	} `json:"list"`
	Cases []struct {
		Name          string   `json:"name"`
		Subject       Subject  `json:"subject"`
		ProviderError bool     `json:"provider_error"`
		Outcome       Outcome  `json:"outcome"`
		Result        Result   `json:"result"`
		Matches       []string `json:"matches"`
		Score         int      `json:"score"`
	} `json:"cases"`
	IDCases []struct {
		Address string `json:"address"`
		Name    string `json:"name"`
		ID      string `json:"id"`
	} `json:"id_cases"`
	InvalidLists []struct {
		Name    string  `json:"name"`
		Version string  `json:"version"`
		Entries []Entry `json:"entries"`
	} `json:"invalid_lists"`
}

func loadVectors(t *testing.T) vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "sanctions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSharedVectors(t *testing.T) {
	v := loadVectors(t)
	lp, err := NewListProvider(List{
		Name: v.List.Name, Version: v.List.Version, UpdatedAt: v.List.UpdatedAt, Entries: v.List.Entries,
	})
	if err != nil {
		t.Fatal(err)
	}
	screener, err := New(lp)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		s := screener
		if c.ProviderError {
			s, err = New(UnavailableProvider{ProviderName: "list:" + v.List.Name, Reason: "timeout"})
			if err != nil {
				t.Fatal(err)
			}
		}
		d := s.Screen(context.Background(), c.Subject)
		if d.Outcome != c.Outcome || d.Result != c.Result {
			t.Fatalf("%s: got %s/%s (%s), want %s/%s", c.Name, d.Outcome, d.Result, d.Reason, c.Outcome, c.Result)
		}
		if c.ProviderError {
			if d.ListVersion != "" {
				t.Fatalf("%s: outage leaked a list version %q", c.Name, d.ListVersion)
			}
			if d.Outcome != Deny || d.RequiresReview || !strings.Contains(d.Reason, "unavailable") {
				t.Fatalf("%s: outage was not a clean deny: %+v", c.Name, d)
			}
			continue
		}
		if d.ListVersion != v.List.Version {
			t.Fatalf("%s: list version = %q", c.Name, d.ListVersion)
		}
		if d.Provider != lp.Name() {
			t.Fatalf("%s: provider = %q", c.Name, d.Provider)
		}
		if len(c.Matches) == 0 {
			if len(d.Matches) != 0 {
				t.Fatalf("%s: unexpected matches %v", c.Name, d.Matches)
			}
			continue
		}
		if !reflect.DeepEqual(d.Matches, c.Matches) {
			t.Fatalf("%s: matches = %v, want %v", c.Name, d.Matches, c.Matches)
		}
		if d.RequiresReview != (c.Outcome == Hold) {
			t.Fatalf("%s: requires_review = %v", c.Name, d.RequiresReview)
		}
		if d.Score != c.Score {
			t.Fatalf("%s: score = %d, want %d", c.Name, d.Score, c.Score)
		}
	}
	for _, c := range v.IDCases {
		if got := entryID(Entry{Address: c.Address, Name: c.Name}); got != c.ID {
			t.Fatalf("entry id = %s, want %s", got, c.ID)
		}
	}
	for i, bad := range v.InvalidLists {
		if _, err := NewListProvider(List{Name: bad.Name, Version: bad.Version, Entries: bad.Entries}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid list %d: %v", i, err)
		}
	}
}

func TestScreenerRejectsNilAndUnknownResult(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil provider: %v", err)
	}
	s, err := New(staticProvider{Match{Result: Result("maybe")}})
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Screen(context.Background(), Subject{}); d.Outcome != Deny || d.Result != Unavailable {
		t.Fatalf("unknown result: %+v", d)
	}
	s, err = New(staticProvider{Match{Result: Clear, ListVersion: "v9"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := s.Screen(context.Background(), Subject{}); d.Outcome != Allow || d.ListVersion != "v9" {
		t.Fatalf("clear: %+v", d)
	}
}

type staticProvider struct{ match Match }

func (s staticProvider) Name() string { return "static" }

func (s staticProvider) Screen(context.Context, Subject) (Match, error) { return s.match, nil }

func TestListProviderEntryID(t *testing.T) {
	p, err := NewListProvider(List{
		Name: "l", Version: "v1",
		Entries: []Entry{{Address: " ABC "}, {Name: "Jane   Doe"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.byID) != 2 {
		t.Fatalf("entries = %d", len(p.byID))
	}
	for id := range p.byID {
		if len(id) != 16 {
			t.Fatalf("derived id = %q", id)
		}
	}
	// The same party derives the same ID regardless of formatting.
	again, err := NewListProvider(List{Name: "l", Version: "v2", Entries: []Entry{{Address: "abc"}}})
	if err != nil {
		t.Fatal(err)
	}
	for id := range again.byID {
		if _, ok := p.byID[id]; !ok {
			t.Fatalf("id %q not stable", id)
		}
	}
}
