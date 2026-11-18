package work

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStateFilterIsAStateOrAttention(t *testing.T) {
	for _, in := range []string{"", "new", "working", " Waiting ", "failed", "done", "attention"} {
		_, ok := ParseStateFilter(in)
		assert.True(t, ok, in)
	}
	got, _ := ParseStateFilter(" Waiting ")
	assert.Equal(t, StateWaiting, got)
	for _, in := range []string{"in_progress", "started", "none", "all"} {
		_, ok := ParseStateFilter(in)
		assert.False(t, ok, in)
	}
}
