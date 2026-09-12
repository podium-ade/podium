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

// maxArgStrlen is Linux's cap on ONE environment string — MAX_ARG_STRLEN, which the kernel
// fixes at 32 * PAGE_SIZE. That is 128 KiB wherever the page is 4 KiB, which is every kernel
// Podium runs on; a bigger page only raises it, and nothing configures it down. So this is a
// floor rather than an estimate, and it is a floor worth respecting: past it the container
// cannot exec at all, which is the worst failure available here, because it happens before
// the runtime's entrypoint. Nothing reports it, the transcript is empty, and the task simply
// exits. Bisected against a real container, `NAME=value` and its NUL together:
//
//	$ docker run --rm -e "PODIUM_AGENT_TURN=$(python3 -c "print('x'*131053)")" alpine:3 /bin/sh -c 'printf ok'
//	ok
//	$ docker run --rm -e "PODIUM_AGENT_TURN=$(python3 -c "print('x'*131054)")" alpine:3 /bin/sh -c 'printf ok'
//	exec /bin/sh: argument list too long
//
// 131053 + len("PODIUM_AGENT_TURN=") + 1 for the NUL is exactly 131072.
//
// The cap is per string and not per environment: a brief at MaxBriefBytes beside eight skill
// bundles at skills.MaxEncodedBytes — 608 KiB of environment — execs. What bounds the whole
// of argv plus environ is a different and much larger limit, a quarter of RLIMIT_STACK, so
// about 2 MiB at the usual 8 MiB; docs/agent.md carries that measurement and nothing here
// budgets against it. TestTheBriefCapLeavesRoomToExec is what pins this one.
const maxArgStrlen = 128 << 10

// MaxBriefBytes caps the *encoded* brief — the base64, as it sits in the env var. The
// runtime refuses anything larger (exit 2) and never truncates; truncating is this side's
// job, because only the conductor knows which part of the context is oldest.
//
// It has to sit under maxArgStrlen with room to spare, or a brief that passes validation
// makes a task that cannot start. 96 KiB leaves a quarter of the limit unused, and it is not
// the smaller number it could be for one reason: a brief holds 3/4 of its cap as JSON, so
// 96 KiB is 72 KiB of document, and a 32 KiB chat message (api.maxChatMessageBytes) has to
// stay answerable beside two system prompts and some history.
const MaxBriefBytes = 96 * 1024

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
	Version             int           `json:"version"`
	SessionID           string        `json:"session_id"`
	TurnID              string        `json:"turn_id"`
	Source              BriefSource   `json:"source"`
	Profile             BriefProfile  `json:"profile"`
	Playbook            BriefPlaybook `json:"playbook"`
	Transcript          []BriefEntry  `json:"transcript"`
	TranscriptTruncated bool          `json:"transcript_truncated"`
	Instruction         string        `json:"instruction"`
	Repos               []BriefRepo   `json:"repos,omitempty"`
	// Git is who this turn's commits are by. Absent means the runtime's own fallback, which
	// is an identity no GitHub account owns — see profiles.GitPersona.
	Git *BriefGit `json:"git,omitempty"`
	// GitCredentials is where a turn redeems its minting capability for a GitHub token.
	// Absent means this conductor has no GitHub App, or this playbook has no repositories
	// to mint for, and the older path — a playbook naming its own token secret — applies.
	GitCredentials *BriefGitCredentials `json:"git_credentials,omitempty"`
	Memory         *BriefMemory         `json:"memory,omitempty"`
	Browser        *BriefBrowser        `json:"browser,omitempty"`
	Provider       *BriefProvider       `json:"provider,omitempty"`
	// Delegation is present only on a HOST turn's brief. A task's brief never carries one,
	// which is what stops a delegated task delegating again.
	Delegation *BriefDelegation `json:"delegation,omitempty"`
	// RunsOn is RunsOnHost for a turn running as a child of the conductor and absent for
	// one running in a task container. The runtime cannot infer it and must not guess: the
	// prompt tells a container turn it has a disposable filesystem and may leave files to
	// be attached, and both of those are false on a host.
	RunsOn string `json:"runs_on,omitempty"`
}

