package logs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store"
)

func storeChunk(stream, sidecar string) store.LogChunk {
	return store.LogChunk{Stream: stream, Sidecar: sidecar}
}

func TestRollupConfigFromEnvDefaults(t *testing.T) {
	cfg := RollupConfigFromEnv()
	require.Equal(t, DefaultRollupConfig(), cfg)
	require.Equal(t, 24*time.Hour, cfg.Grace, "the design's chunk grace is a day")
}

func TestRollupConfigFromEnvOverrides(t *testing.T) {
	t.Setenv(RollupIntervalEnv, "5s")
	t.Setenv(RollupSettleEnv, "1s")
	t.Setenv(PruneIntervalEnv, "10s")
	t.Setenv(ChunkGraceEnv, "1s")
	require.Equal(t, RollupConfig{
		Interval:      5 * time.Second,
		Settle:        time.Second,
		PruneInterval: 10 * time.Second,
		Grace:         time.Second,
	}, RollupConfigFromEnv())
}

// A typo must not silently delete a day of log retention.
func TestRollupConfigIgnoresUnparseableAndNonPositiveValues(t *testing.T) {
	t.Setenv(ChunkGraceEnv, "twenty-four hours")
	require.Equal(t, DefaultRollupConfig().Grace, RollupConfigFromEnv().Grace)

	t.Setenv(ChunkGraceEnv, "-1h")
	require.Equal(t, DefaultRollupConfig().Grace, RollupConfigFromEnv().Grace)
}

func TestStreamArtifactNames(t *testing.T) {
	require.Equal(t, "stdout", streamArtifactName(storeChunk("stdout", "")))
	require.Equal(t, "stderr", streamArtifactName(storeChunk("stderr", "")))
	require.Equal(t, "sidecar-db", streamArtifactName(storeChunk("sidecar", "db")))
}
