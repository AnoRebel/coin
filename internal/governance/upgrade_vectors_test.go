package governance

import (
	"encoding/json"
	"os"
	"testing"
)

type upgradeVectors struct {
	Cases []upgradeCase `json:"cases"`
}

type upgradeCase struct {
	Name    string  `json:"name"`
	Action  Action  `json:"action"`
	Running string  `json:"running"`
	Error   string  `json:"error"`
	Expect  Upgrade `json:"expect"`
}

func TestUpgradeVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/upgrade.json")
	if err != nil {
		t.Fatal(err)
	}
	var file upgradeVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, test := range file.Cases {
		t.Run(test.Name, func(t *testing.T) {
			plan, err := NewUpgrade(test.Action)
			if test.Error == "hash" {
				if err != nil {
					t.Fatal(err)
				}
				if codeOf(Verify(plan, test.Running)) != test.Error {
					t.Fatalf("want %s", test.Error)
				}
				return
			}
			if test.Error != "" {
				if codeOf(err) != test.Error {
					t.Fatalf("want %s, got %v", test.Error, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := Verify(plan, test.Running); err != nil {
				t.Fatal(err)
			}
			for name, pair := range map[string][2]string{
				"target":        {test.Expect.Target, plan.Target},
				"hash":          {test.Expect.Hash, plan.Hash},
				"previous_hash": {test.Expect.Previous, plan.Previous},
				"version":       {test.Expect.Version, plan.Version},
			} {
				if pair[0] != "" && pair[0] != pair[1] {
					t.Fatalf("%s: want %q, got %q", name, pair[0], pair[1])
				}
			}
		})
	}
}
