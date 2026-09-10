package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

func chunk(stream podiumv1.LogChunk_Stream, sidecar, text string) *podiumv1.TaskEvent {
	return &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
		Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
			Stream:      stream,
			SidecarName: sidecar,
			Bytes:       []byte(text),
		}},
	}
}

func TestLogPrinterKeepsTheTaskStreamsSeparate(t *testing.T) {
	var out, errOut bytes.Buffer
	p := newLogPrinter(&out, &errOut)

	p.write(chunk(podiumv1.LogChunk_STREAM_STDOUT, "", "hello\n"))
	p.write(chunk(podiumv1.LogChunk_STREAM_STDERR, "", "oops\n"))
	p.flush()

	assert.Equal(t, "hello\n", out.String())
	assert.Equal(t, "oops\n", errOut.String())
}

func TestLogPrinterPrefixesSidecarOutput(t *testing.T) {
	var out, errOut bytes.Buffer
	p := newLogPrinter(&out, &errOut)

	p.write(chunk(podiumv1.LogChunk_STREAM_SIDECAR, "db", "ready to accept connections\n"))
	p.flush()

	assert.Empty(t, out.String(), "a sidecar's output is not the task's output")
	assert.Equal(t, "[db] ready to accept connections\n", errOut.String())
}

// A chunk is bytes, not lines: the node coalesces a run of output and may cut it anywhere.
func TestLogPrinterJoinsSidecarLinesSplitAcrossChunks(t *testing.T) {
	var out, errOut bytes.Buffer
	p := newLogPrinter(&out, &errOut)

	p.write(chunk(podiumv1.LogChunk_STREAM_SIDECAR, "db", "listening on "))
	p.write(chunk(podiumv1.LogChunk_STREAM_SIDECAR, "db", "port 5432\nnext line\n"))
	p.flush()

	assert.Equal(t, "[db] listening on port 5432\n[db] next line\n", errOut.String())
}

func TestLogPrinterKeepsSidecarsApartAndFlushesTheTail(t *testing.T) {
	var out, errOut bytes.Buffer
	p := newLogPrinter(&out, &errOut)

	p.write(chunk(podiumv1.LogChunk_STREAM_SIDECAR, "db", "starting"))
	p.write(chunk(podiumv1.LogChunk_STREAM_SIDECAR, "cache", "Ready to accept\n"))
	assert.Equal(t, "[cache] Ready to accept\n", errOut.String(), "db's partial line is still pending")

	p.flush()
	assert.Equal(t, "[cache] Ready to accept\n[db] starting\n", errOut.String())

	errOut.Reset()
	p.flush()
	assert.Empty(t, errOut.String(), "flush is not repeatable")
}

func TestLogPrinterIgnoresNonLogEvents(t *testing.T) {
	var out, errOut bytes.Buffer
	p := newLogPrinter(&out, &errOut)
	p.write(&podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED})
	p.flush()
	assert.Empty(t, out.String())
	assert.Empty(t, errOut.String())
}
