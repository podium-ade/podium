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
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/skills"
	"github.com/podium-ade/podium/internal/version"
	"github.com/podium-ade/podium/pkg/spec"
)

// The range a playbook's priority may sit in. The bound is a guard against a typo rather
// than a scale with meaning: the queue is sorted, so only the ORDER of these numbers matters
// and a thousand steps either side of the default is more than any fleet can distinguish.
const (
	MinPriority = -1000
	MaxPriority = 1000
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

// GitCapabilityPrefix is the reserved prefix of the per-turn secret that carries a turn's
// authority to mint a GitHub token. The conductor writes one before it creates the task and
// deletes it when the turn ends, so the name is a turn id and never an operator's choice.
//
// A playbook may not name one: the capability is scoped to the repositories the conductor
// signed into it, and a playbook that could name another turn's would be a playbook with
// that turn's repositories. See internal/agent/conductor/gitcred.go.
const GitCapabilityPrefix = "podium.agent.git_capability."

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

// DefaultAssistantTimeout bounds one assistant turn when profile.yaml names no timeout.
//
// It exists because NOTHING else bounds one: the assistant has no container and, unlike a
// playbook, no step cap by default. A wall clock is the right shape for that gap — a step
// cap fires mid-answer on a turn that is working, which is the bug that took the cap away,
// while a clock only fires on a turn that is genuinely stuck.
//
// Fifteen minutes is generous by two orders of magnitude: a turn that answers or delegates
// takes seconds. There is deliberately no way to switch it off, because "off" is the state
// this constant exists to stop being the default.
const DefaultAssistantTimeout = spec.Duration(15 * 60 * 1e9)

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
	// Git is who every playbook's turns commit as unless the playbook names its own. It is
	// the DEFAULT and not the rule: the identity has to match the account behind the token
	// that pushes, and the GitHub token is a per-playbook secret, so a second playbook
	// pushing with a second token needs a persona of its own.
	Git GitPersona `yaml:"git"`
	// Skills is the Agent Skills the ASSISTANT may use — the turn the conductor answers a
	// conversation with, on this host. A playbook names its own; this is the other end of
	// that list and not a default for it, because the two turns are nothing alike: one has
	// a container and a workspace, and this one has a conversation.
	Skills []string `yaml:"skills"`
	// Timeout bounds one assistant turn on the wall clock. Zero is DefaultAssistantTimeout;
	// unlike MaxTurns there is no "off", because a turn with neither bound has no automatic
	// stop at all.
	Timeout spec.Duration `yaml:"timeout"`
	// MaxTurns caps one assistant turn's steps. UNSET MEANS NO CAP, which is the opposite of
	// a playbook's max_turns and deliberately so: the assistant answers a conversation and
	// delegates, so what is worth bounding is the container it starts rather than the relay
	// that started it, and a cap that fires mid-answer says "I ran out of turns" about a turn
	// that had not failed. An operator who wants a ceiling sets one.
	MaxTurns int `yaml:"max_turns"`

	// Playbooks is every playbooks/*.yaml, keyed by file name without the extension.
	Playbooks map[string]Playbook `yaml:"-"`
	// Dir is where the profile was loaded from.
	Dir string `yaml:"-"`
}

// Assistant is the bot as a CONVERSATION meets it: the turn the conductor runs itself, in
// its own process, with no container around it.
//
// It is NOT a playbook and deliberately has no fields in common with one. A playbook is a
// machine job — an image, a workspace, a Docker daemon, a repository — and the assistant has
// none of those and can never be given them: it runs on the operator's own host, beside the
// master key. What it has is this conversation, a short fixed tool list, memory, and the
// playbooks it may hand work to.
//
// A conversation used to borrow a playbook for its prompt and its model and then have every
// container-shaped field taken away again by the fence. That left one word meaning two
// things, and a chat asking a human to choose a container it would never run in.
// Its prompt is deliberately not here: the assistant IS the profile, so SystemPrompt is
// already what a turn is told about itself, and a copy in a second field would be one of
// them going stale.
type Assistant struct {
	// Skills is the Agent Skills it may use, by name.
	Skills []string
	// MaxTurns caps its steps, and ZERO means no cap: a step cap fires mid-answer on a turn
	// that is working, which is why it is off unless somebody asks for it.
	MaxTurns int
	// Timeout is the wall clock that bounds a turn instead, always positive. It is what
	// stops a stuck turn running for ever on the conductor's own machine.
	Timeout time.Duration
}