// RunsOnHost is Brief.RunsOn for a turn the conductor runs itself. The absent value means a
// task container, which is what every brief before host turns existed described.
const RunsOnHost = "host"

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
	// MaxTurns caps the turn's steps. ZERO means no cap and is omitted from the document,
	// which is what the assistant runs with: the thing worth bounding is the container it
	// starts, not the relay that started it. A task always has one.
	MaxTurns int `json:"max_turns,omitempty"`
	// Skills is the Agent Skills this turn may use. Absent means none, and the runtime
	// writes a permission map that denies every skill either way.
	Skills []BriefSkill `json:"skills,omitempty"`
	// MCPServers is the MCP servers this turn may use. Absent means none, and the runtime
	// writes the harness one entry per server here and no others — so a playbook that names
	// none has no MCP tools beyond the ones the conductor wires up itself.
	MCPServers []BriefMCPServer `json:"mcp_servers,omitempty"`
	// Interactive is true when this turn may ask a human a question and wait. Absent
	// (false) is the default: the runtime has no ask tool and the prompt says so.
	Interactive bool `json:"interactive,omitempty"`
}

// BriefMCPServer points the runtime at one MCP server. It carries an address and the NAME of
// a credential, never a value — the same split memory and the model providers follow, for the
// same reason: the brief is an environment variable on a task spec and is readable by
// anything that can read the spec.
//
// How the credential is presented is not in here. `Authorization: Bearer <token>` is what
// the MCP authorization specification says and what the runtime writes; there is nothing
// per-server to carry.
type BriefMCPServer struct {
	// Name is the server's registered name, and the prefix the harness gives its tools.
	Name string `json:"name"`
	// URL is the server's endpoint.
	URL string `json:"url"`
	// TokenEnv names the environment variable holding the bearer token. Absent for a server
	// registered without one, which reaches its turns unauthenticated.
	TokenEnv string `json:"token_env,omitempty"`
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

// BriefGit is the user.name and user.email the runtime writes into every clone.
type BriefGit struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// BriefGitCredentials points the runtime at this conductor's minting endpoint.
//
// It carries an address and the NAME of the variable the capability arrives in, exactly as
// BriefMemory does and for the same reason: a brief is an environment variable on a task
// spec, so it may name a credential and must never carry one.
type BriefGitCredentials struct {
	URL      string `json:"url"`
	TokenEnv string `json:"token_env"`
}

// BriefMemory points the runtime at Hindsight. Step 19 fills it; nothing here does.
type BriefMemory struct {
	MCPURL    string `json:"mcp_url"`
	APIKeyEnv string `json:"api_key_env"`
}

// BriefDelegation is how a host turn reaches a container: the conductor's own address, the
// variable holding the token that authorises this turn to use it, and the playbooks it may
// ask for.
//
// The playbook list is the ALLOW-LIST as well as the menu. It is built from the same profile
// snapshot as the token's grant, so what the model was shown and what the conductor will
// accept cannot drift apart between the fork and the call — a name that is not here is
// refused rather than resolved.
//
// TokenEnv is the NAME of a variable, never a value, exactly as memory.api_key_env is: a
// brief is a document, and one that carried a bearer token would be a document that must
// never be logged.
type BriefDelegation struct {
	URL       string              `json:"url"`
	TokenEnv  string              `json:"token_env"`
	Playbooks []DelegablePlaybook `json:"playbooks"`
}

// BriefBrowser points the runtime at the headless Chrome running beside this turn. It
// carries an address and nothing else: the browser is reached over the task's own private
// network, so there is no credential to name.
type BriefBrowser struct {
	// CDPURL is the DevTools endpoint of the browser sidecar.
	CDPURL string `json:"cdp_url"`
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

// briefKindFor is the source.kind the runtime's schema will accept for a session's source.
// The schema allows slack, linear and chat and nothing else, and the test-only dev source
// presents itself as chat.
//
// It exists because a DELEGATED task used to be told it came from the web chat whatever
// asked for it. That was invisible while only chats could delegate; it stopped being
// invisible when a Slack thread could, because the runtime's prompt tells the model where
// its answer is going and it was naming the wrong place.
func briefKindFor(kind string) string {
	if kind == KindDev {
		return SourceChat
	}
	return kind
}
