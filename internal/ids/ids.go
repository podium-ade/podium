// Package ids mints the lowercase, prefixed ULIDs Podium uses for every entity.
package ids

import (
	"crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	mu      sync.Mutex
	entropy = ulid.Monotonic(rand.Reader, 0)
)

// New returns "<prefix>_<lowercase ULID>". IDs minted within the same millisecond are
// strictly increasing.
func New(prefix string) string {
	mu.Lock()
	id, err := ulid.New(ulid.Timestamp(time.Now()), entropy)
	mu.Unlock()
	if err != nil {
		// Monotonic entropy exhausted inside one millisecond (needs ~2^80 IDs). A plain
		// random ULID is still unique, it just loses ordering against its neighbours.
		id = ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader)
	}
	return prefix + "_" + strings.ToLower(id.String())
}

// NewTask returns a task_… ID.
func NewTask() string { return New("task") }

// NewNode returns a node_… ID.
func NewNode() string { return New("node") }

// NewLease returns a lease_… ID.
func NewLease() string { return New("lease") }