// Assistant is what answers a conversation. Every field comes from profile.yaml itself:
// there is no playbook in this path and nothing to select.
func (p *Profile) Assistant() Assistant {
	return Assistant{
		Skills:   append([]string(nil), p.Skills...),
		MaxTurns: p.MaxTurns,
		Timeout:  p.assistantTimeout().Std(),
	}
}

// assistantTimeout is the wall clock, defaulted. Unlike a playbook's it is not applied at
// load: a profile is also written by the API's merge, and defaulting in one path and not the
// other is how two copies of the same document drift.
func (p *Profile) assistantTimeout() spec.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return DefaultAssistantTimeout
}

// Repo is a repository a playbook's turns get cloned into /workspace.
type Repo struct {
	Name          string `yaml:"name" json:"name"`
	URL           string `yaml:"url" json:"url"`
	DefaultBranch string `yaml:"default_branch" json:"default_branch"`
}

// GitPersona is who a turn's commits are BY: the user.name and user.email the runtime writes
// into every clone it makes.
//
// It has to be an identity GitHub can resolve to an ACCOUNT, and that is a stronger
// requirement than "a well-formed address". GitHub links a commit to an account by this
// email; an @users.noreply.github.com address belonging to no account — which is what the
// runtime's own fallback is — leaves every commit attributed to nobody, and a deployment
// gate that checks the author's access refuses the pull request. The account whose token the
// playbook pushes with is the right answer, as <id>+<login>@users.noreply.github.com.
//
// Empty is allowed and means the runtime's fallback, because a profile written before this
// field existed must still load.
type GitPersona struct {
	Name  string `yaml:"name" json:"name"`
	Email string `yaml:"email" json:"email"`
}

// Set reports whether this persona says anything at all. validate has already refused a
// half-written one, so a persona that is Set always has both halves.
func (g GitPersona) Set() bool { return g != GitPersona{} }

func (g GitPersona) trim() GitPersona {
	return GitPersona{Name: strings.TrimSpace(g.Name), Email: strings.TrimSpace(g.Email)}
}

// validate refuses a persona git could not write and one written only half-way.
func (g GitPersona) validate() []error {
	if !g.Set() {
		return nil
	}
	var errs []error
	if g.Name == "" || g.Email == "" {
		errs = append(errs, errors.New("git needs both name and email, or neither"))
	}
	for _, f := range []struct{ key, value string }{{"name", g.Name}, {"email", g.Email}} {
		if strings.ContainsAny(f.value, "<>\n") {
			errs = append(errs, fmt.Errorf("git %s %q may not hold <, > or a newline: "+
				"git writes the two into a commit header as `Name <email>`", f.key, f.value))
		}
	}
	if g.Email != "" && !strings.Contains(g.Email, "@") {
		errs = append(errs, fmt.Errorf("git email %q is not an address: GitHub links a commit "+
			"to an account by it, so an address that resolves to none attributes the commit "+
			"to nobody", g.Email))
	}
	return errs
}

// GitEnv is the environment git takes an identity from. Every one of these OVERRIDES
// user.name and user.email, which is why a playbook may not set one alongside a persona:
// there would be two answers and the quieter one would win.
var GitEnv = []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"}

func gitEnvConflicts(env map[string]string, g GitPersona) []error {
	if !g.Set() {
		return nil
	}
	var errs []error
	for _, key := range GitEnv {
		if _, ok := env[key]; ok {
			errs = append(errs, fmt.Errorf("env may not set %s alongside git: it overrides "+
				"user.name and user.email, so the persona would be written and then ignored", key))
		}
	}
	return errs
}

