package linear

// What the deliveries say about an issue or a project, kept for its conversation (docs/agent.md,
// "Linear"): its identifier, title, link, team and state, which the Linear view lists without
// asking Linear about any row, and who touched it, which is who may read its conversation.
//
// Every agent session event names its issue, and with resource types ticked on the webhook every
// Issue, Comment, Project and ProjectUpdate change does too. A person is named by their Linear
// account, "<organization>/<user>" as identities keys one; which Podium login that is, if any,
// is the store's to answer when somebody reads, so an account linked later counts from then on
// and one never linked counts for nobody.

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// How somebody touched an issue, as IssuePerson.Via. The store keeps the same words.
const (
	ViaCreated   = "created"
	ViaAssigned  = "assigned"
	ViaDelegated = "delegated"
	ViaMentioned = "mentioned"
	ViaPrompted  = "prompted"
	ViaCommented = "commented"
	ViaChanged   = "changed"
)

// What a conversation is about.
const (
	IssueKindIssue   = "issue"
	IssueKindProject = "project"
)

// issueSeenTimeout bounds the sink's write for one delivery.
const issueSeenTimeout = 10 * time.Second

// IssueRecord is what one delivery said about an issue or project.
type IssueRecord struct {
	// Kind is IssueKindIssue or IssueKindProject, and ID its Linear id.
	Kind string
	ID   string
	// Identifier is an issue's ("ENG-12"). Title, URL, TeamKey and the state are what the
	// delivery carried; an empty one says nothing.
	Identifier string
	Title      string
	URL        string
	TeamKey    string
	StateName  string
	StateType  string
	// People are who the delivery says touched it.
	People []IssuePerson
	At     time.Time
}

// Key is the conversation the record is about.
func (r IssueRecord) Key() string {
	if r.Kind == IssueKindProject {
		return ProjectKey(r.ID)
	}
	return SourceKey(r.ID)
}

// IssuePerson is one Linear account that touched an issue, and how.
type IssuePerson struct {
	// UserID is "<organization>/<user>", the identities key of a Linear account.
	UserID string
	Via    string
}

// IssueSink keeps what the deliveries say about issues and projects. The conductor's store
// implements it.
type IssueSink interface {
	LinearIssueSeen(ctx context.Context, r IssueRecord) error
}

// ProjectKey is the conversation of one Linear project: the routine runs about it.
func ProjectKey(projectID string) string { return Kind + ":project:" + projectID }

// issueSeen hands a record to the sink, and logs a failure: what the view shows is a
// convenience, and the turn or the routine goes on without it.
func (s *AppSource) issueSeen(ctx context.Context, r *IssueRecord) {
	if s.issues == nil || r == nil || r.ID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, issueSeenTimeout)
	defer cancel()
	if err := s.issues.LinearIssueSeen(ctx, *r); err != nil {
		s.logger.WarnContext(ctx, "linear: recording what a delivery said about an issue failed",
			"kind", r.Kind, "id", r.ID, "error", err)
	}
}

// person is a Linear user as the issue's people name one, or the zero person for nobody, or for
// the app itself.
func (s *AppSource) person(orgID, userID, via string) []IssuePerson {
	if orgID == "" || userID == "" || userID == s.appUserID {
		return nil
	}
	return []IssuePerson{{UserID: orgID + "/" + userID, Via: via}}
}

// sessionIssue is what an agent session event says about its issue, and who touched it: the
// person who opened the session, as delegating or mentioning; whoever wrote the prompt; and the
// issue's creator and assignee when the payload names them.
func (s *AppSource) sessionIssue(p *webhookPayload) *IssueRecord {
	sess := p.AgentSession
	if sess == nil || sess.Issue == nil || sess.Issue.ID == "" {
		return nil
	}
	iss := sess.Issue
	r := &IssueRecord{
		Kind: IssueKindIssue, ID: iss.ID, Identifier: iss.Identifier, Title: iss.Title, URL: iss.URL,
		At: s.now().UTC(),
	}
	if iss.Team != nil {
		r.TeamKey = iss.Team.Key
	}
	org := p.OrganizationID
	if p.Action == ActionCreated && sess.Creator != nil {
		via := ViaMentioned
		if sess.delegated() {
			via = ViaDelegated
		}
		r.People = append(r.People, s.person(org, sess.Creator.ID, via)...)
	}
	if p.AgentActivity != nil && p.AgentActivity.User != nil {
		r.People = append(r.People, s.person(org, p.AgentActivity.User.ID, ViaPrompted)...)
	}
	r.People = append(r.People, s.person(org, firstNonEmpty(iss.CreatorID, userID(iss.Creator)), ViaCreated)...)
	r.People = append(r.People, s.person(org, firstNonEmpty(iss.AssigneeID, userID(iss.Assignee)), ViaAssigned)...)
	return r
}

