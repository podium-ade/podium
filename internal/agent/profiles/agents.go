package profiles

import (
	"fmt"
	"strings"
)

// An agent backend is the harness a turn runs on, together with the credential it spends.
// A skill names one; the profile supplies the default.
//
// Both backends in this file are the SAME runtime image driving the SAME Claude Agent SDK.
// What `grok` changes is where the SDK sends its requests: xAI serves an Anthropic-shaped
// /v1/messages, so pointing the SDK's base URL at api.x.ai and handing it an xAI credential
// runs the whole harness — tools, transcript, artifacts, exit codes — against a Grok model.
// That is why this is a field and not a second image.
const (
	AgentClaude = "claude"
	AgentGrok   = "grok"
)

// DefaultAgent is what a profile that names no agent runs on. It is Claude because that is
// the backend this project's runtime image was built and proved against.
const DefaultAgent = AgentClaude

// The providers a backend spends a credential from. A provider is a set of credentials and
// an endpoint; a backend is a harness pointed at one.
const (
	ProviderAnthropic = "anthropic"
	ProviderXAI       = "xai"
)

// The reasoning-effort levels. They are the Claude Agent SDK's own vocabulary, which xAI's
// reasoning_effort shares for its first four; `max` is Anthropic-only and the catalogue
// below is what says so per model.
const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortXHigh  = "xhigh"
	EffortMax    = "max"
)

