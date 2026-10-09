package profiles

import (
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync/atomic"
	"time"

	"github.com/podium-ade/podium/pkg/spec"
)

// Overrides is the part of profile.yaml an owner or admin may change from Assistant.
// A string is an override when it is non-empty, and empty means "whatever the file says".
// A pointer is an override when it is non-nil, including a pointer at an empty list or at
// zero: those are real decisions (grant nothing, no step cap) and must not collapse into
// "leave the file". The profile's name is absent. It labels every session row already written.
type Overrides struct {
	DisplayName     string `json:"display_name,omitempty"`
	Model           string `json:"model,omitempty"`
	Agent           string `json:"agent,omitempty"`
	Effort          string `json:"effort,omitempty"`
	DefaultPlaybook string `json:"default_playbook,omitempty"`
	// SystemPrompt is the prompt text, not a file: reference. Nil leaves the file's prompt.
	SystemPrompt *string `json:"system_prompt,omitempty"`
	// Skills nil leaves the file. A pointer at an empty slice grants none.
	Skills *[]string `json:"skills,omitempty"`
	// MCPServers is the same shape as Skills, for the servers the assistant may call.
	MCPServers *[]string `json:"mcp_servers,omitempty"`
	// MaxTurns nil leaves the file. A pointer at zero stores no cap.
	MaxTurns *int `json:"max_turns,omitempty"`
	// Timeout is a Go duration string. Nil leaves the file.
	Timeout *string `json:"timeout,omitempty"`
	// Git nil leaves the file. A pointer at an empty persona clears it.
	Git       *GitPersona `json:"git,omitempty"`
	UpdatedBy string      `json:"updated_by,omitempty"`
	UpdatedAt time.Time   `json:"updated_at,omitzero"`
}

// The profile.yaml keys Overrides can supply, as Fields reports them.
const (
	FieldDisplayName     = "display_name"
	FieldModel           = "model"
	FieldAgent           = "agent"
	FieldEffort          = "effort"
	FieldSystemPrompt    = "system_prompt"
	FieldSkills          = "skills"
	FieldMCPServers      = "mcp_servers"
	FieldMaxTurns        = "max_turns"
	FieldTimeout         = "timeout"
	FieldGit             = "git"
	FieldDefaultPlaybook = "default_playbook"
)

// Fields names the profile.yaml keys this override is supplying, in file order. The UI
// shows the file's value beside each one so an operator can see what is being overridden.
func (o Overrides) Fields() []string {
	var out []string
	add := func(key string, on bool) {
		if on {
			out = append(out, key)
		}
	}
	add(FieldDisplayName, o.DisplayName != "")
	add(FieldAgent, o.Agent != "")
	add(FieldModel, o.Model != "")
	add(FieldEffort, o.Effort != "")
	add(FieldSystemPrompt, o.SystemPrompt != nil)
	add(FieldSkills, o.Skills != nil)
	add(FieldMCPServers, o.MCPServers != nil)
	add(FieldMaxTurns, o.MaxTurns != nil)
	add(FieldTimeout, o.Timeout != nil)
	add(FieldGit, o.Git != nil)
	add(FieldDefaultPlaybook, o.DefaultPlaybook != "")
	return out
}

// Trim is the override as it is stored: every field whitespace-trimmed, so a field a human
// cleared to spaces is an empty override rather than an invalid value.
func (o Overrides) Trim() Overrides {
	o.DisplayName = strings.TrimSpace(o.DisplayName)
	o.Model = strings.TrimSpace(o.Model)
	o.Agent = strings.TrimSpace(o.Agent)
	o.Effort = strings.TrimSpace(o.Effort)
	o.DefaultPlaybook = strings.TrimSpace(o.DefaultPlaybook)
	o.SystemPrompt = trimOptional(o.SystemPrompt)
	o.Timeout = trimOptional(o.Timeout)
	o.Skills = trimNames(o.Skills)
	o.MCPServers = trimNames(o.MCPServers)
	if o.Git != nil {
		g := o.Git.trim()
		o.Git = &g
	}
	return o
}

// trimOptional turns a blank string into "no override". A non-nil empty string would
// otherwise survive omitempty's pointer rule and store a prompt of nothing.
func trimOptional(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil
	}
	return &s
}

