package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/storage"
)

func migrateCmd() *cobra.Command {
	var dataDir string
	var target uint32
	var statusOnly bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Inspect or migrate the on-disk schema",
		Long: "Migrate the Pebble store one version at a time. Each step commits its data\n" +
			"change and version atomically; rerunning is a no-op. Use --to 0 to roll back\n" +
			"the baseline marker. Take an encrypted backup before rolling back a\n" +
			"destructive migration.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dataDir == "" {
				return errors.New("--data-dir is required")
			}
			store, err := storage.OpenPebble(dataDir)
			if err != nil {
				return err
			}
			defer store.Close()
			current, err := store.SchemaVersion()
			if err != nil {
				return err
			}
			if statusOnly {
				fmt.Printf("schema: %d (binary supports %d)\n", current, storage.SchemaVersion)
				return nil
			}
			if err := store.Migrate(target); err != nil {
				return err
			}
			now, err := store.SchemaVersion()
			if err != nil {
				return err
			}
			fmt.Printf("schema: %d -> %d\n", current, now)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Pebble data directory")
	cmd.Flags().Uint32Var(&target, "to", storage.SchemaVersion, "target schema version")
	cmd.Flags().BoolVar(&statusOnly, "status", false, "print the current schema without changing it")
	return cmd
}
