package conductor

// The byline: what answered, said under the answer. A Slack thread several people read has
// no other way to tell which model replied or whose credential paid for it, and once a
// conversation can spend the asker's own subscription that second part is the one people
// ask about. Neither part is a secret: a model's name and a person's name, never a key or
// any part of one.

import (
	"context"
	"strings"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

// Byline is what answered one turn, as a source may show it beside the answer: the model, and
// whose credential it spent. The zero Byline says nothing, and a source shows nothing for it.
type Byline struct {
	// Model is the model's display name, "Claude Opus 5.5", or its id when the catalogue does
	// not know it.
	Model string
	// Credential is whose credential paid and what kind it was: "Workspace API key",
	// "Alvaro Ibarguen's Claude plan". Empty when it cannot be told, as on a turn picked up
	// after a restart, which knows its model and not whose key it was started on.
	Credential string
}

// Empty reports whether b says nothing.
func (b Byline) Empty() bool { return b.Model == "" && b.Credential == "" }

// String is b as one line, its parts joined the way a footer reads: "Claude Opus 5.5 ·
// Workspace API key".
func (b Byline) String() string {
	var parts []string
	for _, p := range []string{b.Model, b.Credential} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// bylineFor is the byline of a turn on model, through provider, spending owner's credential
// (empty for the workspace's). claudePlan is whether it spends a Claude subscription, which
// only the chat host's claude-agent turns do and which the caller has already decided
// (claudeAgentTurn): it is not readable from owner alone, because a person's Claude sign-in
// is spent only there and the global one sits beside the global key rather than replacing it.
func (c *Conductor) bylineFor(ctx context.Context, provider, model, owner string, claudePlan bool) Byline {
	plan := claudePlan
	if provider != profiles.ProviderAnthropic {
		// xAI and OpenAI sign-ins replace the key they sit in, for a person and for the
		// workspace alike, so the stored auth kind is the answer.
		plan = c.authKind(provider, owner) == "oauth"
	}
	kind := "API key"
	if plan {
		kind = planName(provider)
	}
	whose := "Workspace"
	if owner != "" {
		whose = c.personName(ctx, owner) + "'s"
	}
	return Byline{Model: modelName(model), Credential: whose + " " + kind}
}

// bylineOfBackend is the byline a turn or a delegation can still give after a restart: its
// row records the model it ran, and not whose credential it was started on.
func bylineOfBackend(b store.Backend) Byline {
	return Byline{Model: modelName(b.Model)}
}

// planName is what a provider's subscription is called where people buy it.
func planName(provider string) string {
	switch provider {
	case profiles.ProviderAnthropic:
		return "Claude plan"
	case profiles.ProviderOpenAI:
		return "ChatGPT plan"
	case profiles.ProviderXAI:
		return "Grok plan"
	}
	return "plan"
}

// modelName is a model id's display name from the catalogue, or the id itself: a model that
// is not in the catalogue is still a model somebody picked, and its id is its name.
func modelName(id string) string {
	for _, b := range profiles.Backends {
		if m, ok := b.FindModel(id); ok && m.DisplayName != "" {
			return m.DisplayName
		}
	}
	return id
}

// personName is login's display name as the members list has it, or the part of the login
// before the @ when there is none: an email address is more than a byline needs to say.
func (c *Conductor) personName(ctx context.Context, login string) string {
	if c.store != nil {
		if name, _, err := c.store.MemberProfile(ctx, login); err == nil && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	if at := strings.IndexByte(login, '@'); at > 0 {
		return login[:at]
	}
	return login
}
