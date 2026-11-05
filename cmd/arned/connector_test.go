package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/anorebel/anorebelcoin/internal/connector"
)

func handshakeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "handshake.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNegotiateConnector(t *testing.T) {
	session, err := negotiateConnector(handshakeFile(t, `{
		"adapter": {"name": "evm", "versions": [1, 2], "assets": ["arce:devnet", "usd:devnet"], "health": "degraded"},
		"request": {"versions": [1, 2], "assets": ["usd:devnet"], "network": "arne:devnet"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if session.Connector != "evm" || session.Version != 2 || len(session.Assets) != 1 ||
		session.Assets[0] != "usd:devnet" || session.Network != "arne:devnet" {
		t.Fatalf("session: %+v", session)
	}
}

func TestNegotiateConnectorRejects(t *testing.T) {
	cases := map[string]struct {
		body string
		want error
	}{
		"unhealthy": {`{"adapter":{"name":"evm","versions":[1],"assets":["arce:devnet"],"health":"unhealthy"},"request":{"versions":[1],"assets":["arce:devnet"]}}`, connector.ErrUnhealthy},
		"version":   {`{"adapter":{"name":"evm","versions":[1],"assets":["arce:devnet"]},"request":{"versions":[2],"assets":["arce:devnet"]}}`, connector.ErrVersion},
		"asset":     {`{"adapter":{"name":"evm","versions":[1],"assets":["arce:devnet"]},"request":{"versions":[1],"assets":["usd:devnet"]}}`, connector.ErrAsset},
		"duplicate": {`{"adapter":{"name":"evm","versions":[1,1],"assets":["arce:devnet"]},"request":{"versions":[1],"assets":["arce:devnet"]}}`, connector.ErrDuplicate},
	}
	for name, test := range cases {
		if _, err := negotiateConnector(handshakeFile(t, test.body)); !errors.Is(err, test.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
