// Package tickets decides which Linear issues a person in Podium may be shown, and reads them
// for the chat's chips and cards, an issue conversation's panel, and the assistant's brief
// (docs/agent.md, "Tickets in a chat").
//
// The Linear app's token reads every team in the workspace, so what it can read is no guide to
// what a person may. The rule is the one Linear would apply, as near as Podium can tell it:
//
//   - an admin or owner of Podium may see every issue, as they read every issue's conversation;
//   - somebody who touched the issue in Linear (linear_people, through their linked Linear
//     account) or owns a routine that ran in it may see it, as they read its conversation;
//   - somebody whose linked Linear account is on the issue's team may see it.
//
// Anybody else is told there is no such issue, exactly as if it did not exist: a title is
// information, and so is the fact that an identifier is real.
package tickets

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/linear"
	"github.com/podium-ade/podium/internal/agent/store"
)

// ErrNotFound is an issue that does not exist, or that the person asking may not see. The two
// are deliberately the same.
var ErrNotFound = errors.New("no such Linear issue")

// App is the running Linear app, as reading tickets needs it (linear.AppSource).
type App interface {
	// Ticket reads one issue by identifier or id; linear.ErrNoIssue when Linear has none.
	Ticket(ctx context.Context, ref string) (linear.Ticket, error)
	// MemberTeams is the team keys a Linear account ("<organization>/<user>") is on.
	MemberTeams(ctx context.Context, account string) ([]string, error)
	// TeamKeys is the workspace's team keys.
	TeamKeys(ctx context.Context) ([]string, error)
}

// Store is what reading tickets needs from the conductor's database.
type Store interface {
	FindLinearIssue(ctx context.Context, identifier, linearID string) (store.LinearIssue, error)
	LinearIssueTeamKeys(ctx context.Context) ([]string, error)
	LinearTouched(ctx context.Context, sourceKey, login string) (bool, error)
	IdentitiesOf(ctx context.Context, logins []string) ([]store.Identity, error)
	// For who reads the answer a ticket would be briefed into.
	GetLinearIssue(ctx context.Context, sourceKey string) (store.LinearIssue, error)
	GetChat(ctx context.Context, id string) (store.Chat, error)
	ListChatAccess(ctx context.Context, chatID string) ([]store.ChatGrant, error)
	ChatParticipants(ctx context.Context, chatID string) ([]store.ChatParticipant, error)
}

// Viewer is who is asking. Admin is an admin or owner of Podium, or the dev token.
type Viewer struct {
	Login string
	Admin bool
}

// Issue is one issue as a person may be shown it. Cached is an issue described from what the
// webhooks said, because Linear was not asked or did not answer: it has no assignee, labels,
// links, description or comments.
type Issue struct {
	linear.Ticket
	Cached bool
}

// Key is the issue's conversation.
func (i Issue) Key() string { return store.LinearIssueKey(i.ID) }

// Options is what New needs.
type Options struct {
	Store Store
	// App is the Linear app now, or nil when none runs here. It is asked each time, because a
	// connection saved or cleared in Settings changes it while the conductor runs.
	App    func() App
	Logger *slog.Logger
}

// Reader reads tickets for people.
type Reader struct {
	store  Store
	app    func() App
	logger *slog.Logger
}

// New returns a Reader.
func New(opts Options) *Reader {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	app := opts.App
	if app == nil {
		app = func() App { return nil }
	}
	return &Reader{store: opts.Store, app: app, logger: logger}
}

// MayRead is the rule: an admin, somebody who touched the issue, or somebody on its team.
// memberTeams are the team keys the viewer's linked Linear accounts are on.
func MayRead(admin, touched bool, memberTeams []string, teamKey string) bool {
	if admin || touched {
		return true
	}
	if teamKey == "" {
		return false
	}
	return slices.ContainsFunc(memberTeams, func(k string) bool { return strings.EqualFold(k, teamKey) })
}

// KnownTeams is the team keys an identifier may be of: the workspace's, when the app can say,
// and every team an issue the webhooks described is in.
func (r *Reader) KnownTeams(ctx context.Context) map[string]bool {
	known := map[string]bool{}
	if app := r.app(); app != nil {
		keys, err := app.TeamKeys(ctx)
		if err != nil {
			r.logger.DebugContext(ctx, "tickets: reading the workspace's team keys failed", "error", err)
		}
		for _, k := range keys {
			known[strings.ToUpper(k)] = true
		}
	}
	keys, err := r.store.LinearIssueTeamKeys(ctx)
	if err != nil {
		r.logger.DebugContext(ctx, "tickets: listing the teams of known issues failed", "error", err)
	}
	for _, k := range keys {
		known[strings.ToUpper(k)] = true
	}
	return known
}

