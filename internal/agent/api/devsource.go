package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/conductor/fakesource"
)

// Dev source routes. They exist only when PODIUM_AGENT_DEV_SOURCE is true and they sit
// behind the same bearer as the AgentService.
const (
	DevInboundPath  = "/dev/inbound"
	DevOutboundPath = "/dev/outbound"
)

// The three test-only knobs the agent runtime reads (step 16). The dev source is the only
// thing in Podium that puts them on a task spec, and the conductor honours them for no
// other source.
const (
	dryRunEnv        = "PODIUM_AGENT_DRY_RUN"
	dryRunSleepMSEnv = "PODIUM_AGENT_DRY_RUN_SLEEP_MS"
	dryRunExitEnv    = "PODIUM_AGENT_DRY_RUN_EXIT"
)

// DevSource is the in-memory source with two HTTP routes bolted on. It is TEST ONLY: it
// lets a test inject an inbound message as if a human had sent it and read back every
// single thing the conductor said, in order, without a Slack workspace.
type DevSource struct {
	*fakesource.Source
}

// NewDevSource returns a started-and-ready dev source.
func NewDevSource() *DevSource {
	return &DevSource{Source: fakesource.New(conductor.KindDev)}
}

// devInbound is the body of POST /dev/inbound.
type devInbound struct {
	Channel string `json:"channel"`
	Thread  string `json:"thread"`
	Author  string `json:"author"`
	Text    string `json:"text"`
	Skill   string `json:"skill"`
	// DryRun and its two companions map onto the runtime's test-only knobs.
	DryRun        bool `json:"dry_run"`
	DryRunSleepMS int  `json:"dry_run_sleep_ms"`
	DryRunExit    int  `json:"dry_run_exit"`
}

// Handler mounts the two dev routes.
func (d *DevSource) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(DevInboundPath, d.inbound)
	mux.HandleFunc(DevOutboundPath, d.outbound)
	return mux
}

func (d *DevSource) inbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body devInbound
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "body must be JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Channel == "" || body.Thread == "" {
		http.Error(w, "channel and thread are required", http.StatusBadRequest)
		return
	}
	if body.Author == "" {
		body.Author = "dev"
	}
	ref := body.Channel + "/" + body.Thread
	key := conductor.KindDev + ":" + body.Channel + ":" + body.Thread

	env := map[string]string{}
	if body.DryRun {
		env[dryRunEnv] = "1"
		if body.DryRunSleepMS > 0 {
			env[dryRunSleepMSEnv] = strconv.Itoa(body.DryRunSleepMS)
		}
		if body.DryRunExit != 0 {
			env[dryRunExitEnv] = strconv.Itoa(body.DryRunExit)
		}
	}

	ev := conductor.InboundEvent{
		SourceKind: conductor.KindDev,
		SourceKey:  key,
		Ref:        ref,
		Channel:    body.Channel,
		Author:     body.Author,
		Text:       body.Text,
		TS:         time.Now().UTC(),
		Skill:      body.Skill,
		// The runtime's schema knows slack, linear and chat. The dev source presents itself
		// as chat, which is what it is: a text conversation with no integration.
		BriefKind: conductor.SourceChat,
		Env:       env,
	}
	if err := d.Send(r.Context(), ev); err != nil {
		http.Error(w, "cancelled", http.StatusRequestTimeout)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"source_key": key, "ref": ref})
}

func (d *DevSource) outbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	writeJSON(w, http.StatusOK, map[string]any{"records": d.RecordsSince(since)})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
