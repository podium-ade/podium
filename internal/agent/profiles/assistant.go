package profiles

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/podium-ade/podium/pkg/spec"
)

// MaxSystemPromptBytes caps the assistant's prompt. The brief that carries a turn is
// itself capped, and a prompt of a whole book would fail the turn rather than the save.
const MaxSystemPromptBytes = 32 << 10

// AssistantDefinition is one assistant a tenant configures.
//
// The conductor runs exactly one of them, the catalog's active id. The definition is the
// whole assistant: every field is set, and nothing on it means "use the install file".
// A later create adds another definition to the same catalog. This type does not decide
// which screen does that.
type AssistantDefinition struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	DisplayName  string     `json:"display_name"`
	SystemPrompt string     `json:"system_prompt"`
	Model        string     `json:"model"`
	Agent        string     `json:"agent"`
	Effort       string     `json:"effort"`
	Git          GitPersona `json:"git"`
	Skills       []string   `json:"skills"`
	MCPServers   []string   `json:"mcp_servers"`
	MaxTurns     int        `json:"max_turns"`
	Timeout      string     `json:"timeout"`
	UpdatedBy    string     `json:"updated_by,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at,omitzero"`
}

// AssistantCatalog is every assistant definition this conductor has stored, and which one
// answers. ActiveID names one of Definitions. An empty catalog is not stored: until the
// first save, the install file and any legacy override row still describe the assistant.
type AssistantCatalog struct {
	ActiveID    string                `json:"active_id"`
	Definitions []AssistantDefinition `json:"definitions"`
}

// NewAssistantID is a stable id for a definition. It is not the name sessions are labeled
// with. The name can change. The id does not.
func NewAssistantID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("assistant id: %w", err)
	}
	return "ast_" + hex.EncodeToString(b[:]), nil
}

// Active is the definition the conductor runs.
func (c AssistantCatalog) Active() (AssistantDefinition, bool) {
	for _, d := range c.Definitions {
		if d.ID == c.ActiveID && d.ID != "" {
			return d, true
		}
	}
	return AssistantDefinition{}, false
}

// Add appends a definition. An empty id is assigned. The first definition becomes active.
// A second one does not: switching which assistant answers is a separate decision.
func (c AssistantCatalog) Add(def AssistantDefinition) (AssistantCatalog, error) {
	def = def.trimmed()
	if err := def.Check(); err != nil {
		return c, err
	}
	if def.ID == "" {
		id, err := NewAssistantID()
		if err != nil {
			return c, err
		}
		def.ID = id
	}
	for _, d := range c.Definitions {
		if d.ID == def.ID {
			return c, fmt.Errorf("assistant %q already exists", def.ID)
		}
	}
	if c.nameTaken(def.Name, "") {
		return c, fmt.Errorf("an assistant named %q already exists", def.Name)
	}
	c.Definitions = append(c.Definitions, def)
	if c.ActiveID == "" {
		c.ActiveID = def.ID
	}
	return c, nil
}

// Upsert replaces one definition and leaves the others, and which one is active, alone.
// An empty id replaces the active definition. An id that is not in the catalog is refused:
// creating a definition is Add, so a save cannot fork one by mistyping an id.
func (c AssistantCatalog) Upsert(def AssistantDefinition) (AssistantCatalog, error) {
	if len(c.Definitions) == 0 {
		return c.Add(def)
	}
	def = def.trimmed()
	if err := def.Check(); err != nil {
		return c, err
	}
	if def.ID == "" {
		def.ID = c.ActiveID
	}
	idx := -1
	for i, d := range c.Definitions {
		if d.ID == def.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return c, fmt.Errorf("no assistant %q", def.ID)
	}
	if c.nameTaken(def.Name, def.ID) {
		return c, fmt.Errorf("an assistant named %q already exists", def.Name)
	}
	c.Definitions[idx] = def
	return c, nil
}

// SetActive chooses which definition answers. It does not create one.
func (c AssistantCatalog) SetActive(id string) (AssistantCatalog, error) {
	for _, d := range c.Definitions {
		if d.ID == id {
			c.ActiveID = id
			return c, nil
		}
	}
	return c, fmt.Errorf("no assistant %q", id)
}