// trimNames keeps a non-nil list non-nil, so "grant none" stays a decision, and drops
// blank names that a paste would otherwise store.
func trimNames(in *[]string) *[]string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(*in))
	for _, n := range *in {
		n = strings.TrimSpace(n)
		if n != "" {
			out = append(out, n)
		}
	}
	return &out
}

func (o Overrides) apply(p *Profile) error {
	if o.DisplayName != "" {
		p.DisplayName = o.DisplayName
	}
	if o.Model != "" {
		p.Model = o.Model
	}
	if o.Agent != "" {
		p.Agent = o.Agent
	}
	if o.Effort != "" {
		p.Effort = o.Effort
	}
	if o.DefaultPlaybook != "" {
		p.DefaultPlaybook = o.DefaultPlaybook
	}
	if o.SystemPrompt != nil {
		p.SystemPrompt = *o.SystemPrompt
	}
	if o.Skills != nil {
		p.Skills = append([]string(nil), (*o.Skills)...)
	}
	if o.MCPServers != nil {
		p.MCPServers = append([]string(nil), (*o.MCPServers)...)
	}
	if o.MaxTurns != nil {
		p.MaxTurns = *o.MaxTurns
	}
	if o.Timeout != nil {
		d, err := time.ParseDuration(*o.Timeout)
		if err != nil {
			return fmt.Errorf("timeout: parse duration %q: %w", *o.Timeout, err)
		}
		if d <= 0 {
			return fmt.Errorf("timeout must be positive, got %s", *o.Timeout)
		}
		p.Timeout = spec.Duration(d)
	}
	if o.Git != nil {
		p.Git = *o.Git
	}
	return nil
}

// Apply is the profile a turn actually runs from: the profile directory with the Assistant-
// screen overrides on top. Playbooks come only from files.
//
// The result is validated exactly as Load validates the directory. A default_playbook that
// names nothing loaded is not a load error: a fresh install may have an empty playbooks/
// directory, and Select refuses the mention instead of refusing to boot.
func Apply(files *Profile, ov Overrides) (*Profile, error) {
	if files == nil {
		return nil, errors.New("agent profile: there is no profile directory to apply overrides to")
	}
	p := *files
	p.Playbooks = maps.Clone(files.Playbooks)
	ov = ov.Trim()
	if err := ov.apply(&p); err != nil {
		return nil, err
	}
	if err := p.validate("agent profile"); err != nil {
		return nil, err
	}
	return &p, nil
}

// Live is the profile a running conductor reads. The file half is loaded at start and
// swapped only when an operator asks for it, because a directory somebody is halfway through
// saving is not a profile and a timer cannot tell the difference. Overrides from the
// Assistant screen are applied on top and swapped whenever they change.
//
// Every reader takes Current() once and works from the value it got. A turn therefore runs
// the playbook it started with even if that playbook is edited while it is in flight: Playbook is a
// value, and swapping the profile behind it changes nothing about the copy already taken.
type Live struct {
	files atomic.Pointer[Profile]
	cur   atomic.Pointer[Profile]
}

// NewLive holds files as both the file half and, until the first Set, the current profile.
// A conductor that cannot reach its database still runs the directory it was given.
func NewLive(files *Profile) *Live {
	l := &Live{}
	l.files.Store(files)
	l.cur.Store(files)
	return l
}

// Files is profile.yaml and playbooks/*.yaml as they were last read off disk, with nothing
// merged in. It is what the UI shows beside an override.
func (l *Live) Files() *Profile {
	if l == nil {
		return nil
	}
	return l.files.Load()
}

// SetFiles replaces the file half with what a re-read of the profile directory found.
//
// It is separate from Set because the two halves are read apart — Files() is the file's
// value the UI shows beside an override, Current() is what a turn runs — and a caller that
// re-reads the directory has to write both. Write this one first: a profile built from
// files nobody can see would leave the UI explaining an override against the wrong file.
func (l *Live) SetFiles(p *Profile) {
	if l == nil || p == nil {
		return
	}
	l.files.Store(p)
}

// Current is the profile in force. Nil only when there is no profile at all.
func (l *Live) Current() *Profile {
	if l == nil {
		return nil
	}
	return l.cur.Load()
}

// Set swaps the profile every later reader will see.
func (l *Live) Set(p *Profile) {
	if l == nil || p == nil {
		return
	}
	l.cur.Store(p)
}