// Lookup is one issue for viewer, or ErrNotFound when there is none or viewer may not see it.
//
// detail false is a chip: what the webhooks said is enough when they said the title, team and
// state, and Linear is asked only when they did not. detail true is a card, a panel or a brief:
// Linear is asked (at most once a minute per issue, linear.AppSource.Ticket), and what the
// webhooks said stands in when it cannot be.
func (r *Reader) Lookup(ctx context.Context, v Viewer, ref linear.IssueRef, detail bool) (Issue, error) {
	if v.Login == "" && !v.Admin {
		return Issue{}, ErrNotFound
	}
	issue, err := r.read(ctx, ref, detail)
	if err != nil {
		return Issue{}, err
	}
	ok, err := r.mayRead(ctx, v, issue)
	if err != nil {
		return Issue{}, err
	}
	if !ok {
		return Issue{}, ErrNotFound
	}
	return issue, nil
}

// read is the issue, from the webhooks' record or from Linear, before anybody is asked about.
func (r *Reader) read(ctx context.Context, ref linear.IssueRef, detail bool) (Issue, error) {
	row, rowErr := r.store.FindLinearIssue(ctx, ref.Identifier, ref.ID)
	if rowErr != nil && !errors.Is(rowErr, store.ErrNotFound) {
		return Issue{}, rowErr
	}
	haveRow := rowErr == nil
	complete := haveRow && row.Title != "" && row.TeamKey != "" && row.StateName != ""
	if complete && !detail {
		return fromRow(row), nil
	}
	app := r.app()
	if app == nil {
		if haveRow {
			return fromRow(row), nil
		}
		return Issue{}, ErrNotFound
	}
	t, err := app.Ticket(ctx, ref.String())
	switch {
	case err == nil:
		return Issue{Ticket: t}, nil
	case errors.Is(err, linear.ErrNoIssue):
		return Issue{}, ErrNotFound
	case haveRow:
		r.logger.DebugContext(ctx, "tickets: reading an issue from Linear failed; using what the webhooks said",
			"issue", ref.String(), "error", err)
		return fromRow(row), nil
	default:
		return Issue{}, err
	}
}

// mayRead applies MayRead, asking only as much as it takes: the admin bit, then the database,
// then Linear.
func (r *Reader) mayRead(ctx context.Context, v Viewer, issue Issue) (bool, error) {
	if v.Admin {
		return true, nil
	}
	touched, err := r.store.LinearTouched(ctx, issue.Key(), v.Login)
	if err != nil {
		return false, err
	}
	if touched {
		return true, nil
	}
	if issue.TeamKey == "" {
		return false, nil
	}
	teams, err := r.memberTeams(ctx, v.Login)
	if err != nil {
		return false, err
	}
	return MayRead(false, false, teams, issue.TeamKey), nil
}

// memberTeams is the team keys login's linked Linear accounts are on. An account Linear cannot
// be asked about is on none: not knowing is not seeing.
func (r *Reader) memberTeams(ctx context.Context, login string) ([]string, error) {
	app := r.app()
	if app == nil {
		return nil, nil
	}
	ids, err := r.store.IdentitiesOf(ctx, []string{login})
	if err != nil {
		return nil, err
	}
	var teams []string
	for _, id := range ids {
		if id.Provider != store.ProviderLinear || id.Login != login {
			continue
		}
		keys, err := app.MemberTeams(ctx, id.ExternalID)
		if err != nil {
			r.logger.DebugContext(ctx, "tickets: reading a person's Linear teams failed",
				"login", login, "error", err)
			continue
		}
		teams = append(teams, keys...)
	}
	return teams, nil
}

func fromRow(row store.LinearIssue) Issue {
	return Issue{Cached: true, Ticket: linear.Ticket{
		ID: row.LinearID, Identifier: row.Identifier, Title: row.Title, URL: row.URL,
		TeamKey: row.TeamKey, StateName: row.StateName, StateType: row.StateType, UpdatedAt: row.UpdatedAt,
	}}
}

// What a brief carries of one ticket, and of all of them.
const (
	maxTicketDescription = 4 * 1024
	maxTicketComment     = 1024
	maxTicketComments    = 5
	maxTicketsBytes      = 12 * 1024
)

