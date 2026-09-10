package main

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/podium-ade/podium/internal/server"
	"github.com/podium-ade/podium/internal/server/secrets"
	"github.com/podium-ade/podium/internal/server/store"
)

func newGenMasterKeyCommand() *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:   "gen-master-key",
		Short: "Print a new 32-byte master key for PODIUM_MASTER_KEY_FILE",
		Long: "Print a new 32-byte master key, hex encoded, for PODIUM_MASTER_KEY_FILE.\n\n" +
			"With --out the key is written straight to a new file with mode 0600, which is\n" +
			"what the server requires; without it the key goes to stdout and creating the\n" +
			"file with the right mode is yours to do:\n\n" +
			"  (umask 077; podium-server gen-master-key > /etc/podium/master.key)\n\n" +
			"There is no recovery path. A lost master key is every secret in the database\n" +
			"lost with it: back the file up somewhere that is not this machine, and rotate\n" +
			"with `podium-server rotate-master-key` rather than replacing the file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, err := secrets.GenerateKey()
			if err != nil {
				return err
			}
			if out == "" {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), key.Hex())
				return err
			}
			if err := secrets.WriteKeyFile(out, key); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s (mode 0600, key %s)\n", out, key.ID())
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write the key to this new file with mode 0600 instead of stdout")
	return cmd
}

func newRotateMasterKeyCommand() *cobra.Command {
	var oldPath, newPath string

	cmd := &cobra.Command{
		Use:   "rotate-master-key --old FILE --new FILE",
		Short: "Re-encrypt every stored secret under a new master key",
		Long: "Re-encrypt every stored secret under a new master key.\n\n" +
			"Every row is re-encrypted in a single transaction, so the table is never left\n" +
			"half under one key and half under the other. Both files must be readable only\n" +
			"by this account. PODIUM_DATABASE_URL names the database.\n\n" +
			"Order of operations: generate the new key, run this, then point\n" +
			"PODIUM_MASTER_KEY_FILE at the new file and restart the server. The old key can\n" +
			"decrypt nothing afterwards.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if oldPath == "" || newPath == "" {
				return errors.New("both --old and --new are required")
			}
			oldKey, err := secrets.LoadKeyFile(oldPath)
			if err != nil {
				return err
			}
			newKey, err := secrets.LoadKeyFile(newPath)
			if err != nil {
				return err
			}

			cfg := server.ConfigFromEnv()
			if cfg.DatabaseURL == "" {
				return errors.New("PODIUM_DATABASE_URL is required")
			}
			ctx := cmd.Context()
			st, err := store.New(ctx, cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.Migrate(ctx); err != nil {
				return err
			}

			rotated, err := secrets.Rotate(ctx, st, oldKey, newKey)
			if err != nil {
				return err
			}
			if err := st.Audit(ctx, "rotate-master-key", store.ActionSecretRotate, newKey.ID(), map[string]any{
				"rotated": rotated, "from_key_id": oldKey.ID(), "to_key_id": newKey.ID(),
			}); err != nil {
				slog.WarnContext(ctx, "audit write failed", "action", store.ActionSecretRotate, "error", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "rotated %d secret(s) and registry credential(s) from key %s to key %s\n",
				rotated, oldKey.ID(), newKey.ID())
			fmt.Fprintf(cmd.ErrOrStderr(),
				"now set PODIUM_MASTER_KEY_FILE=%s and restart podium-server; the old key decrypts nothing\n",
				newPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&oldPath, "old", "", "the master key file the secrets are currently encrypted under")
	cmd.Flags().StringVar(&newPath, "new", "", "the master key file to re-encrypt them under")
	return cmd
}
