// Package profiles is the bot's identity and its skills: what a skill is, how one is
// validated, and how the directory on disk (PODIUM_AGENT_PROFILE_DIR) merges with the
// skills an operator created in the web UI.
//
// A skill declares which image a turn runs, which prompt it is given, which tools it may
// use and which stored secrets it names. Naming a secret here is not a privilege: a task
// spec names secrets the same way and nothing authorises which names a caller may use —
// see docs/security.md. What a skill file does is decide what THIS bot hands a turn.
package profiles

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

// NameRE constrains a profile name and a skill name. A skill name also has to survive being
// typed after a slash in Slack.
var NameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// SkillPrefixRE matches a leading /skill on a mention. It is anchored at the very start and
// the name must be followed by whitespace or the end of the text, so "/etc/hosts" is not a
// skill selector.
var SkillPrefixRE = regexp.MustCompile(`^/([a-z][a-z0-9-]{0,31})(\s+|$)`)

// AnthropicKeySecret is the reserved secret the conductor attaches to a Claude turn itself.
// A skill may not name it: the whole point of the reservation is that no skill file decides
// whether the bot can talk to the model.
const AnthropicKeySecret = "podium.agent.anthropic_api_key"

// AnthropicKeyEnv is where that secret lands in the task container.
const AnthropicKeyEnv = "ANTHROPIC_API_KEY"

// XAIKeySecret is the same reservation for a Grok turn: the xAI credential, which is either
// an API key or the access token of a subscription sign-in. Both are bearer tokens for the
// same endpoint, so there is one secret and not two.
const XAIKeySecret = "podium.agent.xai_api_key"

// XAIKeyEnv is where that secret lands in the task container. It is named for what it holds
// — an xAI credential — and the runtime is what maps it onto the SDK's ANTHROPIC_AUTH_TOKEN
// once it knows the turn is a Grok one.
const XAIKeyEnv = "XAI_API_KEY"

// XAIRefreshSecret is the refresh token of a subscription sign-in. It is stored so a token
// that expires in an hour does not mean a human signs in every hour, and it never leaves
// this host: no turn is ever handed it, and no skill may name it.
const XAIRefreshSecret = "podium.agent.xai_refresh_token"

// MemoryKeySecret is the other reserved secret the conductor attaches itself: the shared
// memory's API key. A skill may not name it and a skill cannot opt out of memory — only the
// operator can, by leaving PODIUM_AGENT_MEMORY_URL empty.
const MemoryKeySecret = "podium.agent.memory_api_key"

// MemoryKeyEnv is where that secret lands in the task container. The brief's
// memory.api_key_env names it, and the runtime reads it to authenticate its MCP client.
const MemoryKeyEnv = "PODIUM_MEMORY_API_KEY"

// BriefEnv is the env var the brief travels in. A skill's env: may not set it.
const BriefEnv = "PODIUM_AGENT_TURN"

// DockerHostEnv is what a skill with `docker: true` gets pointed at its own daemon. A
// skill without the flag may set it itself — pointing a turn at some other engine is a
// legitimate thing to want, and nothing is attached for it to collide with.
const DockerHostEnv = "DOCKER_HOST"

// Defaults for a skill.
const (
	DefaultMaxTurns = 50
	DefaultTimeout  = spec.Duration(30 * 60 * 1e9)
)

// filePrefix marks a prompt that lives in its own file, resolved relative to the YAML file
// that names it.
const filePrefix = "file:"

// Profile is the bot: one identity, one agent backend, one model, a set of skills.
type Profile struct {
	Name         string `yaml:"name"`
	DisplayName  string `yaml:"display_name"`
	SystemPrompt string `yaml:"system_prompt"`
	Model        string `yaml:"model"`
	// Agent is the backend every skill runs on unless it names its own. Empty is
	// DefaultAgent, so a profile.yaml written before Grok existed still loads.
	Agent string `yaml:"agent"`
	// Effort is the reasoning effort every skill runs at unless it names its own. Empty
	// means the model's own default, which is what the provider picks.
	Effort       string `yaml:"effort"`
	DefaultSkill string `yaml:"default_skill"`
	// ChatDefaultSkill is the skill a web-chat message runs when the human has not chosen
	// one. It is a profile decision rather than a page constant: the profile owner decides
	// what the chat is for. Empty falls back to DefaultSkill.
	ChatDefaultSkill string `yaml:"chat_default_skill"`

	// Skills is every skills/*.yaml, keyed by file name without the extension.
	Skills map[string]Skill `yaml:"-"`
	// Dir is where the profile was loaded from.
	Dir string `yaml:"-"`
}

