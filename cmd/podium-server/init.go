package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alvaroibarguen/podium/internal/server"
	"github.com/alvaroibarguen/podium/internal/server/secrets"
)

// initSecrets are the values `init` mints. They are separated from the rendering so the
// rendering is a pure function and can be tested without touching the filesystem.
type initSecrets struct {
	PGPassword  string
	DevToken    string
	S3SecretKey string
	AgentToken  string
	MasterKey   string // the path the key was written to
	Transport   string
	Tailnet     string
}

func newInitCommand() *cobra.Command {
	var dir, transport, tailnet string
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a master key and a filled .env for deploy/docker-compose.yml",
		Long: "Generate a master key and a filled .env for deploy/docker-compose.yml.\n\n" +
			"Run it in the directory holding the compose file — the deploy/ directory you\n" +
			"copied onto the host. It writes two files and never overwrites either:\n\n" +
			"  master.key   the AES-256 key every stored secret is encrypted under, mode 0600\n" +
			"  .env         the compose file's variables, with fresh random credentials\n\n" +
			"With --transport tailnet the .env also carries the two values only you can\n" +
			"supply — TS_AUTHKEY and PODIUM_TAILNET — and the command reports whether the\n" +
			"Tailscale prerequisites are in place.\n\n" +
			"Back master.key up somewhere that is not this machine. There is no recovery\n" +
			"path: losing it loses every secret encrypted under it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch transport {
			case server.TransportDev, server.TransportTailnet:
			default:
				return fmt.Errorf("--transport %q is not a transport (want %s or %s)",
					transport, server.TransportDev, server.TransportTailnet)
			}

			keyPath := filepath.Join(dir, "master.key")
			envPath := filepath.Join(dir, ".env")

			// Both files are refused rather than overwritten. An existing master.key is a
			// live credential, and an existing .env is a running deployment's password.
			if _, err := os.Stat(envPath); err == nil && !force {
				return fmt.Errorf("%s already exists: this deployment is already initialised "+
					"(pass --force to write a second .env, but read it first)", envPath)
			}

			key, err := secrets.GenerateKey()
			if err != nil {
				return err
			}
			if err := secrets.WriteKeyFile(keyPath, key); err != nil {
				return fmt.Errorf("%w (an existing master.key is never overwritten; "+
					"use `podium-server rotate-master-key` to replace one)", err)
			}

			values := initSecrets{Transport: transport, Tailnet: tailnet, MasterKey: keyPath}
			for _, gen := range []struct {
				dst   *string
				bytes int
			}{
				{&values.PGPassword, 24},
				{&values.DevToken, 32},
				{&values.S3SecretKey, 24},
				{&values.AgentToken, 32},
			} {
				if *gen.dst, err = randomSecret(gen.bytes); err != nil {
					return err
				}
			}

			if err := writeEnvFile(envPath, renderEnv(values)); err != nil {
				return err
			}

			out := cmd.ErrOrStderr()
			fmt.Fprintf(out, "wrote %s (mode 0600, key %s)\n", keyPath, key.ID())
			fmt.Fprintf(out, "wrote %s (mode 0600)\n", envPath)
			fmt.Fprintf(out, "\nBack up %s. Losing it loses every secret encrypted under it.\n", keyPath)
			reportTailscalePrereqs(out, values)
			fmt.Fprintf(out, "\nNext: docker compose -f docker-compose%s.yml up -d --wait\n",
				map[string]string{server.TransportDev: "", server.TransportTailnet: ".tailnet"}[transport])
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory to write master.key and .env into")
	cmd.Flags().StringVar(&transport, "transport", server.TransportDev,
		"which deployment this is: dev (loopback) or tailnet")
	cmd.Flags().StringVar(&tailnet, "tailnet", "",
		"your tailnet's MagicDNS suffix without .ts.net (for example taila79bf6); tailnet transport only")
	cmd.Flags().BoolVar(&force, "force", false, "write .env even if one already exists")
	return cmd
}

// randomSecret returns n bytes of crypto/rand as URL-safe base64. It is used for values
// that live in a .env file, so the alphabet has to survive shell and compose quoting.
func randomSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// writeEnvFile creates the file with O_EXCL semantics relaxed only by the caller's --force
// check, and mode 0600: it holds the database password and the API token.
func writeEnvFile(path, body string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := io.WriteString(f, body); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}

