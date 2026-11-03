package vc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/protocol"
)

type vectorFile struct {
	IssuerSeed   string            `json:"issuer_seed"`
	Network      string            `json:"network"`
	Issuer       string            `json:"issuer"`
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	IssuedAt     int64             `json:"issued_at"`
	ExpiresAt    int64             `json:"expires_at"`
	Claims       map[string]any    `json:"claims"`
	Salts        map[string]string `json:"salts"`
	IssuerPublic string            `json:"issuer_public"`
	Credential   string            `json:"credential"`
	Cases        []vectorCase      `json:"cases"`
}

type vectorCase struct {
	Name     string         `json:"name"`
	Disclose []string       `json:"disclose"`
	Require  map[string]any `json:"require"`
	Now      int64          `json:"now"`
	Revoked  []string       `json:"revoked"`
	Tamper   bool           `json:"tamper"`
	Expect   struct {
		Claims map[string]any `json:"claims"`
		Error  string         `json:"error"`
	} `json:"expect"`
}

func loadVectors(t *testing.T) (vectorFile, *protocol.KeyPair) {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/vectors/vc.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(file.IssuerSeed)
	if err != nil {
		t.Fatal(err)
	}
	key, err := protocol.KeyPairFromPrivate(seed, file.Network)
	if err != nil {
		t.Fatal(err)
	}
	return file, key
}

func TestVectors(t *testing.T) {
	file, key := loadVectors(t)
	if got := hex.EncodeToString(key.Public); got != file.IssuerPublic {
		t.Fatalf("issuer public = %s, want %s", got, file.IssuerPublic)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			presented := Present(file.Credential, c.Disclose...)
			if c.Tamper {
				presented = tamper(presented)
			} else if got, want := countDisclosures(presented), len(c.Disclose); got != want {
				t.Fatalf("disclosures = %d, want %d", got, want)
			}
			result, err := Verify(presented, key.Public, VerifyOptions{
				Now: c.Now, Require: c.Require, Revoked: c.Revoked, Disclose: c.Disclose,
			})
			if c.Expect.Error != "" {
				if err == nil || !strings.Contains(err.Error(), c.Expect.Error) {
					t.Fatalf("error = %v, want %q", err, c.Expect.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Claims, c.Expect.Claims) {
				t.Fatalf("claims = %v, want %v", result.Claims, c.Expect.Claims)
			}
			if result.Issuer != file.Issuer || result.ID != file.ID || result.Type != file.Type {
				t.Fatalf("result = %+v", result)
			}
			if result.IssuedAt != file.IssuedAt || result.ExpiresAt != file.ExpiresAt {
				t.Fatalf("times = %d/%d", result.IssuedAt, result.ExpiresAt)
			}
		})
	}
}

func TestIssueMatchesPythonVector(t *testing.T) {
	file, key := loadVectors(t)
	salts := map[string][]byte{}
	for name, raw := range file.Salts {
		salt, err := hex.DecodeString(raw)
		if err != nil {
			t.Fatal(err)
		}
		salts[name] = salt
	}
	credential, err := Issue(file.Claims, IssueOptions{
		Issuer: file.Issuer, ID: file.ID, Type: file.Type,
		IssuedAt: file.IssuedAt, ExpiresAt: file.ExpiresAt, Salts: salts,
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	if credential != file.Credential {
		t.Fatalf("credential mismatch\n got %s\nwant %s", credential, file.Credential)
	}
}

func TestExpiryBoundary(t *testing.T) {
	file, key := loadVectors(t)
	_, err := Verify(file.Credential, key.Public, VerifyOptions{Now: file.ExpiresAt})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("error = %v, want ErrExpired", err)
	}
}

func countDisclosures(credential string) int {
	parts := strings.Split(credential, "~")
	return len(parts) - 1
}

func tamper(credential string) string {
	head, rest, _ := strings.Cut(credential, "~")
	segments := strings.Split(head, ".")
	sig := []byte(segments[2])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	segments[2] = string(sig)
	return strings.Join(segments, ".") + "~" + rest
}
