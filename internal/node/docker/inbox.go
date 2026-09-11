package docker

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Where a task's inbox appears inside the container. The runtime's ask tool reads it.
const (
	inboxTarget   = "/podium/inbox.sock"
	inboxSockFile = "inbox.sock"
)

// inboxWriteTimeout bounds one write of an injected message. The runtime is expected to
// be blocked on a read; anything slower means the connection is dead.
const inboxWriteTimeout = 2 * time.Second

// inboxEnvelope is one line the node writes and the runtime reads.
type inboxEnvelope struct {
	V    int    `json:"v"`
	Text string `json:"text"`
}

// inboxLink is the node's end of one task's inbox: a listener the runtime dials, and a
// buffer for injects that arrive before it has.
//
// The events socket is the other direction — container writes, node reads. This one is
// node writes, container reads. Two sockets rather than reversing events.sock, because
// that socket is 0666 and any process in the container can write it; mixing inbound
// human text onto it would be a protocol change for every existing producer.
type inboxLink struct {
	path string

	mu      sync.Mutex
	ln      net.Listener
	conn    net.Conn
	pending []string
	closed  bool
}

// listenInbox opens the task's inbox socket. It must be called before ContainerStart so
// the runtime never finds a missing socket.
func (e *Executor) listenInbox(taskID string) (*inboxLink, error) {
	path := e.inboxSocketPath(taskID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create inbox socket dir for task %s: %w", taskID, err)
	}
	_ = os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on inbox socket %s: %w", path, err)
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // a socket, not a secret
		e.log.Warn("chmod inbox socket", "task", taskID, "error", err)
	}

	l := &inboxLink{path: path, ln: ln}
	go l.accept()
	return l, nil
}

// inboxSocketPath is the host path of one task's inbox socket.
func (e *Executor) inboxSocketPath(taskID string) string {
	if e.sockDir != "" {
		return filepath.Join(e.sockDir, taskID+".inbox.sock")
	}
	return filepath.Join(e.taskDir(taskID), inboxSockFile)
}

// accept takes the next connection and flushes anything that arrived before it. One
// connection at a time: the runtime holds one for the life of an ask, then dials again.
func (l *inboxLink) accept() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			_ = c.Close()
			return
		}
		if l.conn != nil {
			_ = l.conn.Close()
		}
		l.conn = c
		pending := l.pending
		l.pending = nil
		l.mu.Unlock()
		for _, text := range pending {
			if err := writeInbox(c, text); err != nil {
				l.dropConn(c)
				break
			}
		}
	}
}

// write delivers one human message. If the runtime is not connected yet, the text is
// held and flushed when it dials.
func (l *inboxLink) write(text string) error {
	if l == nil {
		return fmt.Errorf("inbox is not open")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return fmt.Errorf("inbox is closed")
	}
	if l.conn == nil {
		l.pending = append(l.pending, text)
		return nil
	}
	if err := writeInbox(l.conn, text); err != nil {
		_ = l.conn.Close()
		l.conn = nil
		l.pending = append(l.pending, text)
		return nil
	}
	return nil
}

func (l *inboxLink) dropConn(c net.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == c {
		_ = l.conn.Close()
		l.conn = nil
	}
}

func (l *inboxLink) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.closed = true
	ln, conn := l.ln, l.conn
	l.ln, l.conn, l.pending = nil, nil, nil
	l.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	_ = os.Remove(l.path)
}

func writeInbox(c net.Conn, text string) error {
	body, err := json.Marshal(inboxEnvelope{V: 1, Text: text})
	if err != nil {
		return err
	}
	_ = c.SetWriteDeadline(time.Now().Add(inboxWriteTimeout))
	_, err = c.Write(append(body, '\n'))
	return err
}