// Repo is a repository a skill's turns get cloned into /workspace.
type Repo struct {
	Name          string `yaml:"name" json:"name"`
	URL           string `yaml:"url" json:"url"`
	DefaultBranch string `yaml:"default_branch" json:"default_branch"`
}

// Skill is one job the bot can do: which image, which prompt, which tools, which secrets.
//
// The json tags are the on-disk shape of a stored skill in the conductor's database. They
// match the yaml keys deliberately: a skill read out of Postgres and a skill read out of
// skills/<name>.yaml are the same document, so there is one schema to reason about.
type Skill struct {
	Image         string            `yaml:"image" json:"image"`
	SystemPrompt  string            `yaml:"system_prompt" json:"system_prompt"`
	AllowedTools  []string          `yaml:"allowed_tools" json:"allowed_tools"`
	MaxTurns      int               `yaml:"max_turns" json:"max_turns"`
	Timeout       spec.Duration     `yaml:"timeout" json:"timeout"`
	Model         string            `yaml:"model" json:"model,omitempty"`
	Agent         string            `yaml:"agent" json:"agent,omitempty"`
	Effort        string            `yaml:"effort" json:"effort,omitempty"`
	Labels        []string          `yaml:"labels" json:"labels,omitempty"`
	Resources     spec.Resources    `yaml:"resources" json:"resources,omitempty"`
	Secrets       []spec.SecretRef  `yaml:"secrets" json:"secrets,omitempty"`
	Repos         []Repo            `yaml:"repos" json:"repos,omitempty"`
	SlackChannels []string          `yaml:"slack_channels" json:"slack_channels,omitempty"`
	Env           map[string]string `yaml:"env" json:"env,omitempty"`
	// Docker gives the turn a real Docker daemon beside it: the conductor attaches a
	// privileged `dind` sidecar and points DOCKER_HOST at it. A skill needs this to run a
	// dev stack, `docker compose`, or testcontainers.
	//
	// It only works on a node started with --allow-privileged-sidecars, and Podium places
	// on labels alone, so a skill that sets this must also carry a label its operator put
	// on those nodes. Getting that wrong fails the turn with a message naming the flag
	// rather than hanging.
	Docker bool `yaml:"docker" json:"docker,omitempty"`

	// Linear marks the one skill Linear tickets run. Tickets are not chat, so there is no
	// /skill prefix to route them and no channel to match: the flag is the routing rule.
	// At most one skill may set it; zero means this bot does not take tickets, which is
	// only a misconfiguration when a Linear API key is also set — and the conductor says
	// so at start-up, where the key is known.
	Linear bool `yaml:"linear" json:"linear,omitempty"`

	// Name is the file name without the extension.
	Name string `yaml:"-" json:"-"`
	// Origin is where this copy of the skill came from: OriginFile or OriginStored. It is
	// set by the loader and the merge, never by a document.
	Origin string `yaml:"-" json:"-"`
}

// Where a skill came from.
const (
	// OriginFile is a skills/<name>.yaml in the profile directory.
	OriginFile = "file"
	// OriginStored is a skill an operator created through the API, kept in the conductor's
	// own database.
	OriginStored = "stored"
)

// Load reads profile.yaml and every skills/*.yaml under dir. Every decode uses
// KnownFields(true), as pkg/spec.ParseTaskSpec does: a misspelt key is an error naming the
// file, not a field that silently does nothing.
func Load(dir string) (*Profile, error) {
	profilePath := filepath.Join(dir, "profile.yaml")
	p, err := loadProfileFile(profilePath)
	if err != nil {
		return nil, err
	}
	p.Dir = dir

	skills, err := loadSkills(filepath.Join(dir, "skills"))
	if err != nil {
		return nil, err
	}
	p.Skills = skills

	if err := p.validate(profilePath); err != nil {
		return nil, err
	}
	return p, nil
}

func loadProfileFile(path string) (*Profile, error) {
	f, err := os.Open(path) //nolint:gosec // the operator's own profile directory
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var p Profile
	if err := dec.Decode(&p); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty document", path)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	prompt, err := resolvePrompt(path, p.SystemPrompt)
	if err != nil {
		return nil, err
	}
	p.SystemPrompt = prompt
	return &p, nil
}