// Playbook is one job the bot can do: which image, which prompt, which tools, which secrets.
//
// The json tags are the on-disk shape of a stored playbook in the conductor's database. They
// match the yaml keys deliberately: a playbook read out of Postgres and a playbook read out of
// playbooks/<name>.yaml are the same document, so there is one schema to reason about.
type Playbook struct {
	Image        string        `yaml:"image" json:"image"`
	SystemPrompt string        `yaml:"system_prompt" json:"system_prompt"`
	AllowedTools []string      `yaml:"allowed_tools" json:"allowed_tools"`
	MaxTurns     int           `yaml:"max_turns" json:"max_turns"`
	Timeout      spec.Duration `yaml:"timeout" json:"timeout"`
	Model        string        `yaml:"model" json:"model,omitempty"`
	Agent        string        `yaml:"agent" json:"agent,omitempty"`
	Effort       string        `yaml:"effort" json:"effort,omitempty"`
	Labels       []string      `yaml:"labels" json:"labels,omitempty"`
	// Priority is where a turn of this playbook goes in Podium's queue: the scheduler
	// claims higher first and breaks ties by age. Zero is the default and negative is
	// allowed, so a playbook that grinds for two hours can be told to wait behind
	// everything somebody is watching.
	//
	// It is a sort key and not a budget: it changes what runs next when the fleet is full,
	// and nothing at all about what a turn is given or how long it may take.
	Priority  int              `yaml:"priority" json:"priority,omitempty"`
	Resources spec.Resources   `yaml:"resources" json:"resources,omitempty"`
	Secrets   []spec.SecretRef `yaml:"secrets" json:"secrets,omitempty"`
	Repos     []Repo           `yaml:"repos" json:"repos,omitempty"`
	// Git is who this playbook's turns commit as: it overrides the profile's, and unset
	// inherits it. Unset in both leaves the runtime's fallback, which belongs to no GitHub
	// account — see GitPersona for why that is a deployment failure rather than a cosmetic one.
	Git           GitPersona        `yaml:"git" json:"git,omitzero"`
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
	// MCPServers is the MCP servers a turn of this playbook may use, by name, out of the
	// registry an operator manages in the web UI. Nothing is implicit here either: a
	// playbook that names none gets none, and the runtime writes the harness exactly the
	// entries the brief carried.
	//
	// A server is somebody else's API with a credential attached, and the tools it exposes
	// run with whatever that credential can do. This list is the whole of what decides
	// which turns get which of them. See docs/security.md.
	MCPServers []string `yaml:"mcp_servers" json:"mcp_servers,omitempty"`
	// Docker gives the turn a real Docker daemon beside it: the conductor attaches a
	// privileged `dind` sidecar and points DOCKER_HOST at it. A playbook needs this to run a
	// dev stack, `docker compose`, or testcontainers.
	//
	// It only works on a node started with --allow-privileged-sidecars, and Podium places
	// on labels alone, so a playbook that sets this must also carry a label its operator put
	// on those nodes. Getting that wrong fails the turn with a message naming the flag
	// rather than hanging.
	Docker bool `yaml:"docker" json:"docker,omitempty"`

	// Browser gives the turn a headless Chrome of its own and the tools to drive it: the
	// conductor adds a browser sidecar and the runtime points an MCP server at it, so the
	// agent navigates, clicks and screenshots rather than shelling out to curl.
	//
	// The browser is a SIDECAR and not something in the image, which is what keeps it
	// isolated: its own container, its own profile, its own network namespace, thrown away
	// with the task. It needs no privilege — unlike `docker`, this asks nothing of the node
	// beyond an ordinary container, so it carries no label requirement.
	Browser bool `yaml:"browser" json:"browser,omitempty"`

	// Linear marks the one playbook Linear tickets run. Tickets are not chat, so there is no
	// /playbook prefix to route them and no channel to match: the flag is the routing rule.
	// At most one playbook may set it; zero means this bot does not take tickets, which is
	// only a misconfiguration when a Linear API key is also set — and the conductor says
	// so at start-up, where the key is known.
	Linear bool `yaml:"linear" json:"linear,omitempty"`

	// Interactive lets a turn ask a human a question and wait for the answer in the same
	// container, instead of ending the turn. Off by default: a waiting container still
	// holds a node slot (and a dind sidecar, if the playbook asked for one).
	Interactive bool `yaml:"interactive" json:"interactive,omitempty"`

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
		"%s and profile.yaml's default_skill to default_playbook. A chat_default_skill or "+
		"chat_default_playbook has no replacement: a conversation is answered by the assistant, "+
		"which profile.yaml itself describes, and it runs no playbook",
		dir, legacy, filepath.Join(dir, "playbooks"))
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
	p.Git = p.Git.trim()
	return &p, nil
}

