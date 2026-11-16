package conductor

// Linear tickets a person's message names, handed to the assistant (docs/agent.md, "Tickets in
// a chat"). When somebody writes "can you look at EME-6?" to the assistant, the turn is briefed
// with that ticket — its title, state, description and latest comments — if, and only if, the
// person could see it in Linear. Who may see what is the tickets package's to decide; the
// conductor only asks, with the speaker's login, and puts what comes back in the brief, where
// the runtime fences it off as data written by people in the workspace, as it does a routine's
// Linear events.

import (
	"context"
	"time"
)

const (
	// MaxBriefTickets is how many tickets one message is briefed with. A message that names
	// more gets the first ones it names.
	MaxBriefTickets = 3
	// ticketsTimeout bounds asking for them. A turn never waits long on Linear: it starts
	// without the tickets instead, and can still look them up itself.
	ticketsTimeout = 5 * time.Second
)

// BriefTicket is one Linear issue a person's message named. Everything in it but Identifier was
// written by people in Linear: it is data, never instructions.
type BriefTicket struct {
	Identifier string   `json:"identifier"`
	Title      string   `json:"title"`
	URL        string   `json:"url,omitempty"`
	Team       string   `json:"team,omitempty"`
	State      string   `json:"state,omitempty"`
	Assignee   string   `json:"assignee,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	// Description is the issue's description, trimmed.
	Description string `json:"description,omitempty"`
	// Comments are the latest comments, oldest first, each trimmed.
	Comments []BriefTicketComment `json:"comments,omitempty"`
	// Trimmed says the description or a comment is shorter than Linear has it, or that
	// comments were left out.
	Trimmed bool `json:"trimmed,omitempty"`
}

// BriefTicketComment is one comment on a ticket.
type BriefTicketComment struct {
	Author string `json:"author"`
	At     string `json:"at"`
	Body   string `json:"body"`
}

// TicketBriefer is what the conductor asks for the tickets a message names: those of the at
// most MaxBriefTickets issues text names that login may see in Linear, leaving out the issue
// whose conversation sourceKey is (its turn is already about it). It returns nothing, never an
// error, when it cannot tell.
type TicketBriefer func(ctx context.Context, login, sourceKey, text string) []BriefTicket

// briefTickets is the tickets a person's own message to the assistant names. A routine's run,
// an automatic event, a playbook's turn and a message nobody is known to have sent get none:
// only a person's words are looked in, and only for what that person may see.
func (c *Conductor) briefTickets(ctx context.Context, j job, login string, ev InboundEvent) []BriefTicket {
	if c.tickets == nil || !j.onHost || login == "" || ev.Routine != nil || ev.Automatic {
		return nil
	}
	text := ev.RecordText
	if text == "" {
		text = ev.Text
	}
	if text == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, ticketsTimeout)
	defer cancel()
	tickets := c.tickets(ctx, login, ev.SourceKey, text)
	if len(tickets) > MaxBriefTickets {
		tickets = tickets[:MaxBriefTickets]
	}
	return tickets
}
