package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func mustParse(t *testing.T, body string) *PolicySet {
	t.Helper()
	ps, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

const base = `{
  "network": "arne:devnet",
  "versions": [
    {"version": 1, "activation_height": 0, "default": {"decision": "deny", "reason": "default"}, "rules": [
      {"id": "b", "priority": 10, "decision": "allow", "reason": "b", "match": {}},
      {"id": "a", "priority": 10, "decision": "hold", "reason": "a", "match": {}}
    ]},
    {"version": 2, "activation_height": 10, "default": {"decision": "allow", "reason": "v2"}, "rules": []}
  ]
}`

func TestEvaluateUsesActiveVersionAndPriorityTieBreak(t *testing.T) {
	ps := mustParse(t, base)
	got := ps.Evaluate(0, Context{Asset: "arce:devnet", Amount: 1})
	want := Outcome{Decision: Hold, RuleID: "a", PolicyVersion: 1, ActivationHeight: 0,
		Reasons: []string{"a"}, Requirements: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("height 0 = %#v", got)
	}
	got = ps.Evaluate(9, Context{Asset: "arce:devnet", Amount: 1})
	if got.PolicyVersion != 1 || got.RuleID != "a" {
		t.Fatalf("height 9 = %#v", got)
	}
	got = ps.Evaluate(10, Context{Asset: "arce:devnet", Amount: 1})
	want = Outcome{Decision: Allow, RuleID: "default", PolicyVersion: 2, ActivationHeight: 10,
		Reasons: []string{"v2"}, Requirements: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("height 10 = %#v", got)
	}
}

func TestEvaluateBeforeFirstActivationDenies(t *testing.T) {
	ps := mustParse(t, `{"network":"n","versions":[{"version":1,"activation_height":5,"default":{"decision":"allow","reason":"x"},"rules":[]}]}`)
	got := ps.Evaluate(4, Context{})
	if got.Decision != Deny || got.RuleID != "no-active-policy" || got.PolicyVersion != 0 {
		t.Fatalf("outcome = %#v", got)
	}
}

func TestRuleMatchingThresholdsAndLists(t *testing.T) {
	ps := mustParse(t, `{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"allow","reason":"x"},"rules":[
      {"id":"t","priority":1,"decision":"hold","reason":"t","match":{"assets":["usd:devnet"],"originator_jurisdictions":["US"],"transfer_types":["transfer"],"min_amount":100,"max_amount":200}}
    ]}]}`)
	cases := []struct {
		ctx  Context
		want Decision
		id   string
	}{
		{Context{Asset: "usd:devnet", Amount: 100, OriginatorJurisdiction: "US", TransferType: "transfer"}, Hold, "t"},
		{Context{Asset: "usd:devnet", Amount: 200, OriginatorJurisdiction: "US", TransferType: "transfer"}, Hold, "t"},
		{Context{Asset: "usd:devnet", Amount: 99, OriginatorJurisdiction: "US", TransferType: "transfer"}, Allow, "default"},
		{Context{Asset: "usd:devnet", Amount: 201, OriginatorJurisdiction: "US", TransferType: "transfer"}, Allow, "default"},
		{Context{Asset: "usd:devnet", Amount: 100, OriginatorJurisdiction: "CA", TransferType: "transfer"}, Allow, "default"},
	}
	for _, c := range cases {
		got := ps.Evaluate(0, c.ctx)
		if got.Decision != c.want || got.RuleID != c.id {
			t.Fatalf("%+v = %s/%s", c.ctx, got.Decision, got.RuleID)
		}
	}
}

func TestParseRejectsInvalidPolicies(t *testing.T) {
	bad := []string{
		`{"network":"","versions":[]}`,
		`{"network":"n","versions":[]}`,
		`{"network":"n","versions":[{"version":0,"activation_height":0,"default":{"decision":"allow"},"rules":[]}]}`,
		`{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"maybe"},"rules":[]}]}`,
		`{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"allow"},"rules":[]},{"version":1,"activation_height":9,"default":{"decision":"allow"},"rules":[]}]}`,
		`{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"allow"},"rules":[]},{"version":2,"activation_height":0,"default":{"decision":"allow"},"rules":[]}]}`,
		`{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"allow"},"rules":[{"id":"x","decision":"allow","match":{}},{"id":"x","decision":"deny","match":{}}]}]}`,
		`{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"allow"},"rules":[{"id":"x","decision":"allow","match":{"min_amount":10,"max_amount":1}}]}]}`,
		`{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"allow"},"rules":[{"id":"","decision":"allow","match":{}}]}]}`,
		`{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"allow"},"rules":[]}],"extra":true}`,
		`{"network":"n","versions":[{"version":1,"activation_height":0,"default":{"decision":"allow"},"rules":[]}]} {}`,
	}
	for i, body := range bad {
		if _, err := Parse([]byte(body)); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

func TestHistoryIsOrdered(t *testing.T) {
	ps := mustParse(t, `{"network":"n","versions":[
      {"version": 2, "activation_height": 20, "default":{"decision":"allow"},"rules":[]},
      {"version": 1, "activation_height": 10, "default":{"decision":"allow"},"rules":[]}
    ]}`)
	h := ps.History()
	if len(h) != 2 || h[0].ActivationHeight != 10 || h[1].ActivationHeight != 20 {
		t.Fatalf("history = %+v", h)
	}
}

func TestPolicyVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "vectors", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Sets []struct {
			Policy json.RawMessage `json:"policy"`
			Cases  []struct {
				Height   uint64  `json:"height"`
				Context  Context `json:"context"`
				Expected Outcome `json:"expected"`
			} `json:"cases"`
		} `json:"sets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for si, set := range doc.Sets {
		ps, err := Parse(set.Policy)
		if err != nil {
			t.Fatalf("set %d: %v", si, err)
		}
		for ci, c := range set.Cases {
			got := ps.Evaluate(c.Height, c.Context)
			if !reflect.DeepEqual(got, c.Expected) {
				t.Fatalf("set %d case %d: got %#v want %#v", si, ci, got, c.Expected)
			}
		}
	}
}