// renderEnv builds the .env body. Every variable the compose files require has a value;
// everything optional is left to deploy/.env.example, which documents the whole set.
func renderEnv(v initSecrets) string {
	var b strings.Builder
	b.WriteString("# Generated by `podium-server init`. Mode 0600 — it holds live credentials.\n")
	b.WriteString("# Every PODIUM_* variable Podium reads is documented in .env.example.\n\n")

	b.WriteString("# Postgres. The compose file builds PODIUM_DATABASE_URL from this.\n")
	b.WriteString("PODIUM_PG_PASSWORD=" + v.PGPassword + "\n\n")

	b.WriteString("# The object store artifacts and rolled-up logs live in.\n")
	b.WriteString("PODIUM_S3_ACCESS_KEY=podium\n")
	b.WriteString("PODIUM_S3_SECRET_KEY=" + v.S3SecretKey + "\n")
	b.WriteString("PODIUM_S3_BUCKET=podium\n\n")

	if v.Transport == server.TransportTailnet {
		b.WriteString("# Your tailnet's MagicDNS suffix, without .ts.net. `tailscale status --json`\n")
		b.WriteString("# reports it as MagicDNSSuffix; the server is served at\n")
		b.WriteString("# https://podium.<this>.ts.net.\n")
		b.WriteString("PODIUM_TAILNET=" + v.Tailnet + "\n\n")
		b.WriteString("# Tailscale auth keys: REUSABLE and PRE-APPROVED, one tagged tag:podium-server\n")
		b.WriteString("# and one tagged tag:podium-node. These are Tailscale's keys, not Podium's\n")
		b.WriteString("# enrollment token. Read on first run only; the state volumes are the identity\n")
		b.WriteString("# afterwards.\n")
		b.WriteString("TS_AUTHKEY=\n")
		b.WriteString("PODIUM_NODE_TS_AUTHKEY=\n\n")
	} else {
		b.WriteString("# The dev transport's shared bearer token. Every API call and the web UI\n")
		b.WriteString("# present it; it is the only thing between a caller and the whole API.\n")
		b.WriteString("PODIUM_DEV_TOKEN=" + v.DevToken + "\n\n")
	}

	b.WriteString("# The conductor's API token. podium-server presents it on every proxied call and\n")
	b.WriteString("# the conductor accepts nothing else, so the two must agree — which is why it is\n")
	b.WriteString("# minted here rather than left to be filled in. Every compose file requires it.\n")
	b.WriteString("PODIUM_AGENT_TOKEN=" + v.AgentToken + "\n\n")

	b.WriteString("# Podium's own single-use enrollment token, from `podium node enroll-token`.\n")
	b.WriteString("# Needed on a worker's first run only. Not the same thing as TS_AUTHKEY.\n")
	b.WriteString("PODIUM_NODE_ENROLL_TOKEN=\n")
	return b.String()
}

// reportTailscalePrereqs says what is still missing before a tailnet deployment can start.
// It checks what a process can check — an environment variable — and names the two console
// settings that cannot be checked from here at all.
func reportTailscalePrereqs(w io.Writer, v initSecrets) {
	if v.Transport != server.TransportTailnet {
		return
	}
	fmt.Fprintf(w, "\nTailscale prerequisites:\n")

	if v.Tailnet == "" {
		fmt.Fprintf(w, "  [ ] PODIUM_TAILNET is empty in .env. Fill it with your MagicDNS suffix\n"+
			"      (`tailscale status --json | grep MagicDNSSuffix`), without .ts.net.\n")
	} else {
		fmt.Fprintf(w, "  [x] PODIUM_TAILNET=%s — the server will be https://podium.%s.ts.net\n", v.Tailnet, v.Tailnet)
	}

	if os.Getenv("TS_AUTHKEY") == "" {
		fmt.Fprintf(w, "  [ ] TS_AUTHKEY is empty in .env. Generate two auth keys in the Tailscale\n"+
			"      admin console (Settings -> Keys), both Reusable and Pre-approved: one tagged\n"+
			"      tag:podium-server for TS_AUTHKEY, one tagged tag:podium-node for\n"+
			"      PODIUM_NODE_TS_AUTHKEY.\n")
	} else {
		fmt.Fprintf(w, "  [x] TS_AUTHKEY is set in this shell — copy it into .env, it is not read from here\n")
	}

	fmt.Fprintf(w, "  [?] MagicDNS and HTTPS Certificates must both be ON for your tailnet\n"+
		"      (admin console -> DNS). Podium cannot check this from here; without HTTPS\n"+
		"      the server cannot get a certificate and refuses to start.\n"+
		"      https://tailscale.com/kb/1153/enabling-https\n")
	fmt.Fprintf(w, "  [?] Apply deploy/tailscale-acl.example.json to your Access Controls, merged\n"+
		"      with whatever policy you already have.\n")
}
