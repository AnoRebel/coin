package security

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type vectorFile struct {
	Allowlist []string `json:"allowlist"`
	Cases     []struct {
		Name        string            `json:"name"`
		Kind        string            `json:"kind"`
		Hosts       []string          `json:"hosts"`
		URL         string            `json:"url"`
		Credential  Credential        `json:"credential"`
		Fingerprint string            `json:"fingerprint"`
		Now         int64             `json:"now"`
		OldID       string            `json:"old_id"`
		Overlap     int64             `json:"overlap"`
		ID          string            `json:"id"`
		At          int64             `json:"at"`
		Headers     map[string]string `json:"headers"`
		Account     string            `json:"account"`
		Expect      struct {
			Error   string            `json:"error"`
			ID      string            `json:"id"`
			Host    string            `json:"host"`
			Path    string            `json:"path"`
			Port    string            `json:"port"`
			Text    string            `json:"text"`
			Headers map[string]string `json:"headers"`
			Found   *bool             `json:"found"`
			At      int64             `json:"at"`
		} `json:"expect"`
	} `json:"cases"`
}

func load(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/vectors/connector_security.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestVectors(t *testing.T) {
	file := load(t)
	allow, err := NewAllowlist(file.Allowlist)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var (
				gotHost, gotPath, gotPort string
				gotID, gotText            string
				gotHeaders                map[string]string
				gotAt                     int64
				gotFound                  bool
				err                       error
			)
			switch c.Kind {
			case "allowlist":
				_, err = NewAllowlist(c.Hosts)
			case "endpoint":
				var endpoint Endpoint
				endpoint, err = ParseEndpoint(c.URL, allow)
				gotHost, gotPath, gotPort = endpoint.Host, endpoint.Path, endpoint.Port
			case "register":
				err = registry.Register(c.Credential)
			case "authorize":
				var credential Credential
				credential, err = registry.Authorize(c.Fingerprint, c.Now)
				gotID = credential.ID
			case "rotate":
				err = registry.Rotate(c.OldID, c.Credential, c.Now, c.Overlap)
			case "revoke":
				err = registry.Revoke(c.ID, c.At)
			case "retires_at":
				gotAt, gotFound = registry.RetiresAt(c.ID)
			case "redact_url":
				gotText = RedactURL(c.URL)
			case "redact_headers":
				gotHeaders = RedactHeaders(c.Headers)
			case "redact_account":
				gotText = RedactAccount(c.Account)
			default:
				t.Fatalf("unknown kind %q", c.Kind)
			}

			if c.Expect.Error != "" {
				if err == nil || !strings.Contains(err.Error(), c.Expect.Error) {
					t.Fatalf("error = %v, want %q", err, c.Expect.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Expect.Host != "" && gotHost != c.Expect.Host {
				t.Fatalf("host = %q, want %q", gotHost, c.Expect.Host)
			}
			if c.Expect.Path != "" && gotPath != c.Expect.Path {
				t.Fatalf("path = %q, want %q", gotPath, c.Expect.Path)
			}
			if c.Expect.Port != "" && gotPort != c.Expect.Port {
				t.Fatalf("port = %q, want %q", gotPort, c.Expect.Port)
			}
			if c.Expect.ID != "" && gotID != c.Expect.ID {
				t.Fatalf("id = %q, want %q", gotID, c.Expect.ID)
			}
			if c.Expect.Text != "" && gotText != c.Expect.Text {
				t.Fatalf("text = %q, want %q", gotText, c.Expect.Text)
			}
			if c.Expect.Headers != nil && !reflect.DeepEqual(gotHeaders, c.Expect.Headers) {
				t.Fatalf("headers = %v, want %v", gotHeaders, c.Expect.Headers)
			}
			if c.Expect.Found != nil {
				if gotFound != *c.Expect.Found {
					t.Fatalf("found = %v, want %v", gotFound, *c.Expect.Found)
				}
				if gotFound && gotAt != c.Expect.At {
					t.Fatalf("retires at = %d, want %d", gotAt, c.Expect.At)
				}
			}
		})
	}
}

func TestRotationStopsTheOldCertificate(t *testing.T) {
	registry := NewRegistry()
	old := Credential{ID: "old", Fingerprint: strings.Repeat("a", 64)}
	if err := registry.Register(old); err != nil {
		t.Fatal(err)
	}
	next := Credential{ID: "new", Fingerprint: strings.Repeat("b", 64)}
	if err := registry.Rotate("old", next, 100, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Authorize(old.Fingerprint, 129); err != nil {
		t.Fatalf("old certificate inside the overlap: %v", err)
	}
	if _, err := registry.Authorize(old.Fingerprint, 130); err == nil {
		t.Fatal("old certificate still works after the overlap")
	}
	if _, err := registry.Authorize(next.Fingerprint, 100); err != nil {
		t.Fatalf("new certificate: %v", err)
	}
}

func TestRevokeNeedsAReplacementForAvailability(t *testing.T) {
	registry := NewRegistry()
	credential := Credential{ID: "c", Fingerprint: strings.Repeat("c", 64)}
	if err := registry.Register(credential); err != nil {
		t.Fatal(err)
	}
	if err := registry.Revoke("c", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Authorize(credential.Fingerprint, 5); err == nil {
		t.Fatal("revoked certificate still authorizes")
	}
}
