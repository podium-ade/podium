package conductor

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BriefVersion is the only version this conductor writes and the runtime reads.
const BriefVersion = 1

// BriefEnv is the environment variable one turn brief travels in, base64(JSON), on the task
// spec.
const BriefEnv = "PODIUM_AGENT_TURN"

// MaxBriefBytes caps the *encoded* brief — the base64, as it sits in the env var. The
// runtime refuses anything larger (exit 2) and never truncates; truncating is this side's
// job, because only the conductor knows which part of the context is oldest.
const MaxBriefBytes = 256 * 1024

// The source kinds the runtime's schema accepts. Nothing else validates.
const (
	SourceSlack  = "slack"
	SourceLinear = "linear"
	SourceChat   = "chat"
)

// Brief mirrors agent/runtime/src/brief.ts field for field. That file is the single source
// of truth and its schema is strict at every level: an unknown key anywhere is exit 2, and
// an optional field must be *absent* rather than null. So every optional member below is a
// pointer or a slice with omitempty, and every required one is always emitted.
//
// The field order is the schema's order and the encoding is compact, because
// examples/agent/brief.sh renders the same document with `jq -cn` and a test compares bytes.
type Brief struct {
	Version             int            `json:"version"`
	SessionID           string         `json:"session_id"`
	TurnID              string         `json:"turn_id"`
	Source              BriefSource    `json:"source"`
	Profile             BriefProfile   `json:"profile"`
	Playbook            BriefPlaybook  `json:"playbook"`
	Transcript          []BriefEntry   `json:"transcript"`
	TranscriptTruncated bool           `json:"transcript_truncated"`
	Instruction         string         `json:"instruction"`
	Repos               []BriefRepo    `json:"repos,omitempty"`
	Memory              *BriefMemory   `json:"memory,omitempty"`
	Provider            *BriefProvider `json:"provider,omitempty"`
}

// BriefSource is where the turn came from and how a human reaches the conversation.
type BriefSource struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	URL  string `json:"url,omitempty"`
}

// BriefProfile is the bot's identity for this turn, and the model it runs on.
type BriefProfile struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	SystemPrompt string `json:"system_prompt"`
	Model        string `json:"model"`
	// Effort is the resolved reasoning effort, absent for the model's own default.
	Effort string `json:"effort,omitempty"`
}

// BriefProvider is which model API serves this turn. It is always emitted: `id` and
// profile.model together are the whole of what the harness is pointed at, and a turn with
// nowhere to send its requests cannot run.
//
// Nothing here names a vendor in Go: `id` is whatever the harness calls that provider, so
// adding a third is a catalogue entry rather than a change to this struct.
//
// APIKeyEnv is the NAME of the variable the credential lands in, never a value. A brief is
// an environment variable on a task spec and is visible to anything that can read the spec,
// exactly as memory.api_key_env is.
type BriefProvider struct {
	ID        string `json:"id"`
	APIKeyEnv string `json:"api_key_env"`
	// BaseURL overrides where the provider is reached — an egress proxy, or a test seam.
	// Empty means the harness's own default for that provider.
	BaseURL string `json:"base_url,omitempty"`
}

// BriefPlaybook is the job the turn is doing.
type BriefPlaybook struct {
	Name         string   `json:"name"`
	SystemPrompt string   `json:"system_prompt"`
	AllowedTools []string `json:"allowed_tools"`
	MaxTurns     int      `json:"max_turns"`
	// Skills is the Agent Skills this turn may use. Absent means none, and the runtime
	// writes a permission map that denies every skill either way.
	Skills []BriefSkill `json:"skills,omitempty"`
}

// BriefSkill points the runtime at one Agent Skill bundle. It carries a NAME and a DIGEST,
// never the bytes: the bundle rides in its own environment variable, exactly as a
// credential does, and for the same reason — the brief is a document an operator reads, and
// a quarter of a megabyte of base64 in the middle of it is not readable.
//
// Keeping them apart also keeps the two caps apart. A brief that does not fit is truncated
// by dropping the oldest transcript entries; a bundle in the brief would therefore buy
// itself room by silently deleting the conversation, which is the wrong trade to make on a
// human's behalf.
type BriefSkill struct {
	// Name is the skill's name and the directory it is unpacked into.
	Name string `json:"name"`
	// SHA256 is the hex digest of the bundle document the runtime must verify before it
	// writes anything.
	SHA256 string `json:"sha256"`
	// BundleEnv names the environment variable holding base64(gzip(document)).
	BundleEnv string `json:"bundle_env"`
}

// BriefEntry is one utterance of the conversation so far.
type BriefEntry struct {
	Role   string `json:"role"`
	Author string `json:"author"`
	TS     string `json:"ts"`
	Text   string `json:"text"`
}

// BriefRepo is a repository the runtime clones into /workspace.
type BriefRepo struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
}

// BriefMemory points the runtime at Hindsight. Step 19 fills it; nothing here does.
type BriefMemory struct {
	MCPURL    string `json:"mcp_url"`
	APIKeyEnv string `json:"api_key_env"`
}

// Roles a transcript entry may carry.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// BriefTimestamp is how a transcript entry's ts is rendered. The schema only asks for a
// string; RFC 3339 in UTC is what the golden fixture uses and what a model can read.
func BriefTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// ErrBriefTooLarge is what Encode returns when even an empty transcript does not fit. It
// means the prompts, the instruction or the repo list are themselves over the cap, and no
// amount of dropping history will help.
var ErrBriefTooLarge = errors.New("turn brief does not fit even with an empty transcript")

// Encode renders the brief for PODIUM_AGENT_TURN, dropping the oldest transcript entries
// until the encoded form fits MaxBriefBytes and setting TranscriptTruncated when it dropped
// any. The brief is modified in place so the caller can see what was actually sent.
func (b *Brief) Encode() (string, error) {
	b.normalise()
	for {
		encoded, err := b.encodeOnce()
		if err != nil {
			return "", err
		}
		if len(encoded) <= MaxBriefBytes {
			return encoded, nil
		}
		if len(b.Transcript) == 0 {
			return "", fmt.Errorf("%w: %d bytes encoded, the limit is %d",
				ErrBriefTooLarge, len(encoded), MaxBriefBytes)
		}
		// Oldest first: the end of a conversation is what the turn is answering.
		b.Transcript = b.Transcript[1:]
		b.TranscriptTruncated = true
	}
}

// normalise fills in what the schema requires to be present. A nil slice would marshal as
// null, and the runtime's schema wants [].
func (b *Brief) normalise() {
	if b.Version == 0 {
		b.Version = BriefVersion
	}
	if b.Transcript == nil {
		b.Transcript = []BriefEntry{}
	}
	if b.Playbook.AllowedTools == nil {
		b.Playbook.AllowedTools = []string{}
	}
}

// encodeOnce is the byte-exact encoder: compact JSON with HTML escaping off, because `jq`
// does not escape `<`, `>` or `&` either and examples/agent/brief.sh has to agree with this
// function byte for byte. json.Marshal would escape them; json.Encoder can be told not to.
func (b *Brief) encodeOnce() (string, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(b); err != nil {
		return "", fmt.Errorf("encode turn brief: %w", err)
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.TrimSuffix(buf.String(), "\n"))), nil
}
