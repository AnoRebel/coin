package governance

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

type emergencyVectors struct {
	Members   []string        `json:"members"`
	Threshold int             `json:"threshold"`
	Period    uint64          `json:"period"`
	Steps     []emergencyStep `json:"steps"`
}

type emergencyStep struct {
	Op         string          `json:"op"`
	Signers    []string        `json:"signers"`
	Operations []string        `json:"operations"`
	Proposal   string          `json:"proposal"`
	Reason     string          `json:"reason"`
	TargetKind string          `json:"target_kind"`
	At         uint64          `json:"at"`
	Error      string          `json:"error"`
	Expect     emergencyExpect `json:"expect"`
}

type emergencyExpect struct {
	Kind      string            `json:"kind"`
	Status    string            `json:"status"`
	ExpiresAt uint64            `json:"expires_at"`
	Signers   []string          `json:"signers"`
	Paused    string            `json:"paused"`
	NotPaused string            `json:"not_paused"`
	Vetoed    string            `json:"vetoed"`
	NotVetoed string            `json:"not_vetoed"`
	Statuses  map[string]string `json:"statuses"`
	Audit     []string          `json:"audit"`
}

func TestEmergencyVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/emergency.json")
	if err != nil {
		t.Fatal(err)
	}
	var file emergencyVectors
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	var events []EmergencyEvent
	committee, err := NewEmergency(file.Members, file.Threshold, file.Period)
	if err != nil {
		t.Fatal(err)
	}
	committee.Audit = func(e EmergencyEvent) { events = append(events, e) }
	for _, step := range file.Steps {
		events = nil
		var action EmergencyAction
		var stepErr error
		switch step.Op {
		case "pause":
			action, stepErr = committee.Pause(step.Signers, step.Operations, step.Reason, step.At)
		case "veto":
			action, stepErr = committee.Veto(step.Signers, step.Proposal, step.Reason, step.At)
		case "advance":
			committee.Advance(step.At)
		case "revoke":
			target := lastAction(committee, step.TargetKind)
			if target == nil {
				t.Fatalf("no %s action to revoke", step.TargetKind)
			}
			stepErr = committee.Revoke(target.ID, step.Signers, step.Reason, step.At)
		case "ratify":
			target := lastAction(committee, step.TargetKind)
			if target == nil {
				t.Fatalf("no %s action to ratify", step.TargetKind)
			}
			stepErr = committee.Ratify(target.ID, step.Proposal, step.At)
		default:
			t.Fatalf("unknown op %q", step.Op)
		}
		if step.Error != "" && codeOf(stepErr) != step.Error {
			t.Fatalf("%s %+v: want %s, got %v", step.Op, step, step.Error, stepErr)
		}
		if step.Error == "" && stepErr != nil {
			t.Fatalf("%s %+v: %v", step.Op, step, stepErr)
		}
		if step.Error != "" {
			continue
		}
		if step.Expect.Kind != "" && action.Kind != step.Expect.Kind {
			t.Fatalf("kind: want %s, got %s", step.Expect.Kind, action.Kind)
		}
		if step.Expect.Status != "" && action.Status != step.Expect.Status {
			t.Fatalf("status: want %s, got %s", step.Expect.Status, action.Status)
		}
		if step.Expect.ExpiresAt != 0 && action.ExpiresAt != step.Expect.ExpiresAt {
			t.Fatalf("expires: want %d, got %d", step.Expect.ExpiresAt, action.ExpiresAt)
		}
		if step.Expect.Signers != nil && !reflect.DeepEqual(action.Signers, step.Expect.Signers) {
			t.Fatalf("signers: want %v, got %v", step.Expect.Signers, action.Signers)
		}
		for kind, status := range step.Expect.Statuses {
			target := lastAction(committee, kind)
			if target == nil || target.Status != status {
				t.Fatalf("%s status: want %s, got %+v", kind, status, target)
			}
		}
		if step.Expect.Paused != "" && !committee.Paused(step.Expect.Paused, step.At) {
			t.Fatalf("%s not paused", step.Expect.Paused)
		}
		if step.Expect.NotPaused != "" && committee.Paused(step.Expect.NotPaused, step.At) {
			t.Fatalf("%s paused", step.Expect.NotPaused)
		}
		if step.Expect.Vetoed != "" && !committee.Vetoed(step.Expect.Vetoed, step.At) {
			t.Fatalf("%s not vetoed", step.Expect.Vetoed)
		}
		if step.Expect.NotVetoed != "" && committee.Vetoed(step.Expect.NotVetoed, step.At) {
			t.Fatalf("%s vetoed", step.Expect.NotVetoed)
		}
		got := make([]string, 0, len(events))
		for _, event := range events {
			got = append(got, event.Action)
		}
		sort.Strings(got)
		want := append([]string(nil), step.Expect.Audit...)
		sort.Strings(want)
		if len(want) != 0 || len(got) != 0 {
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("audit: want %v, got %v", want, got)
			}
		}
	}
}

func lastAction(e *Emergency, kind string) *EmergencyAction {
	for i := len(e.Actions) - 1; i >= 0; i-- {
		if e.Actions[i].Kind == kind {
			return &e.Actions[i]
		}
	}
	return nil
}
