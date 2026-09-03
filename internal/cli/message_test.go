package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

// noted runs noteMessage against buffers, which is also the non-terminal case: nothing may
// write escape codes into a redirect, and nothing may write to stdout at all.
func noted(t *testing.T, m *podiumv1.Message) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	(&env{stdout: &out, stderr: &errOut}).noteMessage(m)
	return out.String(), errOut.String()
}

func TestNoteMessageIsOneLineOnStderr(t *testing.T) {
	stdout, stderr := noted(t, &podiumv1.Message{Type: "progress", Text: "working"})
	assert.Empty(t, stdout, "stdout is the task's own output, byte for byte")
	assert.Equal(t, "→ message (progress): working\n", stderr)
	assert.NotContains(t, stderr, "\x1b[", "a redirect must not pick up escape codes")
}

func TestNoteMessageIndentsContinuationLines(t *testing.T) {
	_, stderr := noted(t, &podiumv1.Message{
		Type: "final",
		Text: "the PR is ready\nit needs a review\n\nand a merge",
	})
	require.Equal(t, "→ message (final): the PR is ready\n"+
		"  it needs a review\n"+
		"  \n"+
		"  and a merge\n", stderr)
}

func TestNoteMessageListsAttachments(t *testing.T) {
	_, stderr := noted(t, &podiumv1.Message{
		Type:        "final",
		Text:        "here are the screenshots",
		Attachments: []string{"shot-1.png", "shot-2.png"},
	})
	require.Equal(t, "→ message (final): here are the screenshots\n"+
		"  (attachments: shot-1.png, shot-2.png)\n", stderr)
}

// A replayed event whose payload was never stored, or a node that sent a kind with no
// payload, must not take the CLI down.
func TestNoteMessageToleratesAnEmptyPayload(t *testing.T) {
	_, stderr := noted(t, nil)
	assert.Empty(t, stderr)
}
