package server

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// logCall matches a slog call site; assignType matches the podium.v1.Assign type name, which
// is what a log statement handling an assignment inevitably mentions.
var (
	logCall    = regexp.MustCompile(`\.(Log|Debug|Info|Warn|Error)(Context)?\(`)
	assignType = regexp.MustCompile(`\bAssign\b`)
)

// unredacted reports whether a source line logs an Assign without redacting it.
func unredacted(line string) bool {
	return logCall.MatchString(line) &&
		assignType.MatchString(line) &&
		!strings.Contains(line, "RedactForLog(")
}

// TestNoAssignIsLoggedUnredacted is the lint rule the acceptance checklist asks for: an Assign
// carries a task's resolved secrets from step 09 onwards, so every log statement that touches
// one must go through podiumv1.RedactForLog. Non-test sources only; a log call spread over
// several lines would slip through, which is why the helper is also asserted to be in use.
func TestNoAssignIsLoggedUnredacted(t *testing.T) {
	require.True(t, unredacted(`s.logger.InfoContext(ctx, "assigned", "assign", (*podiumv1.Assign)(a))`),
		"the detector must catch an unredacted Assign")
	require.False(t, unredacted(`s.logger.InfoContext(ctx, "assigned", "assign", podiumv1.RedactForLog(a))`))
	require.False(t, unredacted(`n.logger.WarnContext(ctx, "pushing assignment failed", "error", err)`),
		"prose about assignments is not an Assign value")

	var offenders []string
	uses := 0
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join("..", "..", dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(src), "\n") {
				if strings.Contains(line, "RedactForLog(") {
					uses++
				}
				if unredacted(line) {
					offenders = append(offenders, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(path), i+1, strings.TrimSpace(line)))
				}
			}
			return nil
		})
		require.NoError(t, err)
	}

	require.Empty(t, offenders, "these log statements pass an Assign without RedactForLog")
	require.GreaterOrEqual(t, uses, 2, "RedactForLog should be defined and called at least once")
}
