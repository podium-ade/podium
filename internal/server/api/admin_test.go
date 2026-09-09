package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// mergeLabels has to leave a node's labels in the same shape enrollment does — sorted and
// deduplicated — however many times an operator adds the label it already has.
func TestMergeLabels(t *testing.T) {
	cases := map[string]struct {
		current, add, remove, want []string
	}{
		"adds sorted": {
			current: []string{"privileged", "linux/amd64"},
			add:     []string{"monorepo"},
			want:    []string{"linux/amd64", "monorepo", "privileged"},
		},
		"adding what is already there changes nothing": {
			current: []string{"linux/amd64"},
			add:     []string{"linux/amd64", "linux/amd64"},
			want:    []string{"linux/amd64"},
		},
		"removes": {
			current: []string{"linux/amd64", "monorepo"},
			remove:  []string{"monorepo"},
			want:    []string{"linux/amd64"},
		},
		"removing a label the node does not have is not an error": {
			current: []string{"linux/amd64"},
			remove:  []string{"gpu"},
			want:    []string{"linux/amd64"},
		},
		"remove wins over add": {
			current: []string{"linux/amd64"},
			add:     []string{"monorepo"},
			remove:  []string{"monorepo"},
			want:    []string{"linux/amd64"},
		},
		"the last label off leaves an empty set, not nil": {
			current: []string{"monorepo"},
			remove:  []string{"monorepo"},
			want:    []string{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, mergeLabels(tc.current, tc.add, tc.remove))
		})
	}
}