func userID(u *payloadUser) string {
	if u == nil {
		return ""
	}
	return u.ID
}

// dataEntity is as much of a data change's entity as says what issue or project it is about,
// and who touched it. Linear documents no exact payload, so it is read leniently: a field that
// is missing says nothing.
type dataEntity struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	CreatorID  string `json:"creatorId"`
	AssigneeID string `json:"assigneeId"`
	UserID     string `json:"userId"`
	IssueID    string `json:"issueId"`
	ProjectID  string `json:"projectId"`
	State      *struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Issue *struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		URL        string `json:"url"`
	} `json:"issue"`
	Project *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"project"`
}

// dataIssue is what a data change says about the issue or project it is about, and who touched
// it: an Issue's own fields, creator and assignee; a Comment's issue and author; a Project and a
// ProjectUpdate likewise. Whoever made the change touched it too, when it was a person. Any
// other entity is about no conversation, and is nil. teamKey is the entity's team, read before.
func (s *AppSource) dataIssue(p *webhookPayload, teamKey string) *IssueRecord {
	var d dataEntity
	if json.Unmarshal(p.Data, &d) != nil {
		return nil
	}
	org := p.OrganizationID
	actor := ""
	if p.Actor != nil && (p.Actor.Type == "" || strings.EqualFold(p.Actor.Type, "user")) {
		actor = p.Actor.ID
	}
	r := &IssueRecord{At: s.now().UTC()}
	switch p.Type {
	case "Issue":
		r.Kind, r.ID, r.Identifier, r.Title, r.URL, r.TeamKey = IssueKindIssue, d.ID, d.Identifier, d.Title,
			firstNonEmpty(d.URL, p.URL), teamKey
		if d.State != nil {
			r.StateName, r.StateType = d.State.Name, d.State.Type
		}
		r.People = append(r.People, s.person(org, d.CreatorID, ViaCreated)...)
		r.People = append(r.People, s.person(org, d.AssigneeID, ViaAssigned)...)
		via := ViaChanged
		if p.Action == ActionCreate {
			via = ViaCreated
		}
		r.People = append(r.People, s.person(org, actor, via)...)
	case "Comment":
		r.Kind, r.ID, r.TeamKey = IssueKindIssue, d.IssueID, teamKey
		if d.Issue != nil {
			r.ID = firstNonEmpty(r.ID, d.Issue.ID)
			r.Identifier, r.Title, r.URL = d.Issue.Identifier, d.Issue.Title, d.Issue.URL
		}
		r.People = append(r.People, s.person(org, d.UserID, ViaCommented)...)
		r.People = append(r.People, s.person(org, actor, ViaCommented)...)
	case "Project":
		r.Kind, r.ID, r.Title, r.URL = IssueKindProject, d.ID, d.Name, firstNonEmpty(d.URL, p.URL)
		r.People = append(r.People, s.person(org, d.CreatorID, ViaCreated)...)
		r.People = append(r.People, s.person(org, actor, ViaChanged)...)
	case "ProjectUpdate":
		r.Kind, r.ID = IssueKindProject, d.ProjectID
		if d.Project != nil {
			r.ID = firstNonEmpty(r.ID, d.Project.ID)
			r.Title, r.URL = d.Project.Name, d.Project.URL
		}
		r.People = append(r.People, s.person(org, d.UserID, ViaCommented)...)
		r.People = append(r.People, s.person(org, actor, ViaCommented)...)
	default:
		return nil
	}
	if r.ID == "" {
		return nil
	}
	if p.Action == ActionRemove {
		// What a removal says about the entity is that it is gone; its last title stands.
		r.Identifier, r.Title, r.StateName, r.StateType = "", "", "", ""
	}
	return r
}