func (c AssistantCatalog) nameTaken(name, exceptID string) bool {
	for _, d := range c.Definitions {
		if d.ID == exceptID {
			continue
		}
		if d.Name == name {
			return true
		}
	}
	return false
}

func (d AssistantDefinition) trimmed() AssistantDefinition {
	d.ID = strings.TrimSpace(d.ID)
	d.Name = strings.TrimSpace(d.Name)
	d.DisplayName = strings.TrimSpace(d.DisplayName)
	d.SystemPrompt = strings.TrimSpace(d.SystemPrompt)
	d.Model = strings.TrimSpace(d.Model)
	d.Agent = strings.TrimSpace(d.Agent)
	d.Effort = strings.TrimSpace(d.Effort)
	d.Timeout = strings.TrimSpace(d.Timeout)
	d.Git = d.Git.trim()
	d.Skills = trimList(d.Skills)
	d.MCPServers = trimList(d.MCPServers)
	return d
}

func trimList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, n := range in {
		n = strings.TrimSpace(n)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Prepare trims a definition and checks it on its own, before it is laid over a profile
// directory. Playbooks, and the rules that need them, are ApplyDefinition's.
func (d AssistantDefinition) Prepare() (AssistantDefinition, error) {
	d = d.trimmed()
	if err := d.Check(); err != nil {
		return AssistantDefinition{}, err
	}
	return d, nil
}

// Check is the definition on its own, before it is laid over a profile directory.
// Playbooks, and the rules that need them, are ApplyDefinition's.
func (d AssistantDefinition) Check() error {
	var errs []error
	if !NameRE.MatchString(d.Name) {
		errs = append(errs, fmt.Errorf("name %q must match %s", d.Name, NameRE))
	}
	if d.DisplayName == "" {
		errs = append(errs, errors.New("display_name is required"))
	}
	if d.Model == "" {
		errs = append(errs, errors.New("model is required"))
	}
	if d.SystemPrompt == "" {
		errs = append(errs, errors.New("system_prompt is required"))
	} else if len(d.SystemPrompt) > MaxSystemPromptBytes {
		errs = append(errs, fmt.Errorf("system_prompt is %d bytes; the limit is %d", len(d.SystemPrompt), MaxSystemPromptBytes))
	}
	if d.Timeout == "" {
		errs = append(errs, errors.New("timeout is required"))
	}
	return errors.Join(errs...)
}

// ApplyDefinition is the profile a turn runs from once an assistant has been saved.
//
// The definition replaces the assistant fields of the install file. Playbooks still come
// from the directory, and default_playbook is left as the file had it: routing does not
// use it, and it is not part of a definition. The file itself is not modified.
func ApplyDefinition(files *Profile, def AssistantDefinition) (*Profile, error) {
	if files == nil {
		return nil, errors.New("agent profile: there is no profile directory to apply an assistant to")
	}
	def = def.trimmed()
	if err := def.Check(); err != nil {
		return nil, err
	}
	p := *files
	p.Playbooks = maps.Clone(files.Playbooks)
	p.Name = def.Name
	p.DisplayName = def.DisplayName
	p.SystemPrompt = def.SystemPrompt
	p.Model = def.Model
	p.Agent = def.Agent
	p.Effort = def.Effort
	p.Git = def.Git
	p.Skills = append([]string(nil), def.Skills...)
	p.MCPServers = append([]string(nil), def.MCPServers...)
	p.MaxTurns = def.MaxTurns
	dur, err := time.ParseDuration(def.Timeout)
	if err != nil {
		return nil, fmt.Errorf("timeout: parse duration %q: %w", def.Timeout, err)
	}
	if dur <= 0 {
		return nil, fmt.Errorf("timeout must be positive, got %s", def.Timeout)
	}
	p.Timeout = spec.Duration(dur)
	if err := p.validate("agent profile"); err != nil {
		return nil, err
	}
	return &p, nil
}
