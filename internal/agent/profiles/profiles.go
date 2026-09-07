// Package profiles is the bot's identity and its playbooks: what a playbook is, how one is
// validated, and how the directory on disk (PODIUM_AGENT_PROFILE_DIR) merges with the
// playbooks an operator created in the web UI.
//
// A playbook declares which image a turn runs, which prompt it is given, which tools it may
// use and which stored secrets it names. Naming a secret here is not a privilege: a task
// spec names secrets the same way and nothing authorises which names a caller may use —
// see docs/security.md. What a playbook file does is decide what THIS bot hands a turn.
package profiles

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/alvaroibarguen/podium/internal/agent/skills"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// NameRE constrains a profile name and a playbook name. A playbook name also has to survive being
// typed after a slash in Slack.
var NameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// PlaybookPrefixRE matches a leading /playbook on a mention. It is anchored at the very start and
// the name must be followed by whitespace or the end of the text, so "/etc/hosts" is not a
// playbook selector.
var PlaybookPrefixRE = regexp.MustCompile(`^/([a-z][a-z0-9-]{0,31})(\s+|$)`)

// AnthropicKeySecret is the reserved secret the conductor attaches to a Claude turn itself.
// A playbook may not name it: the whole point of the reservation is that no playbook file decides
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
// this host: no turn is ever handed it, and no playbook may name it.
const XAIRefreshSecret = "podium.agent.xai_refresh_token"

// MemoryKeySecret is the other reserved secret the conductor attaches itself: the shared
// memory's API key. A playbook may not name it and a playbook cannot opt out of memory — only the
// operator can, by leaving PODIUM_AGENT_MEMORY_URL empty.
const MemoryKeySecret = "podium.agent.memory_api_key"

// MemoryKeyEnv is where that secret lands in the task container. The brief's
// memory.api_key_env names it, and the runtime reads it to authenticate its MCP client.
const MemoryKeyEnv = "PODIUM_MEMORY_API_KEY"

// BriefEnv is the env var the brief travels in. A playbook's env: may not set it.
const BriefEnv = "PODIUM_AGENT_TURN"

// DockerHostEnv is what a playbook with `docker: true` gets pointed at its own daemon. A
// playbook without the flag may set it itself — pointing a turn at some other engine is a
// legitimate thing to want, and nothing is attached for it to collide with.
const DockerHostEnv = "DOCKER_HOST"

// Defaults for a playbook.
const (
	DefaultMaxTurns = 50
	DefaultTimeout  = spec.Duration(30 * 60 * 1e9)
)

// filePrefix marks a prompt that lives in its own file, resolved relative to the YAML file
// that names it.
const filePrefix = "file:"

// Profile is the bot: one identity, one agent backend, one model, a set of playbooks.
type Profile struct {
	Name         string `yaml:"name"`
	DisplayName  string `yaml:"display_name"`
	SystemPrompt string `yaml:"system_prompt"`
	Model        string `yaml:"model"`
	// Agent is the backend every playbook runs on unless it names its own. Empty is
	// DefaultAgent, so a profile.yaml written before Grok existed still loads.
	Agent string `yaml:"agent"`
	// Effort is the reasoning effort every playbook runs at unless it names its own. Empty
	// means the model's own default, which is what the provider picks.
	Effort          string `yaml:"effort"`
	DefaultPlaybook string `yaml:"default_playbook"`
	// ChatDefaultPlaybook is the playbook a web-chat message runs when the human has not chosen
	// one. It is a profile decision rather than a page constant: the profile owner decides
	// what the chat is for. Empty falls back to DefaultPlaybook.
	ChatDefaultPlaybook string `yaml:"chat_default_playbook"`

	// Playbooks is every playbooks/*.yaml, keyed by file name without the extension.
	Playbooks map[string]Playbook `yaml:"-"`
	// Dir is where the profile was loaded from.
	Dir string `yaml:"-"`
}

// Repo is a repository a playbook's turns get cloned into /workspace.
type Repo struct {
	Name          string `yaml:"name" json:"name"`
	URL           string `yaml:"url" json:"url"`
	DefaultBranch string `yaml:"default_branch" json:"default_branch"`
}

