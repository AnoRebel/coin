package evm

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type vectorFile struct {
	Config    Config `json:"config"`
	MessageID string `json:"message_id"`
	Cases     []struct {
		Name    string  `json:"name"`
		Kind    string  `json:"kind"`
		Now     int64   `json:"now"`
		Reason  string  `json:"reason"`
		Message Message `json:"message"`
		Expect  struct {
			Error string `json:"error"`
		} `json:"expect"`
	} `json:"cases"`
}

func load(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("../../../spec/vectors/evm_bridge.json")
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
	bridge, err := New(file.Config)
	if err != nil {
		t.Fatal(err)
	}
	if got := MessageID(1, "0x"+strings.Repeat("aa", 32), 0); got != file.MessageID {
		t.Fatalf("message id = %s, want %s", got, file.MessageID)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var err error
			switch c.Kind {
			case "pause":
				err = bridge.Pause(c.Reason)
			case "resume":
				err = bridge.Resume()
			case "submit":
				err = bridge.Submit(c.Message, c.Now)
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
		})
	}
}

func TestReplayStateSurvivesResume(t *testing.T) {
	file := load(t)
	bridge, err := New(file.Config)
	if err != nil {
		t.Fatal(err)
	}
	first := file.Cases[0].Message
	if err := bridge.Submit(first, 100); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Pause("maintenance"); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Submit(first, 101); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay after resume = %v, want ErrReplay", err)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	cases := map[string]Config{
		"no chain":      {Finality: 1},
		"zero finality": {ChainID: 1},
		"zero max": {ChainID: 1, Finality: 1, Limits: []Limit{
			{Asset: "arce:testnet/arce", Max: 0, Window: 60}}},
		"duplicate limit": {ChainID: 1, Finality: 1, Limits: []Limit{
			{Asset: "a", Max: 1, Window: 60}, {Asset: "a", Max: 1, Window: 60}}},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(config); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSubmitDoesNotChargeOnFailure(t *testing.T) {
	bridge, err := New(Config{ChainID: 1, Finality: 3, Limits: []Limit{
		{Asset: "a", Max: 100, Window: 60}}})
	if err != nil {
		t.Fatal(err)
	}
	msg := Message{
		Direction: Inbound, ChainID: 1, TxHash: "0x" + strings.Repeat("ab", 32),
		Asset: "a", Amount: 80, Sender: "s", Recipient: "r", Confirmations: 3,
	}
	if err := bridge.Submit(msg, 10); err != nil {
		t.Fatal(err)
	}
	msg.TxHash = "0x" + strings.Repeat("cd", 32)
	msg.Amount = 40
	msg.Confirmations = 2
	if err := bridge.Submit(msg, 11); !errors.Is(err, ErrFinality) {
		t.Fatalf("error = %v, want ErrFinality", err)
	}
	msg.Confirmations = 3
	msg.Amount = 20
	if err := bridge.Submit(msg, 12); err != nil {
		t.Fatalf("failed submit consumed the limit: %v", err)
	}
}
