package paymenturi

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type vector struct {
	Now   uint64 `json:"now"`
	Valid []struct {
		Name      string   `json:"name"`
		URI       string   `json:"uri"`
		Network   string   `json:"network"`
		To        string   `json:"to"`
		Asset     string   `json:"asset"`
		Amount    string   `json:"amount"`
		Memo      string   `json:"memo"`
		Intent    string   `json:"intent"`
		Expires   uint64   `json:"expires"`
		Warnings  []string `json:"warnings"`
		Canonical string   `json:"canonical"`
	} `json:"valid"`
	Invalid []struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	} `json:"invalid"`
}

func loadVectors(t *testing.T) vector {
	t.Helper()
	raw, err := os.ReadFile("../../spec/vectors/uri.json")
	if err != nil {
		t.Fatal(err)
	}
	var f vector
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestVectors(t *testing.T) {
	f := loadVectors(t)
	for _, c := range f.Valid {
		p, err := ParseAt(c.URI, f.Now)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		if p.Network != c.Network || p.To != c.To || p.Asset != c.Asset ||
			p.Amount != c.Amount || p.Memo != c.Memo || p.Expires != c.Expires || p.Intent != c.Intent {
			t.Fatalf("%s: %+v", c.Name, p)
		}
		if len(p.Warnings) != len(c.Warnings) {
			t.Fatalf("%s warnings = %v want %v", c.Name, p.Warnings, c.Warnings)
		}
		for i := range c.Warnings {
			if p.Warnings[i] != c.Warnings[i] {
				t.Fatalf("%s warning = %q want %q", c.Name, p.Warnings[i], c.Warnings[i])
			}
		}
		if p.String() != c.Canonical {
			t.Fatalf("%s canonical = %s want %s", c.Name, p.String(), c.Canonical)
		}
		again, err := ParseAt(p.String(), f.Now)
		if err != nil || again.Summary() != p.Summary() {
			t.Fatalf("%s round trip: %v", c.Name, err)
		}
	}
	for _, c := range f.Invalid {
		if _, err := ParseAt(c.URI, f.Now); err == nil {
			t.Fatalf("%s: accepted", c.Name)
		}
	}
}

func TestConflicts(t *testing.T) {
	p, err := Parse(fixedURI)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckAmount("13"); err == nil {
		t.Fatal("amount conflict accepted")
	}
	if err := p.CheckAmount("12.5"); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckRecipient("arce-devnet1other"); err == nil {
		t.Fatal("recipient conflict accepted")
	}
	if err := p.CheckNetwork("arne:mainnet"); err == nil {
		t.Fatal("network conflict accepted")
	}
}

const fixedURI = "arce:arce-devnet1qxqew42ve7ed8503377ypzfnchqqtphmkux5ye4t?network=arne%3Adevnet&asset=arce%3Adevnet&amount=12.5"

func TestIntentURI(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	p, err := Parse(fixedURI + "&intent=" + id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.String(), "&intent="+id) || !strings.Contains(p.Summary(), id) {
		t.Fatal("intent lost from canonical URI or summary")
	}
	if err := p.CheckIntent(id); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckIntent("ffffffffffffffffffffffffffffffff"); err == nil {
		t.Fatal("intent conflict accepted")
	}
	for _, bad := range []string{"", "ABCDEF0123456789abcdef0123456789ab", "short", id + "&intent=" + id} {
		if _, err := Parse(fixedURI + "&intent=" + bad); err == nil {
			t.Fatalf("accepted intent %q", bad)
		}
	}
}