// Playbook is one job the bot can do: which image, which prompt, which tools, which secrets.
//
// The json tags are the on-disk shape of a stored playbook in the conductor's database. They
// match the yaml keys deliberately: a playbook read out of Postgres and a playbook read out of
// playbooks/<name>.yaml are the same document, so there is one schema to reason about.
type Playbook struct {
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
	// Skills is the Agent Skills a turn of this playbook may use, by name, out of
	// PODIUM_AGENT_SKILLS_DIR on the conductor's host. Nothing is implicit: a playbook that
	// names none gets none, and the harness's own permission map denies every skill it has
	// not been told about — including the ones built into the harness.
	//
	// An Agent Skill is executable content somebody else wrote, and it runs in the turn's
	// container with that turn's credentials. This list is the whole of what decides which
	// ones do. See docs/security.md.
	Skills []string `yaml:"skills" json:"skills,omitempty"`
	// Docker gives the turn a real Docker daemon beside it: the conductor attaches a
	// privileged `dind` sidecar and points DOCKER_HOST at it. A playbook needs this to run a
	// dev stack, `docker compose`, or testcontainers.
	//
	// It only works on a node started with --allow-privileged-sidecars, and Podium places
	// on labels alone, so a playbook that sets this must also carry a label its operator put
	// on those nodes. Getting that wrong fails the turn with a message naming the flag
	// rather than hanging.
	Docker bool `yaml:"docker" json:"docker,omitempty"`

	// Linear marks the one playbook Linear tickets run. Tickets are not chat, so there is no
	// /playbook prefix to route them and no channel to match: the flag is the routing rule.
	// At most one playbook may set it; zero means this bot does not take tickets, which is
	// only a misconfiguration when a Linear API key is also set — and the conductor says
	// so at start-up, where the key is known.
	Linear bool `yaml:"linear" json:"linear,omitempty"`

	// Name is the file name without the extension.
	Name string `yaml:"-" json:"-"`
	// Origin is where this copy of the playbook came from: OriginFile or OriginStored. It is
	// set by the loader and the merge, never by a document.
	Origin string `yaml:"-" json:"-"`
}

// Where a playbook came from.
const (
	// OriginFile is a playbooks/<name>.yaml in the profile directory.
	OriginFile = "file"
	// OriginStored is a playbook an operator created through the API, kept in the conductor's
	// own database.
	OriginStored = "stored"
)

// Load reads profile.yaml and every playbooks/*.yaml under dir. Every decode uses
// KnownFields(true), as pkg/spec.ParseTaskSpec does: a misspelt key is an error naming the
// file, not a field that silently does nothing.
func Load(dir string) (*Profile, error) {
	if err := refusePreRenameLayout(dir); err != nil {
		return nil, err
	}
	profilePath := filepath.Join(dir, "profile.yaml")
	p, err := loadProfileFile(profilePath)
	if err != nil {
		return nil, err
	}
	p.Dir = dir

	playbooks, err := loadPlaybooks(filepath.Join(dir, "playbooks"))
	if err != nil {
		return nil, err
	}
	p.Playbooks = playbooks

	if err := p.validate(profilePath); err != nil {
		return nil, err
	}
	return p, nil
}

// refusePreRenameLayout fails when the profile directory is still laid out the way it was
// before Podium's skills became playbooks: a skills/ and no playbooks/. Nothing further down
// would notice. filepath.Glob over a directory that is not there matches nothing and reports
// no error, so the conductor would come up holding an empty profile — or, on the reload path,
// keep answering from the last one it managed to read — and a bot that has quietly lost every
// job it can do because a directory moved under it is the worst outcome available. It is said
// out loud instead, at the one place that reads the directory.
//
// A directory holding both is a rename in progress: playbooks/ is what counts, and the
// leftover skills/ is the operator's to delete when they are ready.
func refusePreRenameLayout(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "playbooks")); err == nil {
		return nil
	}
	legacy := filepath.Join(dir, "skills")
	if fi, err := os.Stat(legacy); err != nil || !fi.IsDir() {
		return nil
	}
	return fmt.Errorf("%s holds skills/ and no playbooks/: what Podium called a skill is now "+
		"called a playbook, because an Agent Skill is a different thing entirely. Rename %s to "+
		"%s, and profile.yaml's default_skill and chat_default_skill to default_playbook and "+
		"chat_default_playbook", dir, legacy, filepath.Join(dir, "playbooks"))
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

func loadPlaybooks(dir string) (map[string]Playbook, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s holds no playbooks: a profile needs at least one playbooks/<name>.yaml", dir)
	}
	out := make(map[string]Playbook, len(paths))
	for _, path := range paths {
		s, err := loadPlaybookFile(path)
		if err != nil {
			return nil, err
		}
		out[s.Name] = s
	}
	return out, nil
}

