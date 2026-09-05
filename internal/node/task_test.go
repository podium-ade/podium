package node

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunErrorLineStaysOneLine: a failed sidecar's error carries the sidecar's own log tail,
// and "task run finished error=<a hundred lines>" is not a log record. The full text still
// goes to the control plane as the task's error event, which is where an operator reads it.
func TestRunErrorLineStaysOneLine(t *testing.T) {
	err := errors.New("sidecar not ready: sidecar db not ready: nothing is listening on port 5432\n" +
		"[db] FATAL: could not open my data directory\n[db] and another line")
	line, ok := runErrorLine(err).(string)
	require.True(t, ok)
	require.NotContains(t, line, "\n")
	require.Contains(t, line, "nothing is listening on port 5432")
	require.Contains(t, line, "error event")

	require.Equal(t, "no such image", runErrorLine(errors.New("no such image")))
	require.Nil(t, runErrorLine(nil), "a successful run logs no error")
}
