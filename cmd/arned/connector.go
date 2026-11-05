package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/connector"
)

// staticAdapter is the capability set a connector describes in a handshake
// file. It exists so operators can validate onboarding data before wiring a
// live adapter.
type staticAdapter struct {
	AdapterName      string           `json:"name"`
	ProtocolVersions []uint32         `json:"versions"`
	SupportedAssets  []string         `json:"assets"`
	Status           connector.Status `json:"health"`
	Detail           string           `json:"detail,omitempty"`
}

func (a staticAdapter) Name() string       { return a.AdapterName }
func (a staticAdapter) Versions() []uint32 { return a.ProtocolVersions }
func (a staticAdapter) Assets() []string   { return a.SupportedAssets }
func (a staticAdapter) Health(context.Context) connector.Health {
	return connector.Health{Status: a.Status, Detail: a.Detail}
}

func negotiateConnector(path string) (connector.Session, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return connector.Session{}, err
	}
	var input struct {
		Adapter staticAdapter     `json:"adapter"`
		Request connector.Request `json:"request"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return connector.Session{}, fmt.Errorf("handshake file: %w", err)
	}
	if input.Adapter.Status == "" {
		input.Adapter.Status = connector.Healthy
	}
	return connector.Negotiate(context.Background(), input.Adapter, input.Request)
}

func connectorCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "connector", Short: "Validate interoperability connector handshakes"}
	var file string
	negotiate := &cobra.Command{
		Use:   "negotiate",
		Short: "Negotiate versions and assets from a handshake JSON file",
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := negotiateConnector(file)
			if err != nil {
				return err
			}
			return printJSON(session)
		},
	}
	negotiate.Flags().StringVar(&file, "file", "", "handshake JSON file")
	_ = negotiate.MarkFlagRequired("file")
	cmd.AddCommand(negotiate)
	return cmd
}
