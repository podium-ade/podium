package work

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusFilterIsAStatusOrNone(t *testing.T) {
	for _, in := range []string{"", "triage", "backlog", "todo", " In_Progress ", "done", "canceled", "none"} {
		_, ok := ParseStatusFilter(in)
		assert.True(t, ok, in)
	}
	got, _ := ParseStatusFilter(" In_Progress ")
	assert.Equal(t, StatusInProgress, got)
	for _, in := range []string{"cancelled", "started", "in progress", "all"} {
		_, ok := ParseStatusFilter(in)
		assert.False(t, ok, in)
	}
}
