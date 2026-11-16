package conductor

import (
	"context"
	"strings"
	"sync"
	"time"
)

// houseName is what the product is called when nobody renamed it, and the display name the
// shipped profile gives the house assistant.
const houseName = "Podium"

// brandingTTL is how long the names read from podium-server are trusted. A rename reaches
// the next turn after at most this long; a turn never waits on the read for longer than
// brandingTimeout.
const (
	brandingTTL     = 30 * time.Second
	brandingTimeout = 2 * time.Second
)

// branding is what this deployment is called and whom it serves, as podium-server's
// OrganizationService has them. Empty fields were never set.
type branding struct {
	Deployment   string
	Organization string
}

// brandCache holds the last names read. A failed read keeps the last good ones: a turn
// that names the deployment "Podium" for a few seconds beats a turn that does not start.
type brandCache struct {
	mu   sync.Mutex
	at   time.Time
	last branding
}

// branding returns the deployment's names, reading them from podium-server's WhoAmI when
// the cached ones are older than brandingTTL.
func (c *Conductor) branding(ctx context.Context) branding {
	if c.podium == nil {
		return branding{}
	}
	c.brand.mu.Lock()
	defer c.brand.mu.Unlock()
	if !c.brand.at.IsZero() && time.Since(c.brand.at) < brandingTTL {
		return c.brand.last
	}
	// Stamped before the read, so a server that is down is asked once per TTL rather than
	// once per turn.
	c.brand.at = time.Now()
	rctx, cancel := context.WithTimeout(ctx, brandingTimeout)
	defer cancel()
	who, err := c.podium.WhoAmI(rctx)
	if err != nil {
		c.logger.WarnContext(ctx, "could not read the deployment's name; keeping the last one", "error", err)
		return c.brand.last
	}
	c.brand.last = branding{
		Deployment:   strings.TrimSpace(who.GetDeploymentName()),
		Organization: strings.TrimSpace(who.GetOrganizationName()),
	}
	return c.brand.last
}

// cachedBranding is the last names read, without reading. For callers with no context to
// spend on a network call.
func (c *Conductor) cachedBranding() branding {
	c.brand.mu.Lock()
	defer c.brand.mu.Unlock()
	return c.brand.last
}

// displayNameFor is what the house assistant calls itself. A deployment renamed to "Acme AI"
// renames the assistant too, unless an admin already gave it a name of its own.
func displayNameFor(profileName string, b branding) string {
	if b.Deployment != "" && (profileName == "" || profileName == houseName) {
		return b.Deployment
	}
	if profileName == "" {
		return houseName
	}
	return profileName
}

// withBranding puts the deployment's names in front of the system prompt. The runtime's own
// prompt and its tool descriptions say "Podium" in many places; telling the model once what
// that means here is what makes it say "Acme AI" to people, and needs no new brief field,
// so a runtime image older than this conductor still accepts the brief.
func withBranding(prompt string, b branding) string {
	note := brandingNote(b)
	if note == "" {
		return prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return note
	}
	return note + "\n\n" + prompt
}

func brandingNote(b branding) string {
	var parts []string
	if b.Deployment != "" {
		parts = append(parts, "This deployment is called "+b.Deployment+". Call it "+b.Deployment+
			" when you talk to people. Where these instructions or a tool's description say "+
			houseName+" (a "+houseName+" task, a "+houseName+" chat, "+houseName+" people), they mean "+
			b.Deployment+". Tool names stay as they are.")
	}
	if b.Organization != "" {
		parts = append(parts, "You work for "+b.Organization+", and the people you talk to here are its members.")
	}
	return strings.Join(parts, " ")
}
