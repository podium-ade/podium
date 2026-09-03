package runner

import (
	"encoding/json"
	"net"
	"sync"
	"time"
)

// Protocol version carried by every line on the event socket.
const protocolVersion = 1

// Event kinds this slice of the runner emits. Everything else the protocol reserves
// (step, usage, log) belongs to the playbook engine and arrives later.
const (
	kindStarted  = "started"
	kindExited   = "exited"
	kindArtifact = "artifact"
)

// writeTimeout bounds a single write to the node. The runner must never block on the
// node: events are diagnostics, the task's output is what matters.
const writeTimeout = 2 * time.Second

// dialRetryInterval is how often the socket is retried inside the dial window.
const dialRetryInterval = 50 * time.Millisecond

// envelope is the head of every line: {"v":1,"kind":"…","ts":"…", …}.
type envelope struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	TS   string `json:"ts"`
}

type startedEvent struct {
	envelope
	PID int `json:"pid"`
}

type exitedEvent struct {
	envelope
	ExitCode int    `json:"exit_code"`
	Signal   string `json:"signal,omitempty"`
}

// artifactEvent asks the node to collect a file out of the container. Path is inside the
// container; the node copies it out and uploads it through the server. The runner never
// reads the file itself — it may be gigabytes, and the node already has a copy channel
// that does not cost the task any memory.
type artifactEvent struct {
	envelope
	Name        string `json:"name"`
	Path        string `json:"path"`
	ContentType string `json:"content_type,omitempty"`
}

// eventClient writes newline-delimited JSON to the node. A nil *eventClient is a working
// no-op client, which is how "no socket" is represented: the task still runs.
type eventClient struct {
	mu   sync.Mutex
	conn net.Conn
}

// dialEvents connects to path, retrying until within has elapsed. It returns nil when the
// socket never became available — events are best-effort by design, because the node may
// have died and the task's own output still flows through Docker.
func dialEvents(path string, within time.Duration) *eventClient {
	if path == "" {
		return nil
	}
	deadline := time.Now().Add(within)
	for {
		conn, err := net.DialTimeout("unix", path, dialRetryInterval)
		if err == nil {
			return &eventClient{conn: conn}
		}
		if time.Now().After(deadline) {
			return nil
		}
		time.Sleep(dialRetryInterval)
	}
}

func (c *eventClient) started(pid int) {
	c.send(startedEvent{envelope: head(kindStarted), PID: pid})
}

func (c *eventClient) exited(code int, signal string) {
	c.send(exitedEvent{envelope: head(kindExited), ExitCode: code, Signal: signal})
}

func (c *eventClient) artifact(name, path, contentType string) {
	c.send(artifactEvent{
		envelope:    head(kindArtifact),
		Name:        name,
		Path:        path,
		ContentType: contentType,
	})
}

func head(kind string) envelope {
	return envelope{V: protocolVersion, Kind: kind, TS: time.Now().UTC().Format(time.RFC3339Nano)}
}

// send writes one line. A failed write retires the connection: the node is gone (a node
// restart is the ordinary cause) and the runner has nothing useful to do about it.
func (c *eventClient) send(ev any) {
	if c == nil {
		return
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	line = append(line, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := c.conn.Write(line); err != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *eventClient) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}
