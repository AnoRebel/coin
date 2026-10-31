package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/protocol"
	"github.com/anorebel/anorebelcoin/internal/snapshot"
	"github.com/anorebel/anorebelcoin/internal/storage"
)

func snapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Create, verify, or restore app-store snapshots",
		Long: "Snapshots contain blocks since a full base plus the state at a height.\n" +
			"Stop the node before creating or restoring one. A digest covers every\n" +
			"field, so a tampered snapshot is rejected before anything is written.",
	}
	cmd.AddCommand(snapshotCreateCmd(), snapshotVerifyCmd(), snapshotRestoreCmd())
	return cmd
}

func snapshotCreateCmd() *cobra.Command {
	var dataDir, out, network, chain, basePath string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a full or incremental snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dataDir == "" || out == "" {
				return fmt.Errorf("--data-dir and --out are required")
			}
			if chain == "" {
				var err error
				if chain, err = protocol.ChainID(network); err != nil {
					return err
				}
			}
			var base *snapshot.File
			if basePath != "" {
				var err error
				if base, err = snapshot.ReadFile(basePath); err != nil {
					return err
				}
			}
			store, err := storage.OpenPebble(dataDir)
			if err != nil {
				return err
			}
			defer store.Close()
			f, err := snapshot.Create(store, snapshot.Options{
				Network: network, Chain: chain, Base: base,
			})
			if err != nil {
				return err
			}
			if err := snapshot.WriteFile(out, f); err != nil {
				return err
			}
			fmt.Printf("snapshot: %s height %d, %d block(s), root %s\n",
				f.Kind, f.Height, len(f.Blocks), f.StateRoot)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Pebble data directory")
	cmd.Flags().StringVar(&out, "out", "", "snapshot output file")
	cmd.Flags().StringVar(&network, "network", "arne:devnet", "network id")
	cmd.Flags().StringVar(&chain, "chain", "", "chain id (default derived from --network)")
	cmd.Flags().StringVar(&basePath, "base", "", "full snapshot to extend (creates an incremental)")
	return cmd
}

func snapshotVerifyCmd() *cobra.Command {
	var in, basePath string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a snapshot and its block/state chain",
		RunE: func(cmd *cobra.Command, args []string) error {
			if in == "" {
				return fmt.Errorf("--in is required")
			}
			f, err := snapshot.ReadFile(in)
			if err != nil {
				return err
			}
			var base *snapshot.File
			if basePath != "" {
				if base, err = snapshot.ReadFile(basePath); err != nil {
					return err
				}
			}
			if err := snapshot.Verify(f, base); err != nil {
				return err
			}
			fmt.Printf("snapshot: valid %s, height %d, %d block(s), root %s\n",
				f.Kind, f.Height, len(f.Blocks), f.StateRoot)
			return nil
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "snapshot file")
	cmd.Flags().StringVar(&basePath, "base", "", "full base snapshot for an incremental")
	return cmd
}

func snapshotRestoreCmd() *cobra.Command {
	var in, basePath, dataDir string
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a snapshot into a new data directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			if in == "" || dataDir == "" {
				return fmt.Errorf("--in and --data-dir are required")
			}
			f, err := snapshot.ReadFile(in)
			if err != nil {
				return err
			}
			var base *snapshot.File
			if basePath != "" {
				if base, err = snapshot.ReadFile(basePath); err != nil {
					return err
				}
			}
			if err := snapshot.Restore(f, base, dataDir); err != nil {
				return err
			}
			fmt.Printf("snapshot: restored height %d to %s\n", f.Height, dataDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&in, "in", "", "snapshot file")
	cmd.Flags().StringVar(&basePath, "base", "", "full base snapshot for an incremental")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "new Pebble data directory")
	return cmd
}
