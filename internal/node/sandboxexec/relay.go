package sandboxexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox"
	"github.com/podium-ade/podium/internal/sandboxagent/wire"
)

// emitter numbers events the way docker's does; the node renumbers them anyway.
type emitter struct {
	mu  sync.Mutex
	ch  chan<- docker.Event
	seq uint64
}

func newEmitter(ch chan<- docker.Event, base uint64) *emitter { return &emitter{ch: ch, seq: base} }

func (em *emitter) emit(kind string, payload any) {
	em.forward(docker.Event{Kind: kind, TS: time.Now().UTC(), Payload: payload})
}

func (em *emitter) forward(ev docker.Event) {
	em.mu.Lock()
	defer em.mu.Unlock()
	em.seq++
	ev.Seq = em.seq
	em.ch <- ev
}

// agentClient is one task's connection to its agent.
type agentClient struct {
	ep    sandbox.Endpoint
	token string
	hc    *http.Client
}

func (e *Executor) agentClient(t *task) *agentClient {
	t.mu.Lock()
	defer t.mu.Unlock()
	ep := sandbox.Endpoint{URL: strings.TrimRight(t.rec.URL, "/"), Header: t.rec.Header}
	for k, v := range t.rec.Cookies {
		ep.Cookies = append(ep.Cookies, &http.Cookie{Name: k, Value: v})
	}
	return &agentClient{ep: ep, token: t.rec.Token, hc: e.client}
}

func (c *agentClient) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.ep.URL+path, body)
	if err != nil {
		return nil, err
	}
	c.ep.Apply(req)
	req.Header.Set("Authorization", "Bearer "+c.token)
	return req, nil
}

func (c *agentClient) do(req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("agent %s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *agentClient) postJSON(ctx context.Context, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.request(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *agentClient) getJSON(ctx context.Context, path string, out any) error {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// waitHealthy waits for the agent to answer through the provider's front door, which can
// take a few seconds to route a newly exposed port.
func (c *agentClient) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(2 * time.Minute)
	var last error
	for time.Now().Before(deadline) {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		last = c.getJSON(rctx, "/healthz", nil)
		cancel()
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("agent never answered: %w", last)
}

// adoptFilter is what a node that adopted a run must not repeat: log bytes the control
// plane already has, and any other event the previous incarnation forwarded.
type adoptFilter struct {
	skipStdout, skipStderr int64
	seq                    uint64
}

// relay forwards the agent's events until the run finishes, serving transfers as they
// come and reconnecting when the stream drops.
func (e *Executor) relay(ctx context.Context, t *task, c *agentClient, req docker.Request, em *emitter, skip *adoptFilter) (docker.Result, error) {
	var (
		from     uint64
		res      docker.Result
		finished bool
		served   sync.Map
		failures int
		aborted  error
	)
	for !finished {
		err := c.stream(ctx, from, func(f wire.Frame) error {
			if f.Transfer != nil {
				if _, dup := served.LoadOrStore(f.Transfer.ID, true); !dup {
					go e.serveTransfer(ctx, c, req, *f.Transfer)
				}
				return nil
			}
			from = f.Event.Seq + 1
			ev, err := f.Event.ToEvent()
			if err != nil {
				return err
			}
			switch ev.Kind {
			case docker.KindProvisioning:
				return nil
			case docker.KindFinished:
				p, _ := ev.Payload.(docker.FinishedPayload)
				res = docker.Result{ExitCode: p.ExitCode, Usage: p.Usage, Held: p.Held}
				finished = true
			case docker.KindExited:
				p, _ := ev.Payload.(docker.ExitedPayload)
				res.OOMKilled = p.OOMKilled
			case docker.KindError:
				if p, _ := ev.Payload.(docker.ErrorPayload); p.AbortsRun {
					aborted = errors.New(p.Message)
				}
			}
			if skip != nil && !skip.keep(&ev) {
				return nil
			}
			em.forward(ev)
			if ev.Kind != docker.KindLog {
				t.mu.Lock()
				t.rec.Seq = f.Event.Seq
				t.mu.Unlock()
				if err := e.save(t); err != nil {
					e.log.Warn("saving the sandbox record failed", "task", req.TaskID, "error", err)
				}
			}
			return nil
		})
		if finished {
			break
		}
		if err == nil {
			// The agent ends the stream only when its run is over: one that never
			// finished was aborted, and said why in an error event already forwarded.
			if aborted == nil {
				aborted = errors.New("the agent's run ended without finishing")
			}
			return res, errAgentAborted{aborted}
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		failures++
		if failures > 30 {
			return res, fmt.Errorf("lost the agent's event stream: %w", err)
		}
		e.log.Warn("agent event stream dropped; reconnecting", "task", req.TaskID, "from", from, "error", err)
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(time.Duration(min(failures, 10)) * time.Second):
		}
	}
	return res, nil
}

func (f *adoptFilter) keep(ev *docker.Event) bool {
	switch ev.Kind {
	case docker.KindLog:
		p, _ := ev.Payload.(docker.LogPayload)
		var skip int64
		switch p.Stream {
		case docker.StreamStdout:
			skip = f.skipStdout
		case docker.StreamStderr:
			skip = f.skipStderr
		default:
			// Like a Docker adoption, sidecar output is not replayed: there is no cursor
			// for it on the control plane.
			return false
		}
		if p.Offset <= skip {
			return false
		}
		if start := p.Offset - int64(len(p.Bytes)); start < skip {
			p.Bytes = p.Bytes[skip-start:]
			ev.Payload = p
		}
		return true
	case docker.KindExited, docker.KindFinished:
		return true
	default:
		return ev.Seq > f.seq
	}
}

// stream reads frames from ?from= until the agent closes the stream at the end of the run
// (nil) or the connection fails (an error).
func (c *agentClient) stream(ctx context.Context, from uint64, fn func(wire.Frame) error) error {
	u := strings.Replace(c.ep.URL, "http", "ws", 1) + "/events?from=" + strconv.FormatUint(from, 10)
	hdr := http.Header{}
	probe, _ := http.NewRequest(http.MethodGet, c.ep.URL, nil)
	c.ep.Apply(probe)
	for k, v := range probe.Header {
		hdr[k] = v
	}
	hdr.Set("Authorization", "Bearer "+c.token)
	conn, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: hdr, HTTPClient: c.hc})
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(64 << 20)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return err
		}
		var f wire.Frame
		if err := json.Unmarshal(data, &f); err != nil {
			return err
		}
		if err := fn(f); err != nil {
			return err
		}
	}
}