func loadSkills(dir string) (map[string]Skill, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s holds no skills: a profile needs at least one skills/<name>.yaml", dir)
	}
	out := make(map[string]Skill, len(paths))
	for _, path := range paths {
		s, err := loadSkillFile(path)
		if err != nil {
			return nil, err
		}
		out[s.Name] = s
	}
	return out, nil
}

func loadSkillFile(path string) (Skill, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !NameRE.MatchString(name) {
		return Skill{}, fmt.Errorf("%s: skill name %q must match %s", path, name, NameRE)
	}
	f, err := os.Open(path) //nolint:gosec // the operator's own profile directory
	if err != nil {
		return Skill{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var s Skill
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return Skill{}, fmt.Errorf("%s: empty document", path)
		}
		return Skill{}, fmt.Errorf("%s: %w", path, err)
	}
	s.Name = name
	s.Origin = OriginFile
	prompt, err := resolvePrompt(path, s.SystemPrompt)
	if err != nil {
		return Skill{}, err
	}
	s.SystemPrompt = prompt
	s.applyDefaults()
	if err := s.validate(path); err != nil {
		return Skill{}, err
	}
	return s, nil
}

// resolvePrompt reads a `file:` prompt relative to the file that names it, or returns an
// inline string unchanged. Either way the result must be non-empty: a prompt is the whole of
// what the bot is told about itself, and an empty one is always a mistake.
func resolvePrompt(owner, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("%s: system_prompt is required (an inline string, or file:./prompts/x.md)", owner)
	}
	if !strings.HasPrefix(trimmed, filePrefix) {
		return value, nil
	}
	rel := strings.TrimSpace(strings.TrimPrefix(trimmed, filePrefix))
	if rel == "" {
		return "", fmt.Errorf("%s: system_prompt: %q names no file", owner, value)
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(owner), rel)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the operator's own profile directory
	if err != nil {
		return "", fmt.Errorf("%s: system_prompt: %w", owner, err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("%s: system_prompt: %s is empty", owner, path)
	}
	return string(raw), nil
}

func (s *Skill) applyDefaults() {
	if s.MaxTurns == 0 {
		s.MaxTurns = DefaultMaxTurns
	}
	if s.Timeout == 0 {
		s.Timeout = DefaultTimeout
	}
}

