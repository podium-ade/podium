// Package profiles loads the bot's identity and its skills off disk. The directory
// (PODIUM_AGENT_PROFILE_DIR) is the only place a skill's image, prompt, tool list and
// secrets are declared: a skill only ever gets the secrets its own file names, and that
// file is the only boundary there is.
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

// AnthropicKeySecret is the reserved secret the conductor attaches to every turn itself. A
// skill may not name it: the whole point of the reservation is that no skill file decides
// whether the bot can talk to the model.
const AnthropicKeySecret = "podium.agent.anthropic_api_key"

// AnthropicKeyEnv is where that secret lands in the task container.
const AnthropicKeyEnv = "ANTHROPIC_API_KEY"

// MemoryKeySecret is the other reserved secret the conductor attaches itself: the shared
// memory's API key. A skill may not name it and a skill cannot opt out of memory — only the
// operator can, by leaving PODIUM_AGENT_MEMORY_URL empty.
const MemoryKeySecret = "podium.agent.memory_api_key"

// MemoryKeyEnv is where that secret lands in the task container. The brief's
// memory.api_key_env names it, and the runtime reads it to authenticate its MCP client.
const MemoryKeyEnv = "PODIUM_MEMORY_API_KEY"

// BriefEnv is the env var the brief travels in. A skill's env: may not set it.
const BriefEnv = "PODIUM_AGENT_TURN"

// Defaults for a skill.
const (
	DefaultMaxTurns = 50
	DefaultTimeout  = spec.Duration(30 * 60 * 1e9)
)

// filePrefix marks a prompt that lives in its own file, resolved relative to the YAML file
// that names it.
const filePrefix = "file:"

// Profile is the bot: one identity, one model, a set of skills.
type Profile struct {
	Name         string `yaml:"name"`
	DisplayName  string `yaml:"display_name"`
	SystemPrompt string `yaml:"system_prompt"`
	Model        string `yaml:"model"`
	DefaultSkill string `yaml:"default_skill"`

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
type Skill struct {
	Image         string            `yaml:"image"`
	SystemPrompt  string            `yaml:"system_prompt"`
	AllowedTools  []string          `yaml:"allowed_tools"`
	MaxTurns      int               `yaml:"max_turns"`
	Timeout       spec.Duration     `yaml:"timeout"`
	Model         string            `yaml:"model"`
	Labels        []string          `yaml:"labels"`
	Resources     spec.Resources    `yaml:"resources"`
	Secrets       []spec.SecretRef  `yaml:"secrets"`
	Repos         []Repo            `yaml:"repos"`
	SlackChannels []string          `yaml:"slack_channels"`
	Env           map[string]string `yaml:"env"`
	// Linear marks the one skill Linear tickets run. Tickets are not chat, so there is no
	// /skill prefix to route them and no channel to match: the flag is the routing rule.
	// At most one skill may set it; zero means this bot does not take tickets, which is
	// only a misconfiguration when a Linear API key is also set — and the conductor says
	// so at start-up, where the key is known.
	Linear bool `yaml:"linear"`

	// Name is the file name without the extension.
	Name string `yaml:"-"`
}

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
	for _, ref := range s.Secrets {
		switch ref.Name {
		case AnthropicKeySecret, MemoryKeySecret:
			errs = append(errs, fmt.Errorf("secrets may not name %s: it is added automatically to every turn",
				ref.Name))
		}
	}
	for key := range s.Env {
		switch key {
		case BriefEnv:
			errs = append(errs, fmt.Errorf("env may not set %s: the conductor writes the turn brief", BriefEnv))
		case AnthropicKeyEnv:
			errs = append(errs, fmt.Errorf("env may not set %s: it comes from the %s secret",
				AnthropicKeyEnv, AnthropicKeySecret))
		case MemoryKeyEnv:
			errs = append(errs, fmt.Errorf("env may not set %s: it comes from the %s secret",
				MemoryKeyEnv, MemoryKeySecret))
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
	if p.DefaultSkill == "" {
		errs = append(errs, errors.New("default_skill is required"))
	} else if _, ok := p.Skills[p.DefaultSkill]; !ok {
		errs = append(errs, fmt.Errorf("default_skill %q names no skill in skills/ (have %s)",
			p.DefaultSkill, strings.Join(p.SkillNames(), ", ")))
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
	// refused rather than honoured: one session, one skill.
	Explicit bool
}

// Select applies the three routing rules in order: an explicit skill from the source, then
// a leading /skill on the text, then the channel's claim, then the default. An unknown
// /name is deliberately not an error — somebody typing /shrug must not break the bot — it
// is left in the text and falls through.
func (p *Profile) Select(sourceSkill, channel, text string) Selection {
	if sourceSkill != "" {
		if s, ok := p.Skills[sourceSkill]; ok {
			return Selection{Skill: s, Instruction: strings.TrimSpace(text), Explicit: true}
		}
	}
	if m := SkillPrefixRE.FindStringSubmatch(text); m != nil {
		if s, ok := p.Skills[m[1]]; ok {
			return Selection{
				Skill:       s,
				Instruction: strings.TrimSpace(text[len(m[0]):]),
				Explicit:    true,
			}
		}
	}
	instruction := strings.TrimSpace(text)
	if channel != "" {
		for _, name := range p.SkillNames() {
			for _, ch := range p.Skills[name].SlackChannels {
				if ch == channel {
					return Selection{Skill: p.Skills[name], Instruction: instruction}
				}
			}
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
