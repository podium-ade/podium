package conductor

// Not spending a model key the provider is refusing.
//
// The control plane re-checks each stored global key in the background (internal/agent/api,
// keyhealth.go). This is the conductor's half: a turn that would spend a refused key is
// stopped before it starts, with one plain sentence instead of a sandbox that boots only to
// post the provider's raw error; and a turn that was refused anyway asks for a check now,
// which is what makes the first failure the last one.

import (
	"context"
	"fmt"
	"regexp"

	"github.com/podium-ade/podium/internal/agent/profiles"
)

// KeyHealth is what the conductor knows about the stored global model keys.
type KeyHealth interface {
	// KeyRefused reports whether the provider is refusing the stored global key for
	// provider, as of the last check.
	KeyRefused(ctx context.Context, provider string) (bool, error)
	// SuspectKey asks for that key to be checked now. It returns at once.
	SuspectKey(provider string)
}

// credentialRefusedRE recognises a turn whose answer is the provider refusing its
// credential: the runtime's own sentence (agent/runtime/src/opencode.ts,
// CredentialRefusedText), and the raw provider errors a runtime from before that sentence
// relayed instead. A false match costs one free check of the key, never a refused turn.
var credentialRefusedRE = regexp.MustCompile(`(?i)refused this bot's API key|invalid x-api-key|` +
	`authentication_error|invalid[ _]api[ _]key|incorrect api key|api key not valid|api key expired`)

// providerLabels are the providers' own names, for a sentence a person reads.
var providerLabels = map[string]string{
	profiles.ProviderAnthropic: "Anthropic",
	profiles.ProviderXAI:       "xAI",
	profiles.ProviderOpenAI:    "OpenAI",
	profiles.ProviderGoogle:    "Google",
}

// keyRefused reports whether choice would spend a global key its provider is refusing, and
// the sentence to post instead of running it. A person's own key is theirs to look after
// and is not checked here; neither is a chat-host turn, which may be spending a Claude
// subscription rather than the key. An answer that cannot be had starts the turn: the gate
// exists to save a doomed turn, not to add a way for a healthy one to fail.
func (c *Conductor) keyRefused(ctx context.Context, choice profiles.Choice, onChatHost bool) (string, bool) {
	if c.keys == nil || choice.CredentialOwner != "" || onChatHost {
		return "", false
	}
	provider := c.providerFor(choice.Agent, "").ID
	refused, err := c.keys.KeyRefused(ctx, provider)
	if err != nil {
		c.logger.WarnContext(ctx, "could not tell whether the model key is refused; starting the turn",
			"provider", provider, "error", err)
		return "", false
	}
	if !refused {
		return "", false
	}
	c.logger.WarnContext(ctx, "not starting a turn: its provider is refusing the stored key",
		"provider", provider, "agent", choice.Agent)
	label := providerLabels[provider]
	if label == "" {
		label = provider
	}
	return fmt.Sprintf("I can't run this right now: %s is refusing this bot's API key, so nothing ran. "+
		"An operator needs to save a working one.", label), true
}

// noteRefusal asks for provider's key to be checked now when a turn that failed said its
// credential was refused.
func (c *Conductor) noteRefusal(ctx context.Context, provider, answer string, failed bool) {
	if c.keys == nil || !failed || provider == "" || !credentialRefusedRE.MatchString(answer) {
		return
	}
	c.logger.WarnContext(ctx, "a turn was refused its model credential; checking the stored key now",
		"provider", provider)
	c.keys.SuspectKey(provider)
}