func loadPlaybookFile(path string) (Playbook, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !NameRE.MatchString(name) {
		return Playbook{}, fmt.Errorf("%s: playbook name %q must match %s", path, name, NameRE)
	}
	f, err := os.Open(path) //nolint:gosec // the operator's own profile directory
	if err != nil {
		return Playbook{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var s Playbook
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return Playbook{}, fmt.Errorf("%s: empty document", path)
		}
		return Playbook{}, fmt.Errorf("%s: %w", path, err)
	}
	s.Name = name
	s.Origin = OriginFile
	prompt, err := resolvePrompt(path, s.SystemPrompt)
	if err != nil {
		return Playbook{}, err
	}
	s.SystemPrompt = prompt
	s.applyDefaults()
	if err := s.validate(path); err != nil {
		return Playbook{}, err
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

func (s *Playbook) applyDefaults() {
	if s.MaxTurns == 0 {
		s.MaxTurns = DefaultMaxTurns
	}
	if s.Timeout == 0 {
		s.Timeout = DefaultTimeout
	}
}

// validateSkills checks the Agent Skills allow-list against the harness's own naming rule
// and the per-playbook cap. It deliberately does NOT check that the named skill exists:
// the directory it comes from is the conductor's configuration, not the profile's, and a
// playbook file has to be loadable on a machine that has no skills directory at all — a
// test, a `podium agent` on a laptop, CI. A name with nothing behind it fails the turn that
// wants it, naming the directory, and leaves every other playbook running.
func (s Playbook) validateSkills() []error {
	var errs []error
	if len(s.Skills) > skills.MaxSkills {
		errs = append(errs, fmt.Errorf("skills names %d skills; the limit is %d",
			len(s.Skills), skills.MaxSkills))
	}
	seen := make(map[string]bool, len(s.Skills))
	for _, name := range s.Skills {
		if err := skills.ValidateName(name); err != nil {
			errs = append(errs, fmt.Errorf("skills: %w", err))
			continue
		}
		if seen[name] {
			errs = append(errs, fmt.Errorf("skills names %q twice", name))
		}
		seen[name] = true
	}
	return errs
}

func (s Playbook) validate(path string) error {
	var errs []error
	if strings.TrimSpace(s.Image) == "" {
		errs = append(errs, errors.New("image is required"))
	}
	if len(s.AllowedTools) == 0 {
		errs = append(errs, errors.New("allowed_tools is required and must name at least one tool"))
	}
	for _, tool := range s.AllowedTools {
		switch {
		case strings.TrimSpace(tool) == "":
			errs = append(errs, errors.New("allowed_tools holds an empty entry"))
		case !validTool(tool):
			// Loud on purpose. The harness changed and so did the tool names; a playbook
			// carrying the old ones would otherwise run with that tool silently absent.
			errs = append(errs, fmt.Errorf(
				"allowed_tools names %q, which is not a tool this harness has (have %s)",
				tool, strings.Join(Tools, ", ")))
		}
	}
	if s.MaxTurns < 1 {
		errs = append(errs, fmt.Errorf("max_turns must be at least 1, got %d", s.MaxTurns))
	}
	if s.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("timeout must be positive, got %s", s.Timeout))
	}
	// A playbook's own triple, checked with its own model. When the playbook names no model the
	// effective one is the profile's, and Profile.validate re-checks it there.
	if err := validateTriple(s.Agent, s.Model, s.Effort); err != nil {
		errs = append(errs, err)
	}
	for _, ref := range s.Secrets {
		switch ref.Name {
		case AnthropicKeySecret, XAIKeySecret, XAIRefreshSecret, MemoryKeySecret:
			errs = append(errs, fmt.Errorf("secrets may not name %s: the conductor decides what "+
				"credential a turn gets, from the agent the playbook runs on", ref.Name))
		}
	}
	errs = append(errs, s.validateSkills()...)
	for key := range s.Env {
		if strings.HasPrefix(key, skills.EnvPrefix) {
			errs = append(errs, fmt.Errorf("env may not set %s: the conductor writes one %s* "+
				"variable per skill it delivers", key, skills.EnvPrefix))
			continue
		}
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
	// The secrets, resources and env of a playbook are validated by exactly the code that
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
	// Every playbook again, this time with the model, agent and effort a turn of it will
	// actually run: a playbook naming an effort its *inherited* model does not accept is
	// exactly as broken as one naming an effort its own model does not, and only here is
	// the combination known.
	for _, name := range p.PlaybookNames() {
		s := p.Playbooks[name]
		if err := validateTriple(p.AgentFor(s), p.ModelFor(s), p.EffortFor(s)); err != nil {
			errs = append(errs, fmt.Errorf("playbook %q: %w", name, err))
		}
	}
	if p.DefaultPlaybook == "" {
		errs = append(errs, errors.New("default_playbook is required"))
	} else if _, ok := p.Playbooks[p.DefaultPlaybook]; !ok {
		errs = append(errs, fmt.Errorf("default_playbook %q names no playbook in playbooks/ (have %s)",
			p.DefaultPlaybook, strings.Join(p.PlaybookNames(), ", ")))
	}
	// An unset chat_default_playbook is fine and means "whatever default_playbook is"; one
	// naming a playbook that is not there is a silent fall-back to a different playbook than the
	// operator asked for, which is worse than a refusal at start-up.
	if p.ChatDefaultPlaybook != "" {
		if _, ok := p.Playbooks[p.ChatDefaultPlaybook]; !ok {
			errs = append(errs, fmt.Errorf("chat_default_playbook %q names no playbook in playbooks/ (have %s)",
				p.ChatDefaultPlaybook, strings.Join(p.PlaybookNames(), ", ")))
		}
	}
	// Two playbooks claiming Linear is ambiguous routing with no tie-breaker at all — there
	// is no channel and no prefix to disambiguate a ticket — so it is refused at load.
	var linear []string
	for _, name := range p.PlaybookNames() {
		if p.Playbooks[name].Linear {
			linear = append(linear, name)
		}
	}
	if len(linear) > 1 {
		errs = append(errs, fmt.Errorf("playbooks %s all set linear: true; exactly one playbook may, "+
			"because a ticket has no channel and no /playbook prefix to choose with",
			strings.Join(linear, ", ")))
	}
	// Two playbooks claiming one channel is ambiguous routing, and ambiguous routing that
	// resolves by map iteration order is worse than a refusal at start-up.
	claimed := map[string]string{}
	for _, name := range p.PlaybookNames() {
		for _, ch := range p.Playbooks[name].SlackChannels {
			if other, ok := claimed[ch]; ok {
				errs = append(errs, fmt.Errorf("playbooks %q and %q both claim slack channel %s", other, name, ch))
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

// LinearPlaybook is the name of the playbook Linear tickets run, or "" when no playbook claims
// them. validate has already refused more than one.
func (p *Profile) LinearPlaybook() string {
	for _, name := range p.PlaybookNames() {
		if p.Playbooks[name].Linear {
			return name
		}
	}
	return ""
}

// ChatPlaybook is the playbook a web-chat message runs when nothing else picks one: the
// profile's chat_default_playbook, or default_playbook when it is unset. validate has already
// refused a name that is not there.
func (p *Profile) ChatPlaybook() string {
	if p.ChatDefaultPlaybook != "" {
		return p.ChatDefaultPlaybook
	}
	return p.DefaultPlaybook
}

// PlaybookNames is every loaded playbook, sorted.
func (p *Profile) PlaybookNames() []string {
	out := make([]string, 0, len(p.Playbooks))
	for name := range p.Playbooks {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Selection is what Select decided.
type Selection struct {
	// Playbook is the playbook that will run the turn.
	Playbook Playbook
	// Instruction is the triggering text with a /playbook prefix stripped.
	Instruction string
	// Explicit is true when the caller named the playbook — a /playbook prefix, or a source that
	// chose one itself. An explicit playbook that disagrees with an existing session's playbook is
	// refused rather than honoured: one session, one playbook. A default is never explicit.
	Explicit bool
}

// Routing is what Select decides from. Playbook and DefaultPlaybook are the two different things
// a source can say about playbooks, and keeping them apart is the whole of the ordering below:
// one is knowledge and the other is a fallback.
type Routing struct {
	// Playbook is a playbook the source KNOWS is right, and which no routing rule may
	// second-guess: Linear's linear: true playbook, and the web chat's playbook chip. Empty means
	// the rules decide. Slack always leaves it empty.
	Playbook string
	// DefaultPlaybook is what the source falls back to when nothing more specific picks one:
	// the web chat's chat_default_playbook. It is a preference, not knowledge, so a human
	// typing /playbook overrides it — and it still beats the profile's own default_playbook.
	DefaultPlaybook string
	// Channel is the routing key matched against a playbook's slack_channels.
	Channel string
	// Text is what the human said, a /playbook prefix included.
	Text string
}

// Select applies the routing rules in order: a playbook the source knows, then a leading
// /playbook the human typed, then the channel's claim, then the source's own default, then
// profile.default_playbook. An unknown /name is deliberately not an error — somebody typing
// /shrug must not break the bot — it is left in the text and falls through, and so does a
// default naming a playbook that is not loaded.
func (p *Profile) Select(r Routing) Selection {
	if r.Playbook != "" {
		if s, ok := p.Playbooks[r.Playbook]; ok {
			return Selection{Playbook: s, Instruction: strings.TrimSpace(r.Text), Explicit: true}
		}
	}
	if m := PlaybookPrefixRE.FindStringSubmatch(r.Text); m != nil {
		if s, ok := p.Playbooks[m[1]]; ok {
			return Selection{
				Playbook:    s,
				Instruction: strings.TrimSpace(r.Text[len(m[0]):]),
				Explicit:    true,
			}
		}
	}
	instruction := strings.TrimSpace(r.Text)
	if r.Channel != "" {
		for _, name := range p.PlaybookNames() {
			for _, ch := range p.Playbooks[name].SlackChannels {
				if ch == r.Channel {
					return Selection{Playbook: p.Playbooks[name], Instruction: instruction}
				}
			}
		}
	}
	if r.DefaultPlaybook != "" {
		if s, ok := p.Playbooks[r.DefaultPlaybook]; ok {
			return Selection{Playbook: s, Instruction: instruction}
		}
	}
	return Selection{Playbook: p.Playbooks[p.DefaultPlaybook], Instruction: instruction}
}

// ModelFor is the model a playbook runs on: its own if it named one, the profile's otherwise.
func (p *Profile) ModelFor(s Playbook) string {
	if s.Model != "" {
		return s.Model
	}
	return p.Model
}

// Resolve is what a turn actually runs on: the override, then the playbook, then the profile,
// then the built-in default. It is the ONE place that ordering lives, so the conductor, the
// brief and the credential the turn is handed can never disagree about it.
func (p *Profile) Resolve(s Playbook, o Override) Choice {
	c := Choice{Agent: p.AgentFor(s), Model: p.ModelFor(s), Effort: p.EffortFor(s)}
	if o.Agent != "" {
		c.Agent = o.Agent
		// A backend the caller chose without naming a model would otherwise keep the model
		// of the backend it came from — grok-4.6 on Claude — which is a request no provider
		// can serve. The new backend's default is the only sane answer.
		if o.Model == "" {
			if b, ok := FindBackend(o.Agent); ok {
				c.Model = b.DefaultModel
			}
		}
	}
	if o.Model != "" {
		c.Model = o.Model
		// The model moved and the backend did not, so follow the model to its own backend.
		// Picking grok-4.6 means picking Grok; there is no other reading.
		if o.Agent == "" {
			if b, ok := backendOf(o.Model); ok {
				c.Agent = b.ID
			}
		}
	}
	if o.Effort != "" {
		c.Effort = o.Effort
	}
	// An inherited effort the newly chosen model does not accept is dropped rather than
	// carried into a request the provider would refuse. Naming one explicitly is checked by
	// ValidateOverride and refused; inheriting one is not the caller's doing.
	if o.Effort == "" && c.Effort != "" {
		if b, ok := FindBackend(c.Agent); ok {
			if m, known := b.FindModel(c.Model); known && !slices.Contains(m.Efforts, c.Effort) {
				c.Effort = ""
			}
		}
	}
	return c
}

// AgentFor is the backend a playbook runs on: its own, then the profile's, then DefaultAgent.
// It never returns "": a turn always runs on something, and the brief says which.
func (p *Profile) AgentFor(s Playbook) string {
	switch {
	case s.Agent != "":
		return s.Agent
	case p.Agent != "":
		return p.Agent
	default:
		return DefaultAgent
	}
}

// EffortFor is the reasoning effort a playbook runs at, or "" for the model's own default.
//
// Inheriting the profile's level is safe because validateTriple has already refused the
// combination that would make it wrong — a playbook that switches backend and inherits a level
// its new model does not accept fails to load rather than running at a level nobody chose.
func (p *Profile) EffortFor(s Playbook) string {
	if s.Effort != "" {
		return s.Effort
	}
	return p.Effort
}
