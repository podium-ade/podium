package server

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/server/secrets"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func keyFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	key, err := secrets.GenerateKey()
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, os.WriteFile(path, []byte(key.Hex()+"\n"), 0o600))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

// The acceptance item: a world-readable master key file stops the server dead. The check
// runs before anything else in New, so it does not even need a database to prove it.
func TestServerRefusesToStartWithAWorldReadableKeyFile(t *testing.T) {
	cfg := Config{
		DatabaseURL:   "postgres://nobody@127.0.0.1:1/none",
		Transport:     TransportDev,
		DevListen:     "127.0.0.1:0",
		DevToken:      "devtoken",
		MasterKeyFile: keyFile(t, 0o644),
	}
	_, err := New(context.Background(), cfg, quietLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "readable by other accounts")
	assert.Contains(t, err.Error(), "chmod 0600")
}

func TestServerRefusesAMissingOrMalformedKeyFile(t *testing.T) {
	base := Config{
		DatabaseURL: "postgres://nobody@127.0.0.1:1/none",
		Transport:   TransportDev,
		DevListen:   "127.0.0.1:0",
		DevToken:    "devtoken",
	}

	missing := base
	missing.MasterKeyFile = filepath.Join(t.TempDir(), "absent")
	_, err := New(context.Background(), missing, quietLogger())
	require.ErrorContains(t, err, "master key file")

	short := base
	short.MasterKeyFile = filepath.Join(t.TempDir(), "short.key")
	require.NoError(t, os.WriteFile(short.MasterKeyFile, []byte("nope"), 0o600))
	_, err = New(context.Background(), short, quietLogger())
	require.ErrorContains(t, err, "must be 64 hex characters or 32 raw bytes")
}

func TestLoadMasterKeyPrefersTheFileAndAcceptsTheEnvVar(t *testing.T) {
	path := keyFile(t, 0o600)
	fromFile, err := secrets.LoadKeyFile(path)
	require.NoError(t, err)

	inline, err := secrets.GenerateKey()
	require.NoError(t, err)

	got, err := loadMasterKey(Config{MasterKeyFile: path, MasterKey: inline.Hex()}, quietLogger())
	require.NoError(t, err)
	assert.True(t, got.Equal(fromFile), "PODIUM_MASTER_KEY_FILE wins over PODIUM_MASTER_KEY")

	got, err = loadMasterKey(Config{MasterKey: inline.Hex()}, quietLogger())
	require.NoError(t, err)
	assert.True(t, got.Equal(inline))

	_, err = loadMasterKey(Config{MasterKey: "not-a-key"}, quietLogger())
	require.ErrorContains(t, err, "PODIUM_MASTER_KEY")
}

// No master key at all is a legitimate configuration: Podium is still a task runner
// without secrets, and the first SetSecret is where that becomes visible.
func TestNoMasterKeyDisablesSecretsRatherThanFailing(t *testing.T) {
	got, err := loadMasterKey(Config{}, quietLogger())
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestConfigReadsTheMasterKeyEnvironment(t *testing.T) {
	t.Setenv("PODIUM_DATABASE_URL", "postgres://podium@127.0.0.1:5432/podium")
	t.Setenv("PODIUM_DEV_TOKEN", "devtoken")
	t.Setenv("PODIUM_MASTER_KEY_FILE", "/etc/podium/master.key")
	t.Setenv("PODIUM_MASTER_KEY", "inline")

	cfg := ConfigFromEnv()
	assert.Equal(t, "/etc/podium/master.key", cfg.MasterKeyFile)
	assert.Equal(t, "inline", cfg.MasterKey)
	require.NoError(t, cfg.Validate(), "a master key is never required to start")
}
