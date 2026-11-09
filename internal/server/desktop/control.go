package desktop

import (
	"context"
	"sync"

	"github.com/coder/websocket"
)

// voxl lets every viewer resize the shared desktop to its own window. Two viewers of
// different sizes then take turns resizing it several times a second, for ever. So only one
// viewer of a desktop may resize it: the newest, which is the window the person just opened.
// The others watch letterboxed. When the one in control leaves, control passes to the newest
// that remains and its last requested size is replayed, so the desktop fits it again.

// resizePrefix starts voxl's resize message, "sz,W,H" (web/src/protocol.ts in voxl).
const resizePrefix = "sz,"

type viewer struct {
	up *websocket.Conn

	mu   sync.Mutex
	size string // the last resize this viewer asked for
}

func (v *viewer) remember(msg string) {
	v.mu.Lock()
	v.size = msg
	v.mu.Unlock()
}

func (v *viewer) lastSize() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.size
}

// controls is every desktop's viewers, oldest first; the last of each is in control.
type controls struct {
	mu      sync.Mutex
	viewers map[string][]*viewer
}

func (c *controls) join(task string, v *viewer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.viewers == nil {
		c.viewers = map[string][]*viewer{}
	}
	c.viewers[task] = append(c.viewers[task], v)
}

// leave drops v. If v was in control, the viewer now in control has its size replayed.
func (c *controls) leave(task string, v *viewer) {
	c.mu.Lock()
	vs := c.viewers[task]
	was := len(vs) > 0 && vs[len(vs)-1] == v
	for i, x := range vs {
		if x == v {
			vs = append(vs[:i:i], vs[i+1:]...)
			break
		}
	}
	if len(vs) == 0 {
		delete(c.viewers, task)
	} else {
		c.viewers[task] = vs
	}
	var next *viewer
	if was && len(vs) > 0 {
		next = vs[len(vs)-1]
	}
	c.mu.Unlock()
	if next == nil {
		return
	}
	if size := next.lastSize(); size != "" {
		_ = next.up.Write(context.Background(), websocket.MessageText, []byte(size))
	}
}

func (c *controls) inControl(task string, v *viewer) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	vs := c.viewers[task]
	return len(vs) > 0 && vs[len(vs)-1] == v
}

// count is how many viewers a desktop has, for tests and logs.
func (c *controls) count(task string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.viewers[task])
}
