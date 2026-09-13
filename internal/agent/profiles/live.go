package profiles

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Overrides is the part of profile.yaml a browser may change. Every field is an override:
// empty means "whatever the file says", which is also how an override is cleared. The
// profile's name and system prompt are deliberately absent — the name labels every session
// row already written, and the prompt is the operator's own file.
type Overrides struct {
	DisplayName     string    `json:"display_name,omitempty"`
	Model           string    `json:"model,omitempty"`
	Agent           string    `json:"agent,omitempty"`
	Effort          string    `json:"effort,omitempty"`
	DefaultPlaybook string    `json:"default_playbook,omitempty"`
	UpdatedBy       string    `json:"updated_by,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitzero"`
}

// The profile.yaml keys Overrides can supply, as Fields reports them.
const (
	FieldDisplayName     = "display_name"
	FieldModel           = "model"
	FieldAgent           = "agent"
	FieldEffort          = "effort"
	FieldDefaultPlaybook = "default_playbook"
)

// Fields names the profile.yaml keys this override is supplying, in file order. The UI
// shows the file's value beside each one so an operator can see what is being overridden.
func (o Overrides) Fields() []string {
	var out []string
	for _, f := range []struct {
		key   string
		value string
	}{
		{FieldDisplayName, o.DisplayName},
		{FieldAgent, o.Agent},
		{FieldModel, o.Model},
		{FieldEffort, o.Effort},
		{FieldDefaultPlaybook, o.DefaultPlaybook},
	} {
		if f.value != "" {
			out = append(out, f.key)
		}
	}
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
	return o
}

func (o Overrides) apply(p *Profile) {
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
}

// ValidateStoredPlaybook applies a playbook's defaults and validates it by exactly the rules a
// playbooks/<name>.yaml is held to, so a playbook created in the browser is refused for the same
// reasons a file would be. It returns the playbook as it will be stored.
//
// The one rule that differs is `file:`: a stored playbook has no file beside it to resolve a
// path against, so a prompt that names one is refused rather than silently read off the
// conductor's disk.
func ValidateStoredPlaybook(s Playbook) (Playbook, error) {
	if !NameRE.MatchString(s.Name) {
		return Playbook{}, fmt.Errorf("playbook name %q must match %s", s.Name, NameRE)
	}
	where := fmt.Sprintf("playbook %q", s.Name)
	prompt := strings.TrimSpace(s.SystemPrompt)
	switch {
	case prompt == "":
		return Playbook{}, fmt.Errorf("%s: system_prompt is required", where)
	case strings.HasPrefix(prompt, filePrefix):
		return Playbook{}, fmt.Errorf("%s: system_prompt must be the prompt itself; %s only works "+
			"in a playbooks/<name>.yaml, which has a file beside it to resolve the path against",
			where, filePrefix)
	}
	s.Origin = OriginStored
	s.applyDefaults()
	if err := s.validate(where); err != nil {
		return Playbook{}, err
	}
	return s, nil
}

// Merge is the profile a turn actually runs from: the profile directory, the playbooks the
// database holds, and the overrides on top.
//
// **The files win.** A stored playbook whose name a playbooks/*.yaml also defines is dropped
// rather than merged, so which of the two definitions runs never depends on which was
// written last — and a GitOps deployment stays the authority over the names it ships.
// Shadowed reports the ones that were dropped, because a playbook an operator saved and that
// silently never runs is worse than one they can see is shadowed.
//
// The result is validated exactly as Load validates the directory, so a combination that
// could not have been written as files — two playbooks claiming one Slack channel — is
// refused here too. A default_playbook naming a playbook that is not loaded is not a load
// error: a fresh install has none, and Select refuses the mention instead of refusing to boot.
func Merge(files *Profile, ov Overrides, stored []Playbook) (merged *Profile, shadowed []string, err error) {
	if files == nil {
		return nil, nil, errors.New("agent profile: there is no profile directory to merge into")
	}
	p := *files
	p.Playbooks = make(map[string]Playbook, len(files.Playbooks)+len(stored))
	for name, s := range files.Playbooks {
		s.Name = name
		s.Origin = OriginFile
		p.Playbooks[name] = s
	}
	for _, s := range stored {
		if _, ok := files.Playbooks[s.Name]; ok {
			shadowed = append(shadowed, s.Name)
			continue
		}
		s.Origin = OriginStored
		p.Playbooks[s.Name] = s
	}
	sort.Strings(shadowed)

	ov.Trim().apply(&p)
	if err := p.validate("agent profile"); err != nil {
		return nil, shadowed, err
	}
	return &p, shadowed, nil
}

// Live is the profile a running conductor reads. The stored half is swapped in whole,
// atomically, whenever the database changes — which is what makes a playbook created in the
// browser reach a turn without a restart. The file half is loaded at start and swapped only
// when an operator asks for it, because a directory somebody is halfway through saving is
// not a profile and a timer cannot tell the difference.
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
// re-reads the directory has to write both. Write this one first: a merged profile built
// from files nobody can see would leave the UI explaining an override against the wrong file.
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