func (s Skill) validate(path string) error {
	var errs []error
	if strings.TrimSpace(s.Image) == "" {
		errs = append(errs, errors.New("image is required"))
	}
	if len(s.AllowedTools) == 0 {
		errs = append(errs, errors.New("allowed_tools is required and must name at least one tool"))
	}
	for _, tool := range s.AllowedTools {
		if strings.TrimSpace(tool) == "" {
			errs = append(errs, errors.New("allowed_tools holds an empty entry"))
		}
	}
	if s.MaxTurns < 1 {
		errs = append(errs, fmt.Errorf("max_turns must be at least 1, got %d", s.MaxTurns))
	}
	if s.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("timeout must be positive, got %s", s.Timeout))
	}
	// A skill's own triple, checked with its own model. When the skill names no model the
	// effective one is the profile's, and Profile.validate re-checks it there.
	if err := validateTriple(s.Agent, s.Model, s.Effort); err != nil {
		errs = append(errs, err)
	}
	for _, ref := range s.Secrets {
		switch ref.Name {
		case AnthropicKeySecret, XAIKeySecret, XAIRefreshSecret, MemoryKeySecret:
			errs = append(errs, fmt.Errorf("secrets may not name %s: the conductor decides what "+
				"credential a turn gets, from the agent the skill runs on", ref.Name))
		}
	}
	for key := range s.Env {
		switch key {
		case BriefEnv:
			errs = append(errs, fmt.Errorf("env may not set %s: the conductor writes the turn brief", BriefEnv))
		case AnthropicKeyEnv:
			errs = append(errs, fmt.Errorf("env may not set %s: it comes from the %s secret",
				AnthropicKeyEnv, AnthropicKeySecret))
		case XAIKeyEnv:
			errs = append(errs, fmt.Errorf("env may not set %s: it comes from the %s secret",
				XAIKeyEnv, XAIKeySecret))
		case MemoryKeyEnv:
			errs = append(errs, fmt.Errorf("env may not set %s: it comes from the %s secret",
				MemoryKeyEnv, MemoryKeySecret))
		case DockerHostEnv:
			if s.Docker {
				errs = append(errs, fmt.Errorf("env may not set %s when docker is true: "+
					"the conductor points it at the sidecar it attaches", DockerHostEnv))
			}
		}
	}
	for _, r := range s.Repos {
		switch {
		case strings.TrimSpace(r.Name) == "":
			errs = append(errs, errors.New("repos[].name is required"))
		case strings.TrimSpace(r.URL) == "":
			errs = append(errs, fmt.Errorf("repo %q has no url", r.Name))
		case strings.TrimSpace(r.DefaultBranch) == "":
			errs = append(errs, fmt.Errorf("repo %q has no default_branch", r.Name))
		}
	}
	for _, ch := range s.SlackChannels {
		if strings.TrimSpace(ch) == "" {
			errs = append(errs, errors.New("slack_channels holds an empty entry"))
		}
	}
	// The secrets, resources and env of a skill are validated by exactly the code that
	// validates a task spec's, because that is where they end up.
	probe := spec.TaskSpec{
		Image:     s.Image,
		Env:       s.Env,
		Secrets:   append([]spec.SecretRef(nil), s.Secrets...),
		Resources: s.Resources,
		Labels:    s.Labels,
		Timeout:   s.Timeout,
	}
	probe.ApplyDefaults()
	if err := probe.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (p *Profile) validate(path string) error {
	var errs []error
	if !NameRE.MatchString(p.Name) {
		errs = append(errs, fmt.Errorf("name %q must match %s", p.Name, NameRE))
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		errs = append(errs, errors.New("display_name is required"))
	}
	if strings.TrimSpace(p.Model) == "" {
		errs = append(errs, errors.New("model is required"))
	}
	if err := validateTriple(p.Agent, p.Model, p.Effort); err != nil {
		errs = append(errs, err)
	}
	// Every skill again, this time with the model, agent and effort a turn of it will
	// actually run: a skill naming an effort its *inherited* model does not accept is
	// exactly as broken as one naming an effort its own model does not, and only here is
	// the combination known.
	for _, name := range p.SkillNames() {
		s := p.Skills[name]
		if err := validateTriple(p.AgentFor(s), p.ModelFor(s), p.EffortFor(s)); err != nil {
			errs = append(errs, fmt.Errorf("skill %q: %w", name, err))
		}
	}
	if p.DefaultSkill == "" {
		errs = append(errs, errors.New("default_skill is required"))
	} else if _, ok := p.Skills[p.DefaultSkill]; !ok {
		errs = append(errs, fmt.Errorf("default_skill %q names no skill in skills/ (have %s)",
			p.DefaultSkill, strings.Join(p.SkillNames(), ", ")))
	}
	// An unset chat_default_skill is fine and means "whatever default_skill is"; one
	// naming a skill that is not there is a silent fall-back to a different skill than the
	// operator asked for, which is worse than a refusal at start-up.
	if p.ChatDefaultSkill != "" {
		if _, ok := p.Skills[p.ChatDefaultSkill]; !ok {
			errs = append(errs, fmt.Errorf("chat_default_skill %q names no skill in skills/ (have %s)",
				p.ChatDefaultSkill, strings.Join(p.SkillNames(), ", ")))
		}
	}
	// Two skills claiming Linear is ambiguous routing with no tie-breaker at all — there
	// is no channel and no prefix to disambiguate a ticket — so it is refused at load.
	var linear []string
	for _, name := range p.SkillNames() {
		if p.Skills[name].Linear {
			linear = append(linear, name)
		}
	}
	if len(linear) > 1 {
		errs = append(errs, fmt.Errorf("skills %s all set linear: true; exactly one skill may, "+
			"because a ticket has no channel and no /skill prefix to choose with",
			strings.Join(linear, ", ")))
	}
	// Two skills claiming one channel is ambiguous routing, and ambiguous routing that
	// resolves by map iteration order is worse than a refusal at start-up.
	claimed := map[string]string{}
	for _, name := range p.SkillNames() {
		for _, ch := range p.Skills[name].SlackChannels {
			if other, ok := claimed[ch]; ok {
				errs = append(errs, fmt.Errorf("skills %q and %q both claim slack channel %s", other, name, ch))
				continue
			}
			claimed[ch] = name
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// LinearSkill is the name of the skill Linear tickets run, or "" when no skill claims
// them. validate has already refused more than one.
func (p *Profile) LinearSkill() string {
	for _, name := range p.SkillNames() {
		if p.Skills[name].Linear {
			return name
		}
	}
	return ""
}

// ChatSkill is the skill a web-chat message runs when nothing else picks one: the
// profile's chat_default_skill, or default_skill when it is unset. validate has already
// refused a name that is not there.
func (p *Profile) ChatSkill() string {
	if p.ChatDefaultSkill != "" {
		return p.ChatDefaultSkill
	}
	return p.DefaultSkill
}

// SkillNames is every loaded skill, sorted.
func (p *Profile) SkillNames() []string {
	out := make([]string, 0, len(p.Skills))
	for name := range p.Skills {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Selection is what Select decided.
type Selection struct {
	// Skill is the skill that will run the turn.
	Skill Skill
	// Instruction is the triggering text with a /skill prefix stripped.
	Instruction string
	// Explicit is true when the caller named the skill — a /skill prefix, or a source that
	// chose one itself. An explicit skill that disagrees with an existing session's skill is
	// refused rather than honoured: one session, one skill. A default is never explicit.
	Explicit bool
}

// Routing is what Select decides from. Skill and DefaultSkill are the two different things
// a source can say about skills, and keeping them apart is the whole of the ordering below:
// one is knowledge and the other is a fallback.
type Routing struct {
	// Skill is a skill the source KNOWS is right, and which no routing rule may
	// second-guess: Linear's linear: true skill, and the web chat's skill chip. Empty means
	// the rules decide. Slack always leaves it empty.
	Skill string
	// DefaultSkill is what the source falls back to when nothing more specific picks one:
	// the web chat's chat_default_skill. It is a preference, not knowledge, so a human
	// typing /skill overrides it — and it still beats the profile's own default_skill.
	DefaultSkill string
	// Channel is the routing key matched against a skill's slack_channels.
	Channel string
	// Text is what the human said, a /skill prefix included.
	Text string
}

// Select applies the routing rules in order: a skill the source knows, then a leading
// /skill the human typed, then the channel's claim, then the source's own default, then
// profile.default_skill. An unknown /name is deliberately not an error — somebody typing
// /shrug must not break the bot — it is left in the text and falls through, and so does a
// default naming a skill that is not loaded.
func (p *Profile) Select(r Routing) Selection {
	if r.Skill != "" {
		if s, ok := p.Skills[r.Skill]; ok {
			return Selection{Skill: s, Instruction: strings.TrimSpace(r.Text), Explicit: true}
		}
	}
	if m := SkillPrefixRE.FindStringSubmatch(r.Text); m != nil {
		if s, ok := p.Skills[m[1]]; ok {
			return Selection{
				Skill:       s,
				Instruction: strings.TrimSpace(r.Text[len(m[0]):]),
				Explicit:    true,
			}
		}
	}
	instruction := strings.TrimSpace(r.Text)
	if r.Channel != "" {
		for _, name := range p.SkillNames() {
			for _, ch := range p.Skills[name].SlackChannels {
				if ch == r.Channel {
					return Selection{Skill: p.Skills[name], Instruction: instruction}
				}
			}
		}
	}
	if r.DefaultSkill != "" {
		if s, ok := p.Skills[r.DefaultSkill]; ok {
			return Selection{Skill: s, Instruction: instruction}
		}
	}
	return Selection{Skill: p.Skills[p.DefaultSkill], Instruction: instruction}
}

// ModelFor is the model a skill runs on: its own if it named one, the profile's otherwise.
func (p *Profile) ModelFor(s Skill) string {
	if s.Model != "" {
		return s.Model
	}
	return p.Model
}

// AgentFor is the backend a skill runs on: its own, then the profile's, then DefaultAgent.
// It never returns "": a turn always runs on something, and the brief says which.
func (p *Profile) AgentFor(s Skill) string {
	switch {
	case s.Agent != "":
		return s.Agent
	case p.Agent != "":
		return p.Agent
	default:
		return DefaultAgent
	}
}

// EffortFor is the reasoning effort a skill runs at, or "" for the model's own default.
//
// Inheriting the profile's level is safe because validateTriple has already refused the
// combination that would make it wrong — a skill that switches backend and inherits a level
// its new model does not accept fails to load rather than running at a level nobody chose.
func (p *Profile) EffortFor(s Skill) string {
	if s.Effort != "" {
		return s.Effort
	}
	return p.Effort
}
