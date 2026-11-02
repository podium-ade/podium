package conductor

// A warm host turn: the assistant's process stays up after it answers, and the next message
// of the same conversation is written into it instead of starting a new process.
//
// It is the task path's warm window (workspace_warm, warm.ts) brought to the host. The runtime
// is the same and waits the same way, on an inbox socket; on a node the node serves that socket,
// and here this process does, from inside the turn's jail. What it saves is the boot — node, the
// harness, its MCP servers — and the harness session, which carries over to the next message.
//
// A warm turn holds a host slot while it waits, and slots are few. So a conversation that is
// only waiting gives its slot up the moment a turn that has work needs one: acquireHostSlot asks
// the longest-idle warm turn to stop waiting (end_wait), and it exits as it would at the end of
// its window.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The variables a warm host turn's runtime finds its inbox and its window by. Literals, as
// hostEventsSockEnv is: InboxSockEnv in agent/runtime/src/ask.ts, and the variable the node
// sets from workspace_warm in internal/node/docker/run.go.
const (
	hostInboxSockEnv   = "PODIUM_INBOX_SOCK"
	hostWarmSecondsEnv = "PODIUM_WORKSPACE_WARM_SECONDS"
)

// hostInboxWriteTimeout bounds one write to the runtime, as the node's inboxWriteTimeout does.
const hostInboxWriteTimeout = 2 * time.Second

// hostInboxLine is one line written to the runtime: a person's text, or end_wait. The node's
// inboxEnvelope, field for field, because the runtime reading it is the same.
type hostInboxLine struct {
	V       int    `json:"v"`
	Text    string `json:"text,omitempty"`
	EndWait bool   `json:"end_wait,omitempty"`
}

// hostInbox is this process's end of one host turn's inbox: a listener in the jail that the
// runtime dials, and a buffer for lines sent before it has.
type hostInbox struct {
	path string

	mu      sync.Mutex
	ln      net.Listener
	conn    net.Conn
	pending []hostInboxLine
	closed  bool
}

// listenHostInbox opens the inbox in a turn's jail, before the runtime starts.
func listenHostInbox(jail hostPaths) (*hostInbox, error) {
	path := filepath.Join(jail.dir, "inbox.sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on the turn's inbox %s: %w", path, err)
	}
	in := &hostInbox{path: path, ln: ln}
	go in.accept(ln)
	return in, nil
}

// accept takes each connection the runtime makes and flushes what was held for it.
func (in *hostInbox) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		in.mu.Lock()
		if in.closed {
			in.mu.Unlock()
			_ = c.Close()
			return
		}
		if in.conn != nil {
			_ = in.conn.Close()
		}
		in.conn = c
		held := in.pending
		in.pending = nil
		for i, line := range held {
			if err := writeHostInbox(c, line); err != nil {
				_ = c.Close()
				in.conn = nil
				in.pending = held[i:]
				break
			}
		}
		in.mu.Unlock()
	}
}

// inject writes one message for the runtime to continue with.
func (in *hostInbox) inject(text string) error {
	return in.send(hostInboxLine{V: 1, Text: text})
}

// endWait tells a runtime that has answered to stop waiting and exit.
func (in *hostInbox) endWait() error {
	return in.send(hostInboxLine{V: 1, EndWait: true})
}

func (in *hostInbox) send(line hostInboxLine) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.closed {
		return fmt.Errorf("the turn's inbox is closed")
	}
	if in.conn == nil {
		in.pending = append(in.pending, line)
		return nil
	}
	if err := writeHostInbox(in.conn, line); err != nil {
		_ = in.conn.Close()
		in.conn = nil
		in.pending = append(in.pending, line)
	}
	return nil
}

func (in *hostInbox) close() {
	in.mu.Lock()
	in.closed = true
	ln, conn := in.ln, in.conn
	in.ln, in.conn, in.pending = nil, nil, nil
	in.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	_ = os.Remove(in.path)
}

func writeHostInbox(c net.Conn, line hostInboxLine) error {
	body, err := json.Marshal(line)
	if err != nil {
		return err
	}
	_ = c.SetWriteDeadline(time.Now().Add(hostInboxWriteTimeout))
	_, err = c.Write(append(body, '\n'))
	return err
}

// warmHost is a host turn that has answered and is waiting for its conversation's next
// message. idleSince is when it last answered: the oldest gives its slot up first.
type warmHost struct {
	inbox     *hostInbox
	idleSince time.Time
}

// setHostWarm records that this session's host turn has answered and is waiting.
func (c *Conductor) setHostWarm(sessionID string, in *hostInbox) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.sessions[sessionID]
	if st == nil {
		st = &sessionState{}
		c.sessions[sessionID] = st
	}
	st.warmHost = &warmHost{inbox: in, idleSince: time.Now()}
}

// takeHostWarm claims this session's waiting host turn for a new message: it is working again,
// so it is no longer idle and no longer a candidate to evict. Nil when there is none.
func (c *Conductor) takeHostWarm(sessionID string) *hostInbox {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.sessions[sessionID]
	if st == nil || st.warmHost == nil {
		return nil
	}
	in := st.warmHost.inbox
	st.warmHost = nil
	return in
}

// clearHostWarm forgets a session's host turn once it has exited.
func (c *Conductor) clearHostWarm(sessionID string, in *hostInbox) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.sessions[sessionID]
	if st != nil && st.warmHost != nil && st.warmHost.inbox == in {
		st.warmHost = nil
	}
}

// evictIdleHost asks the longest-idle warm host turn to stop waiting, so its slot goes to a
// turn that has work. It reports whether there was one to ask.
func (c *Conductor) evictIdleHost() bool {
	c.mu.Lock()
	var oldestID string
	var oldest *warmHost
	for id, st := range c.sessions {
		if st.warmHost != nil && (oldest == nil || st.warmHost.idleSince.Before(oldest.idleSince)) {
			oldestID, oldest = id, st.warmHost
		}
	}
	if oldest != nil {
		c.sessions[oldestID].warmHost = nil
	}
	c.mu.Unlock()
	if oldest == nil {
		return false
	}
	if err := oldest.inbox.endWait(); err != nil {
		c.logger.Warn("asking an idle warm host turn to give its slot up failed",
			"session_id", oldestID, "error", err)
	}
	c.metrics.HostWarmEvictions.Inc()
	return true
}
