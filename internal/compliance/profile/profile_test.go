package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type vectorConfig struct {
	Profiles              []Kind            `json:"profiles"`
	Threshold             uint64            `json:"threshold"`
	JurisdictionThreshold map[string]uint64 `json:"jurisdiction_threshold"`
}

func TestSharedVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name     string            `json:"name"`
			Config   vectorConfig      `json:"config"`
			Target   string            `json:"target"`
			Metadata map[string]string `json:"metadata"`
			Transfer Transfer          `json:"transfer"`
			Fields   map[string]string `json:"fields"`
			OK       bool              `json:"ok"`
			Field    string            `json:"field"`
		} `json:"cases"`
		ProfileFields map[string]struct {
			Required      []string `json:"required"`
			RetentionDays int      `json:"retention_days"`
		} `json:"profile_fields"`
		DisclaimerContains string `json:"disclaimer_contains"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Cases {
		cfg := Config{
			Network: "arne:devnet", Profiles: c.Config.Profiles,
			FATFThreshold: c.Config.Threshold, JurisdictionThreshold: c.Config.JurisdictionThreshold,
		}
		var err error
		switch c.Target {
		case "asset":
			err = cfg.ValidateAsset(c.Metadata)
		case "transfer":
			err = cfg.ValidateTransfer(c.Transfer)
		case "iso20022":
			err = cfg.ValidatePayment(c.Fields)
		default:
			t.Fatalf("%s: unknown target %q", c.Name, c.Target)
		}
		if c.OK {
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			continue
		}
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: error = %v, want a validation error", c.Name, err)
		}
		if ve.Field != c.Field {
			t.Fatalf("%s: field = %q, want %q (%v)", c.Name, ve.Field, c.Field, err)
		}
	}
	for kind, want := range doc.ProfileFields {
		p, ok := Get(Kind(kind))
		if !ok {
			t.Fatalf("missing profile %s", kind)
		}
		if strings.Join(p.Required, ",") != strings.Join(want.Required, ",") {
			t.Fatalf("%s required = %v, want %v", kind, p.Required, want.Required)
		}
		if p.RetentionDays != want.RetentionDays {
			t.Fatalf("%s retention = %d, want %d", kind, p.RetentionDays, want.RetentionDays)
		}
		if !strings.Contains(strings.ToLower(p.Disclaimer), doc.DisclaimerContains) {
			t.Fatalf("%s disclaimer lacks %q", kind, doc.DisclaimerContains)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty network: %v", err)
	}
	if _, err := New("arne:devnet", "gdpr"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown profile: %v", err)
	}
	if _, err := New("arne:devnet", MiCA, MiCA); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate profile: %v", err)
	}
	if _, err := New("arne:devnet"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty profile list: %v", err)
	}
	cfg, err := New("arne:devnet", FATF)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Threshold("se") != DefaultFATFThreshold {
		t.Fatalf("default threshold = %d", cfg.Threshold("se"))
	}
	cfg.JurisdictionThreshold = map[string]uint64{"se": 5}
	if cfg.Threshold("se") != 5 {
		t.Fatal("jurisdiction override ignored")
	}
	if len(cfg.Disclaimers()) != 1 || len(cfg.Selected()) != 1 {
		t.Fatal("selected profiles")
	}
	if len(All()) != 3 {
		t.Fatal("profile registry")
	}
}