// serveTransfer moves one transfer's bytes and answers the agent.
func (e *Executor) serveTransfer(ctx context.Context, c *agentClient, req docker.Request, tr wire.Transfer) {
	var res wire.TransferResult
	err := e.transfer(ctx, c, req, tr, &res)
	if err != nil {
		res.Error = err.Error()
		e.log.Warn("transfer failed", "task", req.TaskID, "kind", tr.Kind, "error", err)
	}
	if tr.Kind == wire.TransferWorkspaceRestore && err == nil && !res.NotFound {
		return // the PUT answered it
	}
	if perr := c.postJSON(ctx, "/transfers/"+tr.ID+"/result", res, nil); perr != nil {
		e.log.Warn("answering a transfer failed", "task", req.TaskID, "kind", tr.Kind, "error", perr)
	}
}

func (e *Executor) transfer(ctx context.Context, c *agentClient, req docker.Request, tr wire.Transfer, res *wire.TransferResult) error {
	switch tr.Kind {
	case wire.TransferArtifact:
		if req.Artifacts == nil {
			return errors.New("this node cannot store artifacts")
		}
		body, err := c.fetch(ctx, tr.ID)
		if err != nil {
			return err
		}
		defer func() { _ = body.Close() }()
		stored, err := req.Artifacts.UploadArtifact(ctx, docker.ArtifactUpload{
			TaskID: req.TaskID, LeaseID: req.LeaseID, Name: tr.Name, ContentType: tr.ContentType, Size: tr.Size, Body: body,
		})
		res.Stored = stored
		return err
	case wire.TransferWorkspaceRestore:
		if req.Workspace == nil || req.Workspace.Restore == nil {
			res.NotFound = true
			return nil
		}
		r, found, err := req.Workspace.Restore(ctx)
		if err != nil {
			return err
		}
		if !found || r == nil {
			res.NotFound = true
			return nil
		}
		defer func() { _ = r.Close() }()
		hreq, err := c.request(ctx, http.MethodPut, "/transfers/"+tr.ID, r)
		if err != nil {
			return err
		}
		return c.do(hreq, nil)
	case wire.TransferWorkspaceSave:
		if req.Workspace == nil || req.Workspace.Save == nil {
			return nil
		}
		body, err := c.fetch(ctx, tr.ID)
		if err != nil {
			return err
		}
		defer func() { _ = body.Close() }()
		f, err := os.CreateTemp("", "podium-ws-"+req.TaskID+"-*.tar")
		if err != nil {
			return err
		}
		defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
		n, err := io.Copy(f, body)
		if err != nil {
			return err
		}
		return req.Workspace.Save(ctx, f.Name(), n)
	}
	return fmt.Errorf("unknown transfer kind %q", tr.Kind)
}

func (c *agentClient) fetch(ctx context.Context, id string) (io.ReadCloser, error) {
	req, err := c.request(ctx, http.MethodGet, "/transfers/"+id, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("fetch transfer %s: %s", id, resp.Status)
	}
	return resp.Body, nil
}

// withCredentials is the link a person opens for an exposed port.
func withCredentials(ep sandbox.Endpoint) string {
	if ep.BrowserURL != "" {
		return ep.BrowserURL
	}
	return ep.URL
}

// Adopt reattaches to a run whose sandbox outlived the previous daemon.
func (e *Executor) Adopt(ctx context.Context, req docker.AdoptRequest, events chan<- docker.Event) (docker.Result, error) {
	em := newEmitter(events, req.FromSeq)
	rec, err := e.load(req.TaskID)
	if err != nil {
		em.emit(docker.KindError, docker.ErrorPayload{Message: err.Error(), AbortsRun: true})
		return docker.Result{}, err
	}
	t := &task{rec: rec}
	e.mu.Lock()
	e.tasks[req.TaskID] = t
	e.mu.Unlock()
	defer e.forget(req.TaskID)

	em.emit(docker.KindStep, docker.StepPayload{Name: docker.StepReattached, Status: "started"})
	res, err := e.relay(ctx, t, e.agentClient(t), docker.Request{TaskID: req.TaskID, LeaseID: req.LeaseID}, em,
		&adoptFilter{skipStdout: req.SkipStdout, skipStderr: req.SkipStderr, seq: rec.Seq})
	if err != nil {
		em.emit(docker.KindError, docker.ErrorPayload{Message: err.Error(), Retryable: true, AbortsRun: true})
	}
	return res, err
}

// errAgentAborted is a run the agent gave up on. Its error event has already gone out.
type errAgentAborted struct{ error }

func (e errAgentAborted) Unwrap() error { return e.error }
