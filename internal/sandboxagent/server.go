// Package sandboxagent is podium-sandbox-agent: the process podium-node starts inside a
// cloud sandbox. It runs one task with the strategy the node picked and serves the run's
// events, transfers and controls over HTTP to the node, which reaches it through the
// provider's exposed port. Every request carries the run's bearer token.
package sandboxagent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandboxagent/wire"
)

// Strategy runs a task. *docker.Executor is one.
type Strategy interface {
	Run(ctx context.Context, req docker.Request, events chan<- docker.Event) (docker.Result, error)
	Cancel(taskID string)
	Inject(taskID, text string) error
	HeldExit(taskID string) (int, bool)
}

// StrategyFactory builds the named strategy the first time a run asks for it.
type StrategyFactory func(ctx context.Context, name string) (Strategy, error)

// Server is one agent: it accepts one run.
type Server struct {
	token   []byte
	dir     string
	factory StrategyFactory
	logger  *slog.Logger

	journal   *journal
	transfers *transfers

	mu       sync.Mutex
	strategy Strategy
	taskID   string
	running  bool
	finished bool
	held     bool
}

// New prepares an agent that keeps its journal and staged transfers under dir.
func New(token string, dir string, factory StrategyFactory, logger *slog.Logger) (*Server, error) {
	if token == "" {
		return nil, errors.New("sandbox agent: empty token")
	}
	if err := os.MkdirAll(filepath.Join(dir, "transfers"), 0o700); err != nil {
		return nil, err
	}
	j, err := newJournal(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		return nil, err
	}
	return &Server{token: []byte(token), dir: dir, factory: factory, logger: logger,
		journal: j, transfers: newTransfers(filepath.Join(dir, "transfers"))}, nil
}

// Handler is the agent's HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", s.handleRun)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /cancel", s.handleCancel)
	mux.HandleFunc("POST /inject", s.handleInject)
	mux.HandleFunc("GET /transfers/{id}", s.handleTransferGet)
	mux.HandleFunc("PUT /transfers/{id}", s.handleTransferPut)
	mux.HandleFunc("POST /transfers/{id}/result", s.handleTransferResult)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare(got, s.token) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req wire.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if s.taskID != "" {
		same := s.taskID == req.TaskID
		s.mu.Unlock()
		if same {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.Error(w, "this agent already runs another task", http.StatusConflict)
		return
	}
	st, err := s.factory(r.Context(), req.Strategy)
	if err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.strategy, s.taskID, s.running = st, req.TaskID, true
	s.mu.Unlock()

	// Inside the sandbox the sandbox is the machine: the strategy runs the task as any
	// node runs its own.
	req.Spec.Sandbox = nil
	dreq := docker.Request{
		TaskID: req.TaskID, LeaseID: req.LeaseID, Spec: req.Spec,
		Secrets: req.Secrets, Registries: req.Registries, Preview: req.Preview,
	}
	if req.Artifacts {
		dreq.Artifacts = s.transfers
	}
	if req.Workspace {
		dreq.Workspace = s.transfers.workspace()
	}
	go s.run(st, dreq)
	w.WriteHeader(http.StatusAccepted)
}

// run drives the strategy. Its context is never cancelled from here: a cancel is the
// strategy's own business, so the run always ends with exited and finished.
func (s *Server) run(st Strategy, req docker.Request) {
	events := make(chan docker.Event, 256)
	done := make(chan docker.Result, 1)
	go func() {
		res, err := st.Run(context.Background(), req, events)
		if err != nil {
			s.logger.Warn("run ended with an error", "task_id", req.TaskID, "error", err)
		}
		done <- res
	}()
	record := func(ev docker.Event) {
		we, err := wire.FromEvent(ev)
		if err == nil {
			err = s.journal.append(we)
		}
		if err != nil {
			s.logger.Error("journal event", "task_id", req.TaskID, "kind", ev.Kind, "error", err)
		}
	}
	for {
		select {
		case ev := <-events:
			record(ev)
		case res := <-done:
			for drained := false; !drained; {
				select {
				case ev := <-events:
					record(ev)
				default:
					drained = true
				}
			}
			s.mu.Lock()
			s.running, s.finished, s.held = false, true, res.Held
			s.mu.Unlock()
			s.journal.close()
			return
		}
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	st := wire.Status{Running: s.running, Finished: s.finished, Held: s.held}
	strategy, taskID := s.strategy, s.taskID
	s.mu.Unlock()
	if strategy != nil {
		if code, ok := strategy.HeldExit(taskID); ok {
			st.HeldExit = &code
		}
	}
	writeJSON(w, st)
}

func (s *Server) handleCancel(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	st, id := s.strategy, s.taskID
	s.mu.Unlock()
	if st != nil {
		st.Cancel(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	st, id := s.strategy, s.taskID
	s.mu.Unlock()
	if st == nil {
		http.Error(w, "no task", http.StatusConflict)
		return
	}
	if err := st.Inject(id, body.Text); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEvents streams frames: the transfers waiting now and as they appear, and the
// journal from ?from=. It ends once the run has finished and the journal is read out.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	from, _ := strconv.ParseUint(r.URL.Query().Get("from"), 10, 64)
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	ctx := c.CloseRead(r.Context())

	var wmu sync.Mutex
	send := func(f wire.Frame) error {
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, b)
	}

	tctx, stopTransfers := context.WithCancel(ctx)
	defer stopTransfers()
	go func() {
		seen := map[string]bool{}
		for {
			pending, changed := s.transfers.snapshot()
			for _, tr := range pending {
				if !seen[tr.ID] {
					seen[tr.ID] = true
					if send(wire.Frame{Transfer: &tr}) != nil {
						return
					}
				}
			}
			select {
			case <-tctx.Done():
				return
			case <-changed:
			}
		}
	}()

	err = s.journal.follow(ctx, from, func(ev wire.Event) error { return send(wire.Frame{Event: &ev}) })
	if err != nil {
		return
	}
	_ = c.Close(websocket.StatusNormalClosure, "run finished")
}

func (s *Server) handleTransferGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.transfers.get(r.PathValue("id"))
	if !ok || p.Kind == wire.TransferWorkspaceRestore {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p.path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(p.Size, 10))
	_, _ = io.Copy(w, f)
}

func (s *Server) handleTransferPut(w http.ResponseWriter, r *http.Request) {
	p, ok := s.transfers.get(r.PathValue("id"))
	if !ok || p.Kind != wire.TransferWorkspaceRestore {
		http.NotFound(w, r)
		return
	}
	f, err := os.OpenFile(p.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("write workspace: %v", err), http.StatusInternalServerError)
		return
	}
	s.transfers.resolve(p.ID, wire.TransferResult{})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTransferResult(w http.ResponseWriter, r *http.Request) {
	var res wire.TransferResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.transfers.resolve(r.PathValue("id"), res) {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