// BriefTickets is conductor.TicketBriefer: the tickets text names that login may see AND that
// everybody who will read the answer in the conversation sourceKey could see, leaving out the
// issue sourceKey is the conversation of, in the brief's form. withheld counts the tickets login
// may see that were left out only because somebody else reading the answer may not (audience).
// Anything it cannot tell it leaves out.
func (r *Reader) BriefTickets(ctx context.Context, login, sourceKey, text string) (out []conductor.BriefTicket, withheld int) {
	known := r.KnownTeams(ctx)
	if len(known) == 0 {
		return nil, 0
	}
	refs := linear.FindIssueRefs(text, func(k string) bool { return known[k] }, 2*conductor.MaxBriefTickets+1)
	if len(refs) == 0 {
		return nil, 0
	}
	aud := r.audienceOf(ctx, login, sourceKey)
	budget := maxTicketsBytes
	for _, ref := range refs {
		if len(out) == conductor.MaxBriefTickets {
			break
		}
		issue, err := r.Lookup(ctx, Viewer{Login: login}, ref, true)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				r.logger.DebugContext(ctx, "tickets: briefing a ticket failed", "issue", ref.String(), "error", err)
			}
			continue
		}
		if issue.Key() == sourceKey {
			continue
		}
		if !aud.mayAllRead(ctx, issue) {
			withheld++
			continue
		}
		bt := BriefTicket(issue, budget)
		budget -= briefSize(bt)
		out = append(out, bt)
		if budget <= 0 {
			break
		}
	}
	return out, withheld
}

// MaxBriefReaders is how many people a shared web chat may have for the tickets its messages
// name to be briefed at all: each of them is asked about each ticket, and a chat read by more
// is read by too many to vouch for.
const MaxBriefReaders = 20

// audience is who will read the answer to a turn, as far as briefing it with a ticket goes. The
// answer is read by everybody who can read the conversation, and where the conversation lives
// outside Podium it is posted there: a ticket the speaker may see is briefed only when every one
// of them could see it too, or the assistant would quote it to people Linear keeps it from.
type audience struct {
	r *Reader
	// none is a conversation whose readers cannot be told: a Slack thread, a pull request, a
	// Linear project's conversation, a chat open to every member or to anybody with its link.
	// Nothing is briefed in it.
	none bool
	// team, when set, is a Linear issue's conversation: the answer is a comment on the issue,
	// read by whoever can see it in Linear, which is whoever can see its team. Only tickets of
	// that team are briefed.
	team string
	// readers are the web chat's readers other than the speaker, each of whom must be able to
	// see a ticket for it to be briefed. Empty, with none and team unset, is a chat only the
	// speaker reads.
	readers []string
	// teams remembers each reader's Linear teams for the length of one brief.
	teams map[string][]string
}

// audienceOf is who reads the answer in the conversation sourceKey, login asking.
func (r *Reader) audienceOf(ctx context.Context, login, sourceKey string) *audience {
	none := &audience{r: r, none: true}
	if kind, _ := store.LinearKind(sourceKey); kind != "" {
		if kind != store.LinearKindIssue {
			return none
		}
		li, err := r.store.GetLinearIssue(ctx, sourceKey)
		if err != nil || li.TeamKey == "" {
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				r.logger.DebugContext(ctx, "tickets: reading a conversation's issue failed", "source_key", sourceKey, "error", err)
			}
			return none
		}
		return &audience{r: r, team: li.TeamKey}
	}
	chatID, ok := strings.CutPrefix(sourceKey, store.ChatSourceKeyPrefix)
	if !ok || chatID == "" {
		return none
	}
	readers, err := r.chatReaders(ctx, chatID)
	if err != nil {
		r.logger.DebugContext(ctx, "tickets: listing a chat's readers failed", "chat_id", chatID, "error", err)
		return none
	}
	if readers == nil {
		return none
	}
	var others []string
	for _, l := range readers {
		if l != login && !slices.Contains(others, l) {
			others = append(others, l)
		}
	}
	if len(others) > MaxBriefReaders {
		return none
	}
	return &audience{r: r, readers: others, teams: map[string][]string{}}
}

// chatReaders is everybody who can read a web chat by name: its owner, everybody it is shared
// with, and everybody who took part. nil, with no error, is a chat whose readers cannot be
// named: one open to every member or to anybody with its link, one with no owner, or one with
// a participant who is not a Podium person. An admin or owner of Podium reads every chat too,
// and may see every ticket, so they are nobody to check.
func (r *Reader) chatReaders(ctx context.Context, chatID string) ([]string, error) {
	chat, err := r.store.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if chat.Login == "" || (chat.GeneralAccess != "" && chat.GeneralAccess != store.AccessRestricted) {
		return nil, nil
	}
	out := []string{chat.Login}
	grants, err := r.store.ListChatAccess(ctx, chatID)
	if err != nil {
		return nil, err
	}
	for _, g := range grants {
		out = append(out, g.Login)
	}
	people, err := r.store.ChatParticipants(ctx, chatID)
	if err != nil {
		return nil, err
	}
	for _, p := range people {
		if p.Login == "" {
			return nil, nil
		}
		out = append(out, p.Login)
	}
	return out, nil
}

