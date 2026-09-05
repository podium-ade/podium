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
	DisplayName      string    `json:"display_name,omitempty"`
	Model            string    `json:"model,omitempty"`
	DefaultSkill     string    `json:"default_skill,omitempty"`
	ChatDefaultSkill string    `json:"chat_default_skill,omitempty"`
	UpdatedBy        string    `json:"updated_by,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitzero"`
}

// The profile.yaml keys Overrides can supply, as Fields reports them.
const (
	FieldDisplayName      = "display_name"
	FieldModel            = "model"
	FieldDefaultSkill     = "default_skill"
	FieldChatDefaultSkill = "chat_default_skill"
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
		{FieldModel, o.Model},
		{FieldDefaultSkill, o.DefaultSkill},
		{FieldChatDefaultSkill, o.ChatDefaultSkill},
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
	o.DefaultSkill = strings.TrimSpace(o.DefaultSkill)
	o.ChatDefaultSkill = strings.TrimSpace(o.ChatDefaultSkill)
	return o
}

func (o Overrides) apply(p *Profile) {
	if o.DisplayName != "" {
		p.DisplayName = o.DisplayName
	}
	if o.Model != "" {
		p.Model = o.Model
	}
	if o.DefaultSkill != "" {
		p.DefaultSkill = o.DefaultSkill
	}
	if o.ChatDefaultSkill != "" {
		p.ChatDefaultSkill = o.ChatDefaultSkill
	}
}

// ValidateStoredSkill applies a skill's defaults and validates it by exactly the rules a
// skills/<name>.yaml is held to, so a skill created in the browser is refused for the same
// reasons a file would be. It returns the skill as it will be stored.
//
// The one rule that differs is `file:`: a stored skill has no file beside it to resolve a
// path against, so a prompt that names one is refused rather than silently read off the
// conductor's disk.
func ValidateStoredSkill(s Skill) (Skill, error) {
	if !NameRE.MatchString(s.Name) {
		return Skill{}, fmt.Errorf("skill name %q must match %s", s.Name, NameRE)
	}
	where := fmt.Sprintf("skill %q", s.Name)
	prompt := strings.TrimSpace(s.SystemPrompt)
	switch {
	case prompt == "":
		return Skill{}, fmt.Errorf("%s: system_prompt is required", where)
	case strings.HasPrefix(prompt, filePrefix):
		return Skill{}, fmt.Errorf("%s: system_prompt must be the prompt itself; %s only works "+
			"in a skills/<name>.yaml, which has a file beside it to resolve the path against",
			where, filePrefix)
	}
	s.Origin = OriginStored
	s.applyDefaults()
	if err := s.validate(where); err != nil {
		return Skill{}, err
	}
	return s, nil
}

// Merge is the profile a turn actually runs from: the profile directory, the skills the
// database holds, and the overrides on top.
//
// **The files win.** A stored skill whose name a skills/*.yaml also defines is dropped
// rather than merged, so which of the two definitions runs never depends on which was
// written last — and a GitOps deployment stays the authority over the names it ships.
// Shadowed reports the ones that were dropped, because a skill an operator saved and that
// silently never runs is worse than one they can see is shadowed.
//
// The result is validated exactly as Load validates the directory, so a combination that
// could not have been written as files — two skills claiming one Slack channel, a default
// naming a skill that is not there — is refused here too.
func Merge(files *Profile, ov Overrides, stored []Skill) (merged *Profile, shadowed []string, err error) {
	if files == nil {
		return nil, nil, errors.New("agent profile: there is no profile directory to merge into")
	}
	p := *files
	p.Skills = make(map[string]Skill, len(files.Skills)+len(stored))
	for name, s := range files.Skills {
		s.Name = name
		s.Origin = OriginFile
		p.Skills[name] = s
	}
	for _, s := range stored {
		if _, ok := files.Skills[s.Name]; ok {
			shadowed = append(shadowed, s.Name)
			continue
		}
		s.Origin = OriginStored
		p.Skills[s.Name] = s
	}
	sort.Strings(shadowed)

	ov.Trim().apply(&p)
	if err := p.validate("agent profile"); err != nil {
		return nil, shadowed, err
	}
	return &p, shadowed, nil
}

// Live is the profile a running conductor reads. The file half is loaded once at start;
// the stored half is swapped in whole, atomically, whenever the database changes — which
// is what makes a skill created in the browser reach a turn without a restart.
//
// Every reader takes Current() once and works from the value it got. A turn therefore runs
// the skill it started with even if that skill is edited while it is in flight: Skill is a
// value, and swapping the profile behind it changes nothing about the copy already taken.
type Live struct {
	files *Profile
	cur   atomic.Pointer[Profile]
}

// NewLive holds files as both the file half and, until the first Set, the current profile.
// A conductor that cannot reach its database still runs the directory it was given.
func NewLive(files *Profile) *Live {
	l := &Live{files: files}
	l.cur.Store(files)
	return l
}

// Files is profile.yaml and skills/*.yaml as they were read at start, with nothing merged
// in. It is what the UI shows beside an override.
func (l *Live) Files() *Profile {
	if l == nil {
		return nil
	}
	return l.files
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