// anthropicEfforts is every level the Claude models here accept.
var anthropicEfforts = []string{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// grokEfforts stops at xhigh: xAI's reasoning_effort has no `max`.
var grokEfforts = []string{EffortLow, EffortMedium, EffortHigh, EffortXHigh}

// grokEffortsNoXHigh is for the Grok models that document xhigh as a synonym for high.
// Offering a level that silently means another one is worse than not offering it.
var grokEffortsNoXHigh = []string{EffortLow, EffortMedium, EffortHigh}

// Model is one model a backend can be pointed at.
type Model struct {
	ID            string
	DisplayName   string
	Note          string
	ContextTokens int
	// Efforts are the levels this model accepts, weakest first. Empty means the model takes
	// no effort setting at all, and naming one for it is refused.
	Efforts []string
}

// Backend is one agent backend: a harness, the credential it spends, and its models.
type Backend struct {
	ID          string
	DisplayName string
	Provider    string
	Note        string
	// DefaultModel is what a profile naming this backend and no model runs.
	DefaultModel string
	Models       []Model
}

// Backends is the catalogue, in the order the picker shows it.
//
// It is deliberately a curated shortlist rather than whatever the provider's model list
// endpoint returns today: this is also the validator, and a picker of forty ids nobody has
// heard of is not a picker. A model that is not here is still usable — a model id is free
// text everywhere it is set, and only a *known* id has its effort levels checked.
var Backends = []Backend{{
	ID:           AgentClaude,
	DisplayName:  "Claude",
	Provider:     ProviderAnthropic,
	Note:         "The Claude Agent SDK against Anthropic's API.",
	DefaultModel: "claude-opus-5",
	Models: []Model{
		{
			ID: "claude-opus-5", DisplayName: "Claude Opus 5",
			Note: "The default. Best on long agentic work.", ContextTokens: 1_000_000,
			Efforts: anthropicEfforts,
		},
		{
			ID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5",
			Note:          "Cheaper, still strong. A good default for high-volume skills.",
			ContextTokens: 1_000_000, Efforts: anthropicEfforts,
		},
		{
			ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5",
			Note:          "Fastest and cheapest. For narrow, well-specified skills.",
			ContextTokens: 200_000, Efforts: anthropicEfforts,
		},
		{
			ID: "claude-opus-4-8", DisplayName: "Claude Opus 4.8",
			Note:          "The previous Opus. Pin it when a skill is tuned to it.",
			ContextTokens: 1_000_000, Efforts: anthropicEfforts,
		},
		{
			ID: "claude-fable-5-1", DisplayName: "Claude Fable 5.1",
			Note:          "The most capable model, and the most expensive.",
			ContextTokens: 1_000_000, Efforts: anthropicEfforts,
		},
	},
}, {
	ID:           AgentGrok,
	DisplayName:  "Grok",
	Provider:     ProviderXAI,
	Note:         "The same harness, pointed at xAI's Anthropic-compatible endpoint.",
	DefaultModel: "grok-4.6",
	Models: []Model{
		{
			ID: "grok-4.6", DisplayName: "Grok 4.6",
			Note: "The default. The only Grok that takes xhigh.", ContextTokens: 500_000,
			Efforts: grokEfforts,
		},
		{
			ID: "grok-4.5", DisplayName: "Grok 4.5",
			Note:          "The previous generation. xhigh is treated as high, so it is not offered.",
			ContextTokens: 500_000, Efforts: grokEffortsNoXHigh,
		},
		{
			ID: "grok-4.3", DisplayName: "Grok 4.3",
			Note: "A million tokens of context.", ContextTokens: 1_000_000,
			Efforts: grokEffortsNoXHigh,
		},
		{
			ID: "grok-build-0.1", DisplayName: "Grok Build 0.1",
			Note: "xAI's coding model.", ContextTokens: 256_000,
			Efforts: grokEffortsNoXHigh,
		},
	},
}}

// FindBackend is the backend with this id, or false. An empty id is DefaultAgent.
func FindBackend(id string) (Backend, bool) {
	if id == "" {
		id = DefaultAgent
	}
	for _, b := range Backends {
		if b.ID == id {
			return b, true
		}
	}
	return Backend{}, false
}

// FindModel is a known model of a backend, or false. An unknown id is not an error
// anywhere: it is how a model released after this binary was built gets used.
func (b Backend) FindModel(id string) (Model, bool) {
	for _, m := range b.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// backendOf is the backend that offers this model, or false. A model id is unique across
// the catalogue, which is what makes "picking a model picks its backend" well defined.
func backendOf(model string) (Backend, bool) {
	for _, b := range Backends {
		if _, ok := b.FindModel(model); ok {
			return b, true
		}
	}
	return Backend{}, false
}

// AgentNames is every backend id, for an error message.
func AgentNames() []string {
	out := make([]string, 0, len(Backends))
	for _, b := range Backends {
		out = append(out, b.ID)
	}
	return out
}

// Efforts is every level any model here accepts, weakest first.
var Efforts = anthropicEfforts

// Choice is what a turn actually runs on. Every field is resolved: Agent and Model are
// never empty, and Effort is empty only when nothing anywhere named one, which means the
// model's own default.
type Choice struct {
	Agent  string
	Model  string
	Effort string
}

// Override is a per-turn choice of backend, model and effort. Every field is optional and
// an empty one means "whatever the level below says" — the skill, then the profile.
//
// It exists so that "which job" and "what runs it" are two decisions rather than one. A
// skill's model is a default; without an override the only way to run one skill on another
// model is a second skill that differs by a single field.
type Override struct {
	Agent  string
	Model  string
	Effort string
}

// Empty reports whether this override asks for nothing.
func (o Override) Empty() bool { return o.Agent == "" && o.Model == "" && o.Effort == "" }

// ValidateOverride holds a per-turn override to the same catalogue a skill is held to. It
// is the API boundary's check: a level the chosen model does not accept is refused when it
// is asked for, not when the turn fails.
//
// The override is checked on its own terms, so a caller that names only an effort is checked
// against the model it will inherit — which is why Resolve runs first and this takes the
// resolved triple.
func ValidateOverride(c Choice) error { return validateTriple(c.Agent, c.Model, c.Effort) }

// validAgent refuses an agent id that names no backend. An empty id means "the default"
// everywhere and is always fine.
func validAgent(agent string) error {
	if strings.TrimSpace(agent) == "" {
		return nil
	}
	if _, ok := FindBackend(agent); !ok {
		return fmt.Errorf("agent %q is not one this conductor runs (have %s)",
			agent, strings.Join(AgentNames(), ", "))
	}
	return nil
}

// validEffort refuses a level no model anywhere accepts. It is the check that can be made
// without knowing which model will run; validateTriple is the one that knows.
func validEffort(effort string) error {
	if strings.TrimSpace(effort) == "" {
		return nil
	}
	for _, e := range Efforts {
		if e == effort {
			return nil
		}
	}
	return fmt.Errorf("effort %q is not a level (have %s)", effort, strings.Join(Efforts, ", "))
}

// validateTriple holds an effective (agent, model, effort) to the catalogue.
//
// The strictness is uneven on purpose. An unknown AGENT is refused: it names a harness that
// does not exist, so nothing could run it. An unknown MODEL is accepted: providers ship
// models faster than this binary is rebuilt, and refusing one would make Podium the reason
// a new model cannot be used. An EFFORT is only checked against a model the catalogue
// knows — for an unknown model there is nothing to check it against, so it is passed
// through and the provider gets to be the one that refuses it.
func validateTriple(agent, model, effort string) error {
	if err := validAgent(agent); err != nil {
		return err
	}
	if err := validEffort(effort); err != nil {
		return err
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil
	}
	b, _ := FindBackend(agent)
	m, known := b.FindModel(strings.TrimSpace(model))
	if !known {
		return nil
	}
	if len(m.Efforts) == 0 {
		return fmt.Errorf("%s takes no effort setting", m.ID)
	}
	for _, e := range m.Efforts {
		if e == effort {
			return nil
		}
	}
	return fmt.Errorf("effort %q is not one %s accepts (have %s)",
		effort, m.ID, strings.Join(m.Efforts, ", "))
}
