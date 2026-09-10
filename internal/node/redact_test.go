package node

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

func resolved(name, value string) *podiumv1.ResolvedSecret {
	return &podiumv1.ResolvedSecret{Name: name, Target: "env", Key: name, Value: []byte(value)}
}

// all is redact-then-flush for one source, which is what the task loop does at the end of a
// run: it returns everything the redactor will ever emit for the input.
func all(r *redactor, chunks ...string) string {
	var out strings.Builder
	for _, c := range chunks {
		out.Write(r.redact("stdout", "", []byte(c)))
	}
	out.Write(r.flush("stdout", ""))
	return out.String()
}

func TestRedactorIsNilWithoutSecrets(t *testing.T) {
	assert.Nil(t, newRedactor(nil))
	assert.Nil(t, newRedactor([]*podiumv1.ResolvedSecret{}))

	var r *redactor
	assert.Equal(t, []byte("untouched"), r.redact("stdout", "", []byte("untouched")))
	assert.Nil(t, r.flush("stdout", ""))
}

// A short secret is not searched for: redacting every occurrence of two or three bytes
// would destroy a task's output and protect nothing worth protecting.
func TestRedactorIgnoresSecretsShorterThanTheMinimum(t *testing.T) {
	assert.Nil(t, newRedactor([]*podiumv1.ResolvedSecret{resolved("SHORT", "abc")}))

	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("EIGHT", "abcdefgh")})
	require.NotNil(t, r, "exactly the minimum length is redacted")
	assert.Equal(t, "x [redacted:EIGHT] y", all(r, "x abcdefgh y"))
}

func TestRedactorReplacesEveryOccurrence(t *testing.T) {
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("GREETING", "hunter2-hunter2")})
	assert.Equal(t,
		"a [redacted:GREETING] b [redacted:GREETING]\n",
		all(r, "a hunter2-hunter2 b hunter2-hunter2\n"))
}

func TestRedactorCoversBase64AndURLEncodedForms(t *testing.T) {
	const value = "p@ssw0rd/with+slashes"
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("DB_PASSWORD", value)})

	for name, form := range map[string]string{
		"raw":       value,
		"std":       base64.StdEncoding.EncodeToString([]byte(value)),
		"rawstd":    base64.RawStdEncoding.EncodeToString([]byte(value)),
		"url":       base64.URLEncoding.EncodeToString([]byte(value)),
		"rawurl":    base64.RawURLEncoding.EncodeToString([]byte(value)),
		"query":     url.QueryEscape(value),
		"pathescap": url.PathEscape(value),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRedactor([]*podiumv1.ResolvedSecret{resolved("DB_PASSWORD", value)})
			got := all(r, "postgres://user:"+form+"@db:5432/app\n")
			assert.NotContains(t, got, form)
			assert.Contains(t, got, "[redacted:DB_PASSWORD]")
		})
	}
	assert.NotNil(t, r)
}

// The whole point of the carry-over: a secret split across two chunks is still caught.
func TestRedactorCatchesASecretStraddlingAChunkBoundary(t *testing.T) {
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("GREETING", "hunter2-hunter2")})
	got := all(r, "before hunter2", "-hunter2 after\n")
	assert.Equal(t, "before [redacted:GREETING] after\n", got)
}

func TestRedactorCatchesASecretSplitOneByteAtATime(t *testing.T) {
	const value = "correct-horse-battery-staple"
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("PHRASE", value)})

	chunks := make([]string, 0, len(value)+2)
	chunks = append(chunks, "start ")
	for _, b := range []byte(value) {
		chunks = append(chunks, string(b))
	}
	chunks = append(chunks, " end\n")
	assert.Equal(t, "start [redacted:PHRASE] end\n", all(r, chunks...))
}

// Output that cannot be the beginning of a secret is never held back, which is what keeps
// `podium run` printing lines as they happen.
func TestRedactorDoesNotDelayOutputThatCannotMatch(t *testing.T) {
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("GREETING", "hunter2-hunter2")})
	assert.Equal(t, "tick 1\n", string(r.redact("stdout", "", []byte("tick 1\n"))))
	assert.Nil(t, r.flush("stdout", ""), "nothing was held back")
}

// A partial match at the end of a chunk is held back rather than emitted, so a secret that
// completes in the next chunk never reaches the server in pieces.
func TestRedactorHoldsBackAPartialMatch(t *testing.T) {
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("GREETING", "hunter2-hunter2")})
	first := string(r.redact("stdout", "", []byte("value: hunter2-hun")))
	assert.Equal(t, "value: ", first, "the partial match is withheld")
	assert.Equal(t, "hunter2-hun", string(r.flush("stdout", "")),
		"and released unchanged when the stream ends without completing it")
}

// The carry is per source: the task's stdout, its stderr and each sidecar interleave, and
// a tail from one must never be prepended to the next chunk of another.
func TestRedactorCarriesPerSource(t *testing.T) {
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("GREETING", "hunter2-hunter2")})

	assert.Equal(t, "", string(r.redact("stdout", "", []byte("hunter2"))))
	assert.Equal(t, "unrelated\n", string(r.redact("stderr", "", []byte("unrelated\n"))))
	assert.Equal(t, "[redacted:GREETING]\n", string(r.redact("stdout", "", []byte("-hunter2\n"))))
	assert.Nil(t, r.flush("stderr", ""))
}

func TestRedactorHandlesSidecarStreams(t *testing.T) {
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("DB_PASSWORD", "hunter2-hunter2")})
	got := r.redact("sidecar", "db", []byte("PASSWORD=hunter2-hunter2 accepted\n"))
	assert.Equal(t, "PASSWORD=[redacted:DB_PASSWORD] accepted\n", string(got))
}

func TestRedactorPrefersTheLongestMatch(t *testing.T) {
	r := newRedactor([]*podiumv1.ResolvedSecret{
		resolved("SHORTER", "hunter2-hunter2"),
		resolved("LONGER", "hunter2-hunter2-extra"),
	})
	assert.Equal(t, "[redacted:LONGER]\n", all(r, "hunter2-hunter2-extra\n"))
	assert.Equal(t, "[redacted:SHORTER] x\n", all(newRedactor([]*podiumv1.ResolvedSecret{
		resolved("SHORTER", "hunter2-hunter2"),
		resolved("LONGER", "hunter2-hunter2-extra"),
	}), "hunter2-hunter2 x\n"))
}

// The carry is bounded: a task that prints megabytes of a secret's first byte must not
// grow the redactor's state without limit.
func TestRedactorCarryIsBounded(t *testing.T) {
	long := strings.Repeat("a", 4096)
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("LONG", long)})
	require.NotNil(t, r)

	r.redact("stdout", "", []byte(strings.Repeat("a", 100_000)))
	assert.LessOrEqual(t, len(r.carry["stdout\x00"]), maxRedactCarry)
}

func TestRedactorLeavesUnrelatedOutputByteIdentical(t *testing.T) {
	r := newRedactor([]*podiumv1.ResolvedSecret{resolved("GREETING", "hunter2-hunter2")})
	const payload = "the quick brown fox jumps over the lazy dog 0123456789\n"
	assert.Equal(t, payload, all(r, payload))
}