func loadPlaybooks(dir string) (map[string]Playbook, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	sort.Strings(paths)
	// Zero files is a fresh install: the assistant still answers chat, and playbooks are
	// created in the UI (or bind-mounted later). A mention with nothing to route to is
	// refused at select time, not at boot.
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

// RuntimeImageRepo is where the published agent runtime images live. The repository name is
// fixed; the tag is this build's own version.
const RuntimeImageRepo = "ghcr.io/podium-ade/podium-agent-runtime"

// LocalRuntimeImage is the tag `make agent-runtime` writes. A node runs tasks on its own
// Docker engine, so a local tag is visible to a task without any registry — which is exactly
// what a development stack wants and exactly what a fleet cannot use.
const LocalRuntimeImage = "podium-agent-runtime:dev"

// releaseVersion matches a version that came from a v* tag, which is the only kind that has a
// published runtime image behind it. GoReleaser sets internal/version.Version to the tag
// without its leading v, so a release reads `0.1.0` or `0.1.0-rc.1`.
//
// It has to be this rather than `!= "dev"`: the Makefile stamps `git describe --tags --always
// --dirty`, so an ordinary local build carries a short commit like `04d190f`, and one made
// after a tag carries `v0.1.0-5-gabc123`. Neither has an image published under it, and both
// would otherwise send a node looking for one.
var releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+`)

// DefaultRuntimeImage is the image a playbook that names none will run in: the runtime
// published alongside THIS build, so a conductor and its runtime are a matched pair by
// construction rather than by whoever last edited a tag into a YAML file.
//
// It is why the playbooks Podium ships name no image. `latest` drifts out from under a pinned
// conductor, and a hard-coded version has to be edited every release — a promise to remember
// something, and those are the ones that rot.
func DefaultRuntimeImage() string {
	if releaseVersion.MatchString(version.Version) {
		return RuntimeImageRepo + ":" + version.Version
	}
	return LocalRuntimeImage
}

func (s *Playbook) applyDefaults() {
	if s.MaxTurns == 0 {
		s.MaxTurns = DefaultMaxTurns
	}
	if s.Timeout == 0 {
		s.Timeout = DefaultTimeout
	}
	if strings.TrimSpace(s.Image) == "" {
		s.Image = DefaultRuntimeImage()
	}
	s.Git = s.Git.trim()
}

// validateSkills checks an Agent Skills allow-list against the harness's own naming rule and
// the cap. Both lists there are go through it: a playbook's and the assistant's.
//
// It deliberately does NOT check that the named skill exists: the directory it comes from is
// the conductor's configuration, not the profile's, and a profile has to be loadable on a
// machine that has no skills directory at all — a test, a `podium agent` on a laptop, CI. A
// name with nothing behind it fails the turn that wants it, naming the directory, and leaves
// everything else running.
func validateSkills(names []string) []error {
	var errs []error
	if len(names) > skills.MaxSkills {
		errs = append(errs, fmt.Errorf("skills names %d skills; the limit is %d",
			len(names), skills.MaxSkills))
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
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

// validateMCPServers holds an mcp_servers list to the same rules validateSkills holds a
// skills list to, and for the same reason: a name that is not a name at all is a turn that
// fails on its harness config, and finding that out from a turn is worse than finding it out
// on save. Every list goes through it.
//
// Whether a named server EXISTS is deliberately not checked, exactly as a skill's is not. The
// registry is the conductor's own database, and a profile has to be loadable on a machine
// with no database at all — a test, CI, a laptop. A name with nothing behind it fails the
// turn that wants it, naming the server, and leaves everything else running.
func validateMCPServers(names []string) []error {
	var errs []error
	if len(names) > mcp.MaxServers {
		errs = append(errs, fmt.Errorf("mcp_servers names %d servers; the limit is %d",
			len(names), mcp.MaxServers))
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if err := mcp.ValidateName(name); err != nil {
			errs = append(errs, fmt.Errorf("mcp_servers: %w", err))
			continue
		}
		if seen[name] {
			errs = append(errs, fmt.Errorf("mcp_servers names %q twice", name))
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
	if s.Priority < MinPriority || s.Priority > MaxPriority {
		errs = append(errs, fmt.Errorf("priority must be between %d and %d, got %d",
			MinPriority, MaxPriority, s.Priority))
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
		if strings.HasPrefix(ref.Name, GitCapabilityPrefix) {
			errs = append(errs, fmt.Errorf("secrets may not name %s: a turn's authority to mint "+
				"a GitHub token is written by the conductor, for one turn, and is scoped to the "+
				"repositories that turn's playbook listed", ref.Name))
		}
		if strings.HasPrefix(ref.Name, mcp.SecretPrefix) {
			// The registry is what grants an MCP server to a playbook, and the token is
			// how a turn uses one. A playbook that could name the secret directly would
			// have the credential of a server it was never granted.
			errs = append(errs, fmt.Errorf("secrets may not name %s: an MCP server's token "+
				"comes from mcp_servers, which is what decides whether this playbook has "+
				"that server at all", ref.Name))
		}
	}
	errs = append(errs, validateSkills(s.Skills)...)
	errs = append(errs, validateMCPServers(s.MCPServers)...)
	errs = append(errs, s.Git.validate()...)
	errs = append(errs, gitEnvConflicts(s.Env, s.Git)...)
	for key := range s.Env {
		if strings.HasPrefix(key, skills.EnvPrefix) {
			errs = append(errs, fmt.Errorf("env may not set %s: the conductor writes one %s* "+
				"variable per skill it delivers", key, skills.EnvPrefix))
			continue
		}
		if strings.HasPrefix(key, mcp.EnvPrefix) {
			errs = append(errs, fmt.Errorf("env may not set %s: the conductor writes one %s* "+
				"variable per MCP server it delivers", key, mcp.EnvPrefix))
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
	// default_playbook is a routing hint, not a boot requirement. A fresh install has no
	// playbooks yet; a name that is not loaded is the same as empty — Select returns no
	// playbook and the mention is refused then, with the profile still running.
	// The assistant's own two fields. An absent max_turns and a `max_turns: 0` are the same
	// document to a YAML decoder, and both mean no cap; a negative one is the only shape
	// that can be refused, and it is.
	errs = append(errs, validateSkills(p.Skills)...)
	errs = append(errs, p.Git.validate()...)
	// The profile's persona against the env of every playbook that INHERITS it. A playbook
	// with its own has already been checked against that one by Playbook.validate, and
	// checking it twice would report the same line twice.
	for _, name := range p.PlaybookNames() {
		s := p.Playbooks[name]
		if s.Git.Set() {
			continue
		}
		for _, err := range gitEnvConflicts(s.Env, p.Git) {
			errs = append(errs, fmt.Errorf("playbook %q: %w", name, err))
		}
	}
	if p.MaxTurns < 0 {
		errs = append(errs, fmt.Errorf("max_turns must be at least 1, got %d", p.MaxTurns))
	}
	if p.Timeout < 0 {
		errs = append(errs, fmt.Errorf("timeout must be positive, got %s", p.Timeout))
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

// Routing is what Select decides from.
//
// It only ever describes a TASK: a Slack thread or a Linear ticket, which are one piece of
// work and run one playbook in a container. A conversation does not appear here at all — it
// is answered by the assistant, and the playbooks are what that turn delegates to rather
// than something a routing rule picks for it.
type Routing struct {
	// Playbook is a playbook the source KNOWS is right, and which no routing rule may
	// second-guess: Linear's linear: true playbook. Empty means the rules decide. Slack
	// always leaves it empty.
	Playbook string
	// Channel is the routing key matched against a playbook's slack_channels.
	Channel string
	// Text is what the human said, a /playbook prefix included.
	Text string
}

// Select applies the routing rules in order: a playbook the source knows, then a leading
// /playbook the human typed, then the channel's claim, then profile.default_playbook. An
// unknown /name is deliberately not an error — somebody typing /shrug must not break the
// bot — it is left in the text and falls through.
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
	return Selection{Playbook: p.Playbooks[p.DefaultPlaybook], Instruction: instruction}
}

// ModelFor is the model a playbook runs on: its own if it named one, the profile's otherwise.
func (p *Profile) ModelFor(s Playbook) string {
	if s.Model != "" {
		return s.Model
	}
	return p.Model
}

// Resolve is what a TASK actually runs on: the override, then the playbook, then the profile,
// then the built-in default. It is the ONE place that ordering lives, so the conductor, the
// brief and the credential the turn is handed can never disagree about it.
func (p *Profile) Resolve(s Playbook, o Override) Choice {
	return p.resolve(Choice{Agent: p.AgentFor(s), Model: p.ModelFor(s), Effort: p.EffortFor(s)}, o)
}

// ResolveAssistant is the same for a turn the conductor answers a conversation with. There
// is no playbook in that path, so the starting point is the profile's own triple — which is
// what the assistant is — and the override is the composer's model picker.
func (p *Profile) ResolveAssistant(o Override) Choice {
	return p.resolve(Choice{Agent: p.AgentFor(Playbook{}), Model: p.Model, Effort: p.Effort}, o)
}

// resolve applies an override to a starting triple. Both callers above share it so that
// "picking grok-4.6 means picking Grok" is true of a conversation and of a task alike.
func (p *Profile) resolve(c Choice, o Override) Choice {
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

// GitFor is who a turn of a playbook commits as: the playbook's own persona, then the
// profile's. An empty one is a real answer — it leaves the runtime's fallback in place.
func (p *Profile) GitFor(s Playbook) GitPersona {
	if s.Git.Set() {
		return s.Git
	}
	return p.Git
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
