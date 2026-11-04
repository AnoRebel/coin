package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func getJSON(t *testing.T, e http.Handler, path string, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, rec.Body)
		}
	}
	return rec
}

type blockPage struct {
	Items []struct {
		Height   uint64 `json:"height"`
		Proposer string `json:"proposer"`
	} `json:"items"`
	NextCursor uint64 `json:"next_cursor"`
}

func TestBlockPaginationAndFilter(t *testing.T) {
	n, _ := newTestNode(t, 0)
	now := uint64(time.Now().Unix())
	for i := 0; i < 5; i++ {
		if _, err := n.ProduceBlock(now + uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	e := New(n, Config{})

	var page blockPage
	if rec := getJSON(t, e, "/v1/blocks?limit=2", &page); rec.Code != http.StatusOK {
		t.Fatalf("page 1: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 2 || page.Items[0].Height != 5 || page.Items[1].Height != 4 || page.NextCursor != 4 {
		t.Fatalf("page 1 = %+v", page)
	}

	page = blockPage{}
	if rec := getJSON(t, e, "/v1/blocks?limit=2&cursor=4", &page); rec.Code != http.StatusOK {
		t.Fatalf("page 2: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 2 || page.Items[0].Height != 3 || page.Items[1].Height != 2 || page.NextCursor != 2 {
		t.Fatalf("page 2 = %+v", page)
	}

	page = blockPage{}
	if rec := getJSON(t, e, "/v1/blocks?limit=2&cursor=2", &page); rec.Code != http.StatusOK {
		t.Fatalf("page 3: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 1 || page.Items[0].Height != 1 || page.NextCursor != 0 {
		t.Fatalf("page 3 = %+v", page)
	}

	page = blockPage{}
	if rec := getJSON(t, e, "/v1/blocks?limit=2&order=asc", &page); rec.Code != http.StatusOK {
		t.Fatalf("asc: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 2 || page.Items[0].Height != 1 || page.Items[1].Height != 2 || page.NextCursor != 2 {
		t.Fatalf("asc = %+v", page)
	}

	page = blockPage{}
	if rec := getJSON(t, e, "/v1/blocks?limit=10&order=asc", &page); rec.Code != http.StatusOK {
		t.Fatalf("all: %d %s", rec.Code, rec.Body)
	}
	proposer := page.Items[0].Proposer
	if proposer == "" {
		t.Fatal("block has no proposer")
	}
	page = blockPage{}
	if rec := getJSON(t, e, "/v1/blocks?proposer="+proposer, &page); rec.Code != http.StatusOK {
		t.Fatalf("proposer: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 5 {
		t.Fatalf("proposer page = %+v", page)
	}
	for _, item := range page.Items {
		if item.Proposer != proposer {
			t.Fatalf("proposer filter leaked %q", item.Proposer)
		}
	}
	page = blockPage{}
	if rec := getJSON(t, e, "/v1/blocks?proposer=nobody", &page); rec.Code != http.StatusOK {
		t.Fatalf("unknown proposer: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 0 || page.NextCursor != 0 {
		t.Fatalf("unknown proposer = %+v", page)
	}
}

func TestBlockQueryValidation(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{})
	for _, path := range []string{
		"/v1/blocks?limit=0",
		"/v1/blocks?limit=1001",
		"/v1/blocks?limit=abc",
		"/v1/blocks?cursor=abc",
		"/v1/blocks?order=sideways",
		"/v1/blocks?sort=hash",
		"/v1/blocks?before=2",
	} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		var body struct{ Error struct{ Code string } }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Code != "INVALID_ARGUMENT" {
			t.Fatalf("%s code = %q", path, body.Error.Code)
		}
	}
}

type assetPage struct {
	Items []struct {
		ID     string `json:"id"`
		Class  string `json:"class"`
		Issuer string `json:"issuer"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func TestAssetPaginationAndFilters(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{})

	var all assetPage
	if rec := getJSON(t, e, "/v1/assets?order=asc", &all); rec.Code != http.StatusOK {
		t.Fatalf("all: %d %s", rec.Code, rec.Body)
	}
	if len(all.Items) != 2 {
		t.Fatalf("expected two genesis assets, got %+v", all)
	}

	var page assetPage
	if rec := getJSON(t, e, "/v1/assets?limit=1&order=asc", &page); rec.Code != http.StatusOK {
		t.Fatalf("page 1: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 1 || page.Items[0].ID != all.Items[0].ID || page.NextCursor != all.Items[0].ID {
		t.Fatalf("page 1 = %+v", page)
	}
	page = assetPage{}
	if rec := getJSON(t, e, "/v1/assets?limit=1&order=asc&cursor="+all.Items[0].ID, &page); rec.Code != http.StatusOK {
		t.Fatalf("page 2: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 1 || page.Items[0].ID != all.Items[1].ID || page.NextCursor != "" {
		t.Fatalf("page 2 = %+v", page)
	}

	page = assetPage{}
	if rec := getJSON(t, e, "/v1/assets?class=fiat", &page); rec.Code != http.StatusOK {
		t.Fatalf("class: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) != 1 || page.Items[0].Class != "fiat" {
		t.Fatalf("class filter = %+v", page)
	}
	page = assetPage{}
	var issuer string
	for _, item := range all.Items {
		if item.Issuer != "" {
			issuer = item.Issuer
		}
	}
	if issuer == "" {
		t.Fatal("no genesis asset has an issuer")
	}
	page = assetPage{}
	if rec := getJSON(t, e, "/v1/assets?issuer="+issuer, &page); rec.Code != http.StatusOK {
		t.Fatalf("issuer: %d %s", rec.Code, rec.Body)
	}
	if len(page.Items) == 0 {
		t.Fatal("issuer filter returned nothing")
	}
	for _, item := range page.Items {
		if item.Issuer != issuer {
			t.Fatalf("issuer filter leaked %q", item.Issuer)
		}
	}

	for _, path := range []string{
		"/v1/assets?cursor=bad%20cursor",
		"/v1/assets?order=sideways",
		"/v1/assets?unknown=1",
	} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestDeprecatedStatusAliasAdvertisesSunset(t *testing.T) {
	n, _ := newTestNode(t, 0)
	e := New(n, Config{})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/transactions/"+strings.Repeat("0", 64)+"/status", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Deprecation"); got != "true" {
		t.Fatalf("Deprecation = %q", got)
	}
	if got := rec.Header().Get("Sunset"); got != "Tue, 04 May 2027 00:00:00 GMT" {
		t.Fatalf("Sunset = %q", got)
	}
	if got := rec.Header().Get("Link"); !strings.Contains(got, "successor-version") {
		t.Fatalf("Link = %q", got)
	}
}
