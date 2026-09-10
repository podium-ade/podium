// Package cli is the podium command-line client. It talks to the control plane over
// Connect and never touches Docker: everything it knows comes from the server.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// DefaultServer is what the CLI dials when nothing says otherwise.
const DefaultServer = "http://127.0.0.1:8080"

// Config is ~/.config/podium/config.yaml. Environment variables override it and flags
// override them, so a scripted invocation never depends on what is on the developer's
// disk.
type Config struct {
	Server string `yaml:"server"`
	// SENSITIVE: never printed. Under the dev transport this is PODIUM_DEV_TOKEN.
	Token string `yaml:"token"`
}

// ConfigPath is the file the CLI reads, honouring XDG_CONFIG_HOME.
func ConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "podium", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "podium", "config.yaml")
}

// LoadConfig resolves the effective configuration: the file, then PODIUM_SERVER and
// PODIUM_TOKEN (or PODIUM_DEV_TOKEN, which is the same secret under the server's name for
// it), then the --server and --token flags. A missing file is not an error.
func LoadConfig(serverFlag, tokenFlag string) (Config, error) {
	cfg := Config{Server: DefaultServer}

	if path := ConfigPath(); path != "" {
		raw, err := os.ReadFile(path) //nolint:gosec // the user names their own config file
		switch {
		case err == nil:
			var fromFile Config
			if err := yaml.Unmarshal(raw, &fromFile); err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", path, err)
			}
			if fromFile.Server != "" {
				cfg.Server = fromFile.Server
			}
			if fromFile.Token != "" {
				cfg.Token = fromFile.Token
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			return Config{}, fmt.Errorf("read %s: %w", path, err)
		}
	}

	if v := os.Getenv("PODIUM_SERVER"); v != "" {
		cfg.Server = v
	}
	if v := os.Getenv("PODIUM_TOKEN"); v != "" {
		cfg.Token = v
	} else if v := os.Getenv("PODIUM_DEV_TOKEN"); v != "" {
		// The dev transport has exactly one bearer, and PODIUM_DEV_TOKEN is the name the
		// server reads it under. A shell that sourced a stack's .env is therefore already
		// holding the CLI's token, and asking for the same secret again under a second
		// name only creates a pair that can drift apart.
		cfg.Token = v
	}
	if serverFlag != "" {
		cfg.Server = serverFlag
	}
	if tokenFlag != "" {
		cfg.Token = tokenFlag
	}

	if cfg.Server == "" {
		return Config{}, errors.New("no server: pass --server, set PODIUM_SERVER, or put `server:` in " + ConfigPath())
	}
	// A token is a dev-transport artefact. Over the tailnet the server serves HTTPS on its
	// MagicDNS name and Tailscale's WhoIs names the caller, so there is nothing to present and
	// asking for one would be wrong.
	if cfg.Token == "" && !cfg.Tailnet() {
		return Config{}, errors.New("no token: pass --token, set PODIUM_TOKEN or PODIUM_DEV_TOKEN, " +
			"or put `token:` in " + ConfigPath() +
			" (a tailnet control plane needs none: use its https:// MagicDNS URL)")
	}
	return cfg, nil
}

// Tailnet reports whether the configured server is a tailnet control plane, which is the only
// case in which the CLI needs no credential of its own.
func (c Config) Tailnet() bool { return strings.HasPrefix(c.Server, "https://") }
