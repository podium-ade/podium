package ids

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPrefixAndFormat(t *testing.T) {
	for _, tc := range []struct {
		want string
		got  string
	}{
		{"task_", NewTask()},
		{"node_", NewNode()},
		{"lease_", NewLease()},
		{"x_", New("x")},
	} {
		require.True(t, strings.HasPrefix(tc.got, tc.want), "%q must start with %q", tc.got, tc.want)
		body := strings.TrimPrefix(tc.got, tc.want)
		assert.Len(t, body, 26, "ULID body is 26 chars")
		assert.Equal(t, strings.ToLower(tc.got), tc.got, "IDs are lowercase")
	}
}

func TestNewIsUniqueAndMonotonic(t *testing.T) {
	const n = 1000
	got := make([]string, n)
	seen := make(map[string]struct{}, n)
	for i := range got {
		got[i] = NewTask()
		_, dup := seen[got[i]]
		require.False(t, dup, "duplicate id %s", got[i])
		seen[got[i]] = struct{}{}
	}
	assert.True(t, sort.StringsAreSorted(got), "sequential ids must sort lexicographically")
}
