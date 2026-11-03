package connector

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

type vectorFile struct {
	Cases []struct {
		Name      string `json:"name"`
		Connector struct {
			Name     string   `json:"name"`
			Versions []uint32 `json:"versions"`
			Assets   []string `json:"assets"`
			Health   Status   `json:"health"`
		} `json:"connector"`
		Request Request `json:"request"`
		Expect  struct {
			Connector string   `json:"connector"`
			Version   uint32   `json:"version"`
			Assets    []string `json:"assets"`
			Network   string   `json:"network"`
			Error     string   `json:"error"`
		} `json:"expect"`
	} `json:"cases"`
}

type fakeAdapter struct {
	name     string
	versions []uint32
	assets   []string
	health   Health
}

func (a fakeAdapter) Name() string                  { return a.name }
func (a fakeAdapter) Versions() []uint32            { return a.versions }
func (a fakeAdapter) Assets() []string              { return a.assets }
func (a fakeAdapter) Health(context.Context) Health { return a.health }

func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/connector.json")
	if err != nil {
		t.Fatal(err)
	}
	var file vectorFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			adapter := fakeAdapter{
				name: c.Connector.Name, versions: c.Connector.Versions,
				assets: c.Connector.Assets, health: Health{Status: c.Connector.Health},
			}
			session, err := Negotiate(context.Background(), adapter, c.Request)
			if c.Expect.Error != "" {
				if err == nil || !strings.Contains(err.Error(), c.Expect.Error) {
					t.Fatalf("error = %v, want %q", err, c.Expect.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if session.Connector != c.Expect.Connector || session.Version != c.Expect.Version ||
				session.Network != c.Expect.Network || !reflect.DeepEqual(session.Assets, c.Expect.Assets) {
				t.Fatalf("session = %+v, want %+v", session, c.Expect)
			}
		})
	}
}

func TestNegotiateHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adapter := fakeAdapter{name: "x", versions: []uint32{1}, assets: []string{"a"}, health: Health{Status: Healthy}}
	if _, err := Negotiate(ctx, adapter, Request{Versions: []uint32{1}, Assets: []string{"a"}}); err == nil {
		t.Fatal("expected cancellation error")
	}
}
