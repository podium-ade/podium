package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

func TestParseSecretRef(t *testing.T) {
	cases := []struct {
		in   string
		want spec.SecretRef
	}{
		{"GREETING", spec.SecretRef{Name: "GREETING", Target: "env", Key: "GREETING"}},
		{"GREETING:env:OTHER", spec.SecretRef{Name: "GREETING", Target: "env", Key: "OTHER"}},
		{"PEM:file:/podium/secrets/pem", spec.SecretRef{Name: "PEM", Target: "file", Key: "/podium/secrets/pem"}},
		// SplitN keeps the rest of the string, so a path with a colon in it survives.
		{"PEM:file:/podium/secrets/a:b", spec.SecretRef{Name: "PEM", Target: "file", Key: "/podium/secrets/a:b"}},
	}
	for _, tc := range cases {
		got, err := parseSecretRef(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}

	_, err := parseSecretRef("GREETING:file")
	require.ErrorContains(t, err, "is not NAME, NAME:env:KEY or NAME:file:/absolute/path")
}

func TestBuildSpecTurnsSecretFlagsIntoRefs(t *testing.T) {
	got, err := buildSpec("", "alpine:3", "", nil, nil,
		[]string{"GREETING", "PEM:file:/podium/secrets/pem"}, 0, []string{"true"})
	require.NoError(t, err)
	assert.Equal(t, []spec.SecretRef{
		{Name: "GREETING", Target: "env", Key: "GREETING"},
		{Name: "PEM", Target: "file", Key: "/podium/secrets/pem"},
	}, got.Secrets)

	_, err = buildSpec("", "alpine:3", "", nil, nil, []string{"GREETING:vault:X"}, 0, []string{"true"})
	require.ErrorContains(t, err, "is not a secret target")
}

func newSecretValueEnv(stdin string) (*env, *cobra.Command, *bytes.Buffer) {
	stderr := &bytes.Buffer{}
	e := &env{stdout: &bytes.Buffer{}, stderr: stderr, stdin: bytes.NewBufferString(stdin)}
	cmd := &cobra.Command{}
	cmd.SetIn(bytes.NewBufferString(stdin))
	return e, cmd, stderr
}

func TestReadSecretValueFromStdinStripsOneTrailingNewline(t *testing.T) {
	for in, want := range map[string]string{
		"hunter2\n":          "hunter2",
		"hunter2\r\n":        "hunter2",
		"hunter2":            "hunter2",
		"hunter2\n\n":        "hunter2\n",
		"line one\nline 2\n": "line one\nline 2",
	} {
		e, cmd, _ := newSecretValueEnv(in)
		got, err := readSecretValue(e, cmd, "", "")
		require.NoError(t, err)
		assert.Equal(t, want, string(got), "input %q", in)
	}
}

// --value is accepted but never quietly: it puts the secret in the shell's history and in
// the process table, and the CLI says so every time.
func TestReadSecretValueWarnsAboutTheValueFlag(t *testing.T) {
	e, cmd, stderr := newSecretValueEnv("")
	got, err := readSecretValue(e, cmd, "hunter2", "")
	require.NoError(t, err)
	assert.Equal(t, "hunter2", string(got))
	assert.Contains(t, stderr.String(), "shell history")
	assert.NotContains(t, stderr.String(), "hunter2", "the warning must not repeat the value")
}

func TestReadSecretValueFromFileIsByteForByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.pem")
	const contents = "-----BEGIN KEY-----\nabc\n-----END KEY-----\n"
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	e, cmd, _ := newSecretValueEnv("")
	got, err := readSecretValue(e, cmd, "", path)
	require.NoError(t, err)
	assert.Equal(t, contents, string(got), "a PEM file keeps its trailing newline")

	_, err = readSecretValue(e, cmd, "inline", path)
	require.ErrorContains(t, err, "mutually exclusive")

	_, err = readSecretValue(e, cmd, "", filepath.Join(t.TempDir(), "absent"))
	require.ErrorContains(t, err, "read ")
}

func TestZeroValueScrubs(t *testing.T) {
	b := []byte("hunter2")
	zeroValue(b)
	assert.Equal(t, make([]byte, 7), b)
}