// mayAllRead reports whether everybody in the audience could see issue.
func (a *audience) mayAllRead(ctx context.Context, issue Issue) bool {
	switch {
	case a.none:
		return false
	case a.team != "":
		return strings.EqualFold(issue.TeamKey, a.team)
	}
	for _, login := range a.readers {
		ok, err := a.mayRead(ctx, login, issue)
		if err != nil {
			a.r.logger.DebugContext(ctx, "tickets: asking whether a chat's reader may see a ticket failed",
				"issue", issue.Identifier, "error", err)
			return false
		}
		if !ok {
			return false
		}
	}
	return true
}

// mayRead is Reader.mayRead for one reader, remembering their teams across the brief's tickets.
func (a *audience) mayRead(ctx context.Context, login string, issue Issue) (bool, error) {
	teams, seen := a.teams[login]
	if !seen {
		touched, err := a.r.store.LinearTouched(ctx, issue.Key(), login)
		if err != nil || touched {
			return touched, err
		}
		teams, err = a.r.memberTeams(ctx, login)
		if err != nil {
			return false, err
		}
		a.teams[login] = teams
		return MayRead(false, false, teams, issue.TeamKey), nil
	}
	if MayRead(false, false, teams, issue.TeamKey) {
		return true, nil
	}
	return a.r.store.LinearTouched(ctx, issue.Key(), login)
}

// BriefTicket is one issue as a brief carries it, its description and comments trimmed to fit
// maxTicketDescription, maxTicketComment, and budget bytes for the whole.
func BriefTicket(issue Issue, budget int) conductor.BriefTicket {
	bt := conductor.BriefTicket{
		Identifier: issue.Identifier,
		Title:      oneLine(issue.Title, 300),
		URL:        issue.URL,
		Team:       issue.TeamKey,
		State:      issue.StateName,
		Assignee:   issue.Assignee,
		Labels:     issue.Labels,
	}
	if !issue.UpdatedAt.IsZero() {
		bt.UpdatedAt = issue.UpdatedAt.UTC().Format(time.RFC3339)
	}
	var trimmed bool
	bt.Description, trimmed = clip(issue.Description, min(maxTicketDescription, max(0, budget/2)))
	bt.Trimmed = bt.Trimmed || trimmed
	left := budget - briefSize(bt)
	comments := issue.Comments
	if len(comments) > maxTicketComments {
		comments = comments[len(comments)-maxTicketComments:]
		bt.Trimmed = true
	}
	// The latest are kept when the budget runs out: they are what was said last.
	var kept []conductor.BriefTicketComment
	for i := len(comments) - 1; i >= 0; i-- {
		c := comments[i]
		body, cut := clip(c.Body, maxTicketComment)
		bc := conductor.BriefTicketComment{Author: oneLine(c.Author, 100), At: c.At.UTC().Format(time.RFC3339), Body: body}
		size := len(bc.Author) + len(bc.At) + len(bc.Body) + 40
		if size > left {
			bt.Trimmed = true
			break
		}
		left -= size
		bt.Trimmed = bt.Trimmed || cut
		kept = append(kept, bc)
	}
	slices.Reverse(kept)
	bt.Comments = kept
	return bt
}

// briefSize is roughly what a ticket adds to a brief, in bytes of JSON.
func briefSize(bt conductor.BriefTicket) int {
	n := 120 + len(bt.Identifier) + len(bt.Title) + len(bt.URL) + len(bt.Team) + len(bt.State) +
		len(bt.Assignee) + len(bt.UpdatedAt) + len(bt.Description)
	for _, l := range bt.Labels {
		n += len(l) + 3
	}
	for _, c := range bt.Comments {
		n += len(c.Author) + len(c.At) + len(c.Body) + 40
	}
	return n
}

// clip cuts text to at most limit bytes on a rune boundary, and says whether it did.
func clip(text string, limit int) (string, bool) {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut]) + "…", true
}

// oneLine is text on one line, at most limit bytes.
func oneLine(text string, limit int) string {
	flat, _ := clip(strings.Join(strings.Fields(text), " "), limit)
	return flat
}
