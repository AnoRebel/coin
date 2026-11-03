package assetmap

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type vectorFile struct {
	Version  uint32    `json:"version"`
	Mappings []Mapping `json:"mappings"`
	Cases    []struct {
		Name      string    `json:"name"`
		Kind      string    `json:"kind"`
		Version   uint32    `json:"version"`
		External  External  `json:"external"`
		Canonical string    `json:"canonical"`
		Mappings  []Mapping `json:"mappings"`
		Expect    struct {
			Error     string `json:"error"`
			Canonical string `json:"canonical"`
			Decimals  uint8  `json:"decimals"`
			Count     int    `json:"count"`
		} `json:"expect"`
	} `json:"cases"`
}

func load(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/vectors/assetmap.json")
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
	registry, err := New(file.Version, file.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var (
				got   Mapping
				err   error
				count int
			)
			switch c.Kind {
			case "resolve":
				got, err = registry.Resolve(c.External)
			case "resolve_version":
				got, err = registry.ResolveVersion(c.Version, c.External)
			case "reverse":
				count = len(registry.Reverse(c.Canonical))
			case "apply":
				err = registry.Apply(c.Version, c.Mappings)
			case "new_set":
				_, err = New(c.Version, c.Mappings)
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
			if c.Kind == "reverse" {
				if count != c.Expect.Count {
					t.Fatalf("reverse count = %d, want %d", count, c.Expect.Count)
				}
				return
			}
			if c.Kind == "resolve" || c.Kind == "resolve_version" {
				if got.Canonical != c.Expect.Canonical || got.Decimals != c.Expect.Decimals {
					t.Fatalf("mapping = %+v, want %s/%d", got, c.Expect.Canonical, c.Expect.Decimals)
				}
			}
		})
	}
}

func TestApplyRequiresSequentialVersions(t *testing.T) {
	file := load(t)
	registry, err := New(file.Version, file.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Apply(3, file.Mappings); !errors.Is(err, ErrVersion) {
		t.Fatalf("apply 3 after 1 = %v, want ErrVersion", err)
	}
	if err := registry.Apply(2, file.Mappings); err != nil {
		t.Fatalf("apply 2 after 1: %v", err)
	}
	if err := registry.Apply(2, file.Mappings); !errors.Is(err, ErrVersion) {
		t.Fatalf("re-apply 2 = %v, want ErrVersion", err)
	}
	if got := registry.Versions(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("versions = %v, want [1 2]", got)
	}
}

func TestFailedApplyLeavesRegistryUnchanged(t *testing.T) {
	file := load(t)
	registry, err := New(file.Version, file.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	bad := []Mapping{{External: External{Rail: "swift"}, Canonical: "arce:testnet/usdc"}}
	if err := registry.Apply(2, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("apply invalid set = %v, want ErrInvalid", err)
	}
	if registry.Current() != 1 {
		t.Fatalf("current = %d after a rejected apply, want 1", registry.Current())
	}
}

func TestNormalizeKeyIsCaseInsensitive(t *testing.T) {
	upper := External{Rail: EVM, ChainID: 1, Address: "0xA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48"}
	lower := External{Rail: EVM, ChainID: 1, Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"}
	if upper.Key() != lower.Key() {
		t.Fatalf("%q != %q", upper.Key(), lower.Key())
	}
	fiat := External{Rail: Fiat, Scheme: "iso4217", Code: "usd"}
	if got := fiat.Normalize().Code; got != "USD" {
		t.Fatalf("normalized code = %q, want USD", got)
	}
}
