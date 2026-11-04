package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
)

type openAPIDoc struct {
	Info struct {
		Version string `json:"version"`
	} `json:"info"`
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

func loadOpenAPI(t *testing.T, path string) openAPIDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return doc
}

func openAPIKey(method, path string) string {
	return method + " " + path
}

// documented routes are the operations in the document, in a stable order.
func documentedRoutes(doc openAPIDoc) []string {
	var keys []string
	for path, item := range doc.Paths {
		for method := range item {
			switch method {
			case "get", "post", "put", "delete", "patch":
				keys = append(keys, openAPIKey(strings.ToUpper(method), path))
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// liveRoutes converts Echo's `:param` and `*` syntax to OpenAPI templates.
func liveRoutes(t *testing.T) []string {
	t.Helper()
	e := New(nil, Config{})
	var keys []string
	for _, route := range e.Router().Routes() {
		path := route.Path
		segments := strings.Split(path, "/")
		for i, segment := range segments {
			if strings.HasPrefix(segment, ":") {
				segments[i] = "{" + strings.TrimPrefix(segment, ":") + "}"
			}
			if strings.HasPrefix(segment, "*") {
				segments[i] = "{path}"
			}
		}
		keys = append(keys, openAPIKey(route.Method, strings.Join(segments, "/")))
	}
	sort.Strings(keys)
	return keys
}

func TestOpenAPIDescribesEveryRoute(t *testing.T) {
	doc := loadOpenAPI(t, "openapi.json")
	if doc.Info.Version != "1.0.0" {
		t.Fatalf("info.version = %q", doc.Info.Version)
	}
	documented := documentedRoutes(doc)
	live := liveRoutes(t)
	if strings.Join(documented, "\n") != strings.Join(live, "\n") {
		t.Fatalf("documented routes do not match live routes\nlive:\n%s\n\ndocumented:\n%s",
			strings.Join(live, "\n"), strings.Join(documented, "\n"))
	}
}

func TestEveryOperationHasResponsesAndErrorSchema(t *testing.T) {
	doc := loadOpenAPI(t, "openapi.json")
	for path, item := range doc.Paths {
		for method, raw := range item {
			switch method {
			case "get", "post", "put", "delete", "patch":
			default:
				continue
			}
			var operation struct {
				Responses map[string]struct {
					Content map[string]struct {
						Schema struct {
							Ref string `json:"$ref"`
						} `json:"schema"`
					} `json:"content"`
				} `json:"responses"`
			}
			if err := json.Unmarshal(raw, &operation); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			if len(operation.Responses) == 0 {
				t.Fatalf("%s %s has no responses", method, path)
			}
			var found bool
			for _, response := range operation.Responses {
				for _, content := range response.Content {
					if content.Schema.Ref == "#/components/schemas/Error" {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("%s %s does not reference the Error schema", method, path)
			}
		}
	}
}

func TestDeprecatedOperationCarriesTheSunset(t *testing.T) {
	doc := loadOpenAPI(t, "openapi.json")
	var operation struct {
		Deprecated  bool   `json:"deprecated"`
		Description string `json:"description"`
	}
	item := doc.Paths["/v1/transactions/{hash}/status"]
	if err := json.Unmarshal(item["get"], &operation); err != nil {
		t.Fatal(err)
	}
	if !operation.Deprecated || !strings.Contains(operation.Description, "Sunset") {
		t.Fatalf("deprecated alias = %+v", operation)
	}
}

func TestServedDocumentMatchesSpec(t *testing.T) {
	embedded, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	shared, err := os.ReadFile("../../spec/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(embedded) != string(shared) {
		t.Fatal("internal/api/openapi.json and spec/openapi.json differ")
	}

	n, _ := newTestNode(t, 0)
	request := httptest.NewRequest(http.MethodGet, "/v1/openapi.json", nil)
	recorder := httptest.NewRecorder()
	New(n, Config{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	if recorder.Body.String() != string(shared) {
		t.Fatal("served document differs from spec/openapi.json")
	}
}
