package conductor

// On behalf of: a turn that writes as the organization — through the Linear app or the
// GitHub App — rather than as the person who asked says, in what it writes, whose request it
// was. "Created by Acme AI on behalf of Dana (via Slack)." Without it a ticket opened from a
// Slack thread by somebody with no Linear of their own is, in Linear, simply the app's.
//
// The person is named by what Podium knows of them and where they asked, never by whether
// they are in Podium: a Podium person's display name, or the name their Slack, Linear or
// GitHub account shows. Every such turn is also written to the audit log, so who drove the
// app stays on record however the ticket is edited later.
//
// Linear's own MCP server writes the issue, so the footer is asked for in the brief rather
// than enforced; GitHub's commit trailer is enforced by the runtime (withRequestedBy).

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/podium-ade/podium/internal/agent/mcp"
)

// auditOnBehalf is a turn writing as the organization's Linear app or GitHub App for somebody.
const auditOnBehalf = "identity.on_behalf"

// requester is who a turn is for, as its writing names them: Name, and Via, the surface they
// asked on ("Slack", "Linear", "GitHub"; empty for Podium's own web chat). The zero requester
// is a turn nobody asked for, a routine's.
type requester struct {
	Name string
	Via  string
	// Account is the surface account that asked, "<provider>:<id>", for the audit log. Empty
	// for the web chat, whose login is the account.
	Account string
}

// label is how writing names them: "Dana (via Slack)", or "Dana" from the web chat.
func (r requester) label() string {
	if r.Via == "" {
		return r.Name
	}
	return fmt.Sprintf("%s (via %s)", r.Name, r.Via)
}

// viaLabel is a source kind as a person reads it. Empty for the web chat and the dev source.
func viaLabel(kind string) string {
	switch kind {
	case SourceSlack:
		return "Slack"
	case SourceLinear:
		return "Linear"
	case SourceGitHub:
		return "GitHub"
	}
	return ""
}

// requesterOf is who one turn is for: the Podium person behind it by their display name, or,
// for somebody who is not one, the name their account shows where they asked. A turn whose
// event names nobody (a routine's run, the Linear poller) is for nobody.
func (c *Conductor) requesterOf(ctx context.Context, login string, src Source, ev InboundEvent) requester {
	var r requester
	if src != nil {
		r.Via = viaLabel(src.Kind())
	}
	if ev.SpeakerProvider != "" && ev.SpeakerID != "" {
		r.Account = ev.SpeakerProvider + ":" + ev.SpeakerID
	}
	if name, _ := c.requestedBy(ctx, login); name != "" {
		r.Name = name
		return r
	}
	if r.Account == "" {
		// Nobody spoke: a routine's run, a poller's event.
		return requester{}
	}
	r.Name = strings.TrimSpace(ev.Author)
	if r.Name == "" {
		r.Name = "someone"
	}
	return r
}

// usesLinearApp reports whether a turn reaches Linear as the organization's Linear app.
func usesLinearApp(servers []mcp.Server) bool {
	return slices.ContainsFunc(servers, func(s mcp.Server) bool { return s.AuthKind == mcp.AuthLinearApp })
}

// linearOnBehalfNote is the line a turn that writes to Linear as the app for somebody is
// given. Kept separate so a test pins its words. brand is the deployment's name.
func linearOnBehalfNote(brand string, r requester) string {
	return fmt.Sprintf("(%[1]s note: you reach Linear as the %[1]s Linear app, on behalf of %[2]s. "+
		"End every issue description and every comment you write in Linear with the line "+
		"`Created by %[1]s on behalf of %[2]s.` so people in Linear can see whose request it was.)",
		brand, r.label())
}

// withOnBehalfOf gives a turn that reaches Linear as the app the on-behalf-of line, and
// records in the audit log every turn that writes as the organization for somebody: through
// the Linear app, or the GitHub App (withRequestedBy's turns). subject is the conversation.
func (c *Conductor) withOnBehalfOf(
	ctx context.Context, b *Brief, r requester, login, subject, turnID string, servers []mcp.Server, persona *BriefGit,
) {
	if b == nil || r.Name == "" {
		return
	}
	linearApp := usesLinearApp(servers)
	githubApp := persona == nil && b.GitCredentials != nil
	if linearApp {
		b.Instruction = strings.TrimRight(b.Instruction, "\n") + "\n\n" +
			linearOnBehalfNote(displayNameFor("", c.branding(ctx)), r)
	}
	if !linearApp && !githubApp {
		return
	}
	actor := turnOwner(login)
	if actor == "" {
		actor = r.Account
	}
	if actor == "" || subject == "" {
		return
	}
	var apps []string
	if linearApp {
		apps = append(apps, "linear")
	}
	if githubApp {
		apps = append(apps, "github")
	}
	c.audit(ctx, actor, auditOnBehalf, subject, map[string]any{
		"requester": r.Name, "via": r.Via, "account": r.Account, "turn_id": turnID, "apps": apps,
	})
}
