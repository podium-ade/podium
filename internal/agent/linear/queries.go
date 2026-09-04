package linear

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// The pagination arguments below are string constants because they are substituted into
// the query text, which stays a compile-time constant.
//
// pollPageSize is how many issues one tick's page asks for. It is Linear's own default and
// its maximum useful value here: the bot is assigned tickets by humans, one at a time.
const pollPageSize = "50"

// pollCommentsPerIssue is how many of an issue's newest comments the poll query carries.
//
// Linear bills complexity, a connection multiplies its children by the pagination argument
// and one query is hard-rejected above 10,000 points — so issues(first:50){comments(first:50)}
// is the shape to avoid. Twenty is the number of comments a human writes between two ticks
// with room to spare, and FetchTranscript reads the whole conversation properly anyway: the
// poll only has to notice that something new was said and by whom.
const pollCommentsPerIssue = "20"

// transcriptCommentsPerPage and maxTranscriptPages bound FetchTranscript. Five pages is 500
// comments; a longer issue than that is truncated at the oldest end, which is the same rule
// the brief applies.
const (
	transcriptCommentsPerPage = "100"
	maxTranscriptPages        = 5
)

// teamStatesPerTeam is how many workflow states one team is asked for. Linear's own
// templates ship five or six.
const teamStatesPerTeam = "50"

// Workflow state types. `WorkflowState.type` is a String, not an enum: filtering or
// switching on it means comparing to these literals.
const (
	StateTypeTriage    = "triage"
	StateTypeBacklog   = "backlog"
	StateTypeUnstarted = "unstarted"
	StateTypeStarted   = "started"
	StateTypeCompleted = "completed"
	StateTypeCanceled  = "canceled"
	StateTypeDuplicate = "duplicate"
)

// InProgressStateName is the state React(working) moves a ticket to, by name, because
// "which state means the bot has picked this up" is a team's convention and not something
// the schema records. The type: started fallback is what happens when a team renamed it.
const InProgressStateName = "In Progress"

// ---------------------------------------------------------------------------
// wire types
// ---------------------------------------------------------------------------

// User is a Linear user as the two queries here ask for one.
type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	IsMe        bool   `json:"isMe"`
}

// label is the name to show for a user, preferring what they typed.
func (u *User) label() string {
	if u == nil {
		return ""
	}
	for _, v := range []string{u.DisplayName, u.Name, u.ID} {
		if v != "" {
			return v
		}
	}
	return ""
}

// ActorBot is the non-human author of a comment. Its presence is how a comment written by
// an integration or an agent is told apart from one written by a person.
type ActorBot struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	UserDisplayName string `json:"userDisplayName"`
}

// Comment is one comment on an issue.
//
// User is NULLABLE and that is the important part: Linear's schema says it is "null for
// comments created by integrations or bots without a user association". A nil User is
// therefore never a human, and never starts a turn.
type Comment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	// ParentID is set on a reply. Threading is one level deep.
	ParentID *string `json:"parentId"`
	// QuotedText is set on an inline comment against a passage of the description.
	QuotedText *string   `json:"quotedText"`
	User       *User     `json:"user"`
	BotActor   *ActorBot `json:"botActor"`
}

// author is the display name to record for a comment, whoever wrote it.
func (c Comment) author() string {
	if name := c.User.label(); name != "" {
		return name
	}
	if c.BotActor != nil {
		for _, v := range []string{c.BotActor.UserDisplayName, c.BotActor.Name} {
			if v != "" {
				return v
			}
		}
	}
	return "unknown"
}

// WorkflowState is a column on a team's board.
type WorkflowState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type is one of the StateType constants. It is a String in the schema, not an enum.
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

// Team is the team an issue belongs to.
type Team struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// Issue is an issue as the poll query and the transcript query ask for one.
type Issue struct {
	ID          string        `json:"id"`
	Identifier  string        `json:"identifier"`
	Title       string        `json:"title"`
	Description *string       `json:"description"`
	URL         string        `json:"url"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
	State       WorkflowState `json:"state"`
	Team        Team          `json:"team"`
	Assignee    *User         `json:"assignee"`
	Comments    struct {
		PageInfo pageInfo  `json:"pageInfo"`
		Nodes    []Comment `json:"nodes"`
	} `json:"comments"`
}

// closed reports whether the issue is finished as far as the bot is concerned: a completed
// or cancelled ticket gets no turns, however it was edited.
func (i Issue) closed() bool {
	return i.State.Type == StateTypeCompleted || i.State.Type == StateTypeCanceled
}

// brief is the ticket as one block of text: the identifier first, because the coder skill
// is told to branch from it and the runtime's prompt renders the transcript and the
// instruction but not source.ref.
func (i Issue) brief() string {
	head := i.Title
	if i.Identifier != "" {
		head = i.Identifier + " — " + i.Title
	}
	if i.Description == nil || *i.Description == "" {
		return head
	}
	return head + "\n\n" + *i.Description
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// ---------------------------------------------------------------------------
// the operations
// ---------------------------------------------------------------------------

const viewerQuery = `query PodiumViewer {
  viewer { id name displayName isMe }
}`

// Viewer is who the API key belongs to. It is the bot user: every filter below is "assigned
// to this id".
func (c *Client) Viewer(ctx context.Context) (User, error) {
	var out struct {
		Viewer User `json:"viewer"`
	}
	if err := c.do(ctx, "PodiumViewer", viewerQuery, nil, &out); err != nil {
		return User{}, err
	}
	if out.Viewer.ID == "" {
		return User{}, errors.New("linear: viewer returned no user id")
	}
	return out.Viewer, nil
}

// assignedIssuesQuery is one tick's page.
//
// `orderBy: updatedAt` is DESCENDING and there is no ascending option — PaginationOrderBy
// has exactly two values and the sort argument is internal. So the newest issue is the
// first node of the first page, which is why the watermark can only be written once every
// page of a tick has been handed over.
//
// There is no assignedAt field and no assignment-time comparator, so "newly assigned to
// me" is NOT expressible here: this filter fires on any change to an assigned issue and
// the conductor's own sessions table is what turns that into an assignment or a follow-up.
//
// A bot on an OAuth actor=app seat would be the issue's `delegate` rather than its
// `assignee`. This is a personal API key on a normal user seat, so `assignee` is right.
const assignedIssuesQuery = `query PodiumAssignedIssues($me: ID!, $since: DateTimeOrDuration!, $after: String) {
  issues(
    first: ` + pollPageSize + `
    after: $after
    orderBy: updatedAt
    includeArchived: false
    filter: { assignee: { id: { eq: $me } }, updatedAt: { gt: $since } }
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      identifier
      title
      description
      url
      updatedAt
      state { id name type }
      team { id key }
      assignee { id name displayName isMe }
      comments(first: ` + pollCommentsPerIssue + `) {
        nodes {
          id
          body
          createdAt
          parentId
          quotedText
          user { id name displayName isMe }
          botActor { id name userDisplayName }
        }
      }
    }
  }
}`

// AssignedIssues is one page of issues assigned to userID and touched after since.
func (c *Client) AssignedIssues(
	ctx context.Context, userID string, since time.Time, after string,
) (issues []Issue, next string, err error) {
	vars := map[string]any{
		"me": userID,
		// DateTimeOrDuration accepts an ISO timestamp as well as a duration like "-PT10M".
		"since": since.UTC().Format(time.RFC3339Nano),
	}
	if after != "" {
		vars["after"] = after
	}
	var out struct {
		Issues struct {
			PageInfo pageInfo `json:"pageInfo"`
			Nodes    []Issue  `json:"nodes"`
		} `json:"issues"`
	}
	if err := c.do(ctx, "PodiumAssignedIssues", assignedIssuesQuery, vars, &out); err != nil {
		return nil, "", err
	}
	if out.Issues.PageInfo.HasNextPage {
		next = out.Issues.PageInfo.EndCursor
	}
	return out.Issues.Nodes, next, nil
}

// issueQuery reads one issue and a page of its comments. `issue(id:)` accepts a UUID or an
// identifier like ENG-123; this code always passes the UUID.
const issueQuery = `query PodiumIssue($id: String!, $after: String) {
  issue(id: $id) {
    id
    identifier
    title
    description
    url
    createdAt
    updatedAt
    state { id name type }
    team { id key }
    assignee { id name displayName isMe }
    comments(first: ` + transcriptCommentsPerPage + `, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        id
        body
        createdAt
        parentId
        quotedText
        user { id name displayName isMe }
        botActor { id name userDisplayName }
      }
    }
  }
}`

// Issue reads one issue with every comment on it, oldest first.
//
// The comments connection is newest-first and has no ascending option either, so the pages
// are collected and then sorted here. maxTranscriptPages bounds the cost; past it the
// OLDEST comments are the ones missing, which is the same end the brief truncates from.
func (c *Client) Issue(ctx context.Context, id string) (Issue, error) {
	var issue Issue
	after := ""
	var comments []Comment
	for page := 0; page < maxTranscriptPages; page++ {
		vars := map[string]any{"id": id}
		if after != "" {
			vars["after"] = after
		}
		var out struct {
			Issue *Issue `json:"issue"`
		}
		if err := c.do(ctx, "PodiumIssue", issueQuery, vars, &out); err != nil {
			return Issue{}, err
		}
		if out.Issue == nil {
			return Issue{}, fmt.Errorf("linear: no issue %s", id)
		}
		if page == 0 {
			issue = *out.Issue
		}
		comments = append(comments, out.Issue.Comments.Nodes...)
		if !out.Issue.Comments.PageInfo.HasNextPage || out.Issue.Comments.PageInfo.EndCursor == "" {
			break
		}
		after = out.Issue.Comments.PageInfo.EndCursor
	}
	sort.SliceStable(comments, func(i, j int) bool { return comments[i].CreatedAt.Before(comments[j].CreatedAt) })
	issue.Comments.Nodes = comments
	issue.Comments.PageInfo = pageInfo{}
	return issue, nil
}

const teamStatesQuery = `query PodiumTeamStates($id: String!) {
  team(id: $id) {
    id
    key
    states(first: ` + teamStatesPerTeam + `) {
      nodes { id name type position }
    }
  }
}`

// TeamStates is one team's workflow states.
func (c *Client) TeamStates(ctx context.Context, teamID string) ([]WorkflowState, error) {
	var out struct {
		Team *struct {
			States struct {
				Nodes []WorkflowState `json:"nodes"`
			} `json:"states"`
		} `json:"team"`
	}
	if err := c.do(ctx, "PodiumTeamStates", teamStatesQuery, map[string]any{"id": teamID}, &out); err != nil {
		return nil, err
	}
	if out.Team == nil {
		return nil, fmt.Errorf("linear: no team %s", teamID)
	}
	return out.Team.States.Nodes, nil
}

const commentCreateMutation = `mutation PodiumAddComment($id: String!, $issueId: String!, $body: String!) {
  commentCreate(input: { id: $id, issueId: $issueId, body: $body }) {
    success
    comment { id }
  }
}`

// AddComment posts one comment and returns its id.
//
// The id is supplied by this client rather than Linear: CommentCreateInput takes a
// client-generated UUIDv4 precisely so a create can be repeated without duplicating, and
// the conductor's relayed ledger is the other half of the same guarantee.
func (c *Client) AddComment(ctx context.Context, issueID, body string) (string, error) {
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	var out struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment struct {
				ID string `json:"id"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	vars := map[string]any{"id": id, "issueId": issueID, "body": body}
	if err := c.do(ctx, "PodiumAddComment", commentCreateMutation, vars, &out); err != nil {
		return "", err
	}
	if !out.CommentCreate.Success {
		return "", fmt.Errorf("linear: commentCreate on %s reported no success", issueID)
	}
	if out.CommentCreate.Comment.ID != "" {
		return out.CommentCreate.Comment.ID, nil
	}
	// The id we asked for is the id it has, so a payload that omits it is still usable.
	return id, nil
}

const commentUpdateMutation = `mutation PodiumEditComment($id: String!, $body: String!) {
  commentUpdate(id: $id, input: { body: $body }) {
    success
    comment { id }
  }
}`

// EditComment replaces a comment's body.
func (c *Client) EditComment(ctx context.Context, commentID, body string) error {
	var out struct {
		CommentUpdate struct {
			Success bool `json:"success"`
		} `json:"commentUpdate"`
	}
	vars := map[string]any{"id": commentID, "body": body}
	if err := c.do(ctx, "PodiumEditComment", commentUpdateMutation, vars, &out); err != nil {
		return err
	}
	if !out.CommentUpdate.Success {
		return fmt.Errorf("linear: commentUpdate on %s reported no success", commentID)
	}
	return nil
}

const issueUpdateMutation = `mutation PodiumMoveIssue($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) {
    success
  }
}`

// MoveIssue puts an issue in a workflow state.
func (c *Client) MoveIssue(ctx context.Context, issueID, stateID string) error {
	var out struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	vars := map[string]any{"id": issueID, "stateId": stateID}
	if err := c.do(ctx, "PodiumMoveIssue", issueUpdateMutation, vars, &out); err != nil {
		return err
	}
	if !out.IssueUpdate.Success {
		return fmt.Errorf("linear: issueUpdate on %s reported no success", issueID)
	}
	return nil
}

// UploadTarget is where an attachment's bytes go and how to address it afterwards.
type UploadTarget struct {
	// UploadURL is a pre-signed PUT.
	UploadURL string
	// AssetURL is the permanent URL to put in a comment body.
	AssetURL string
	// Headers must be sent verbatim with the PUT.
	Headers []UploadHeader
}

// UploadHeader is one header fileUpload requires on the PUT.
type UploadHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

const fileUploadMutation = `mutation PodiumUploadFile($contentType: String!, $filename: String!, $size: Int!) {
  fileUpload(contentType: $contentType, filename: $filename, size: $size) {
    success
    uploadFile {
      uploadUrl
      assetUrl
      headers { key value }
    }
  }
}`

// PrepareUpload asks Linear for somewhere to put a file.
//
// This mutation is the ONE operation in this file that could not be validated against
// Linear's published schema: the verified surface notes cover every query above and stop
// short of fileUpload. The shape below is what Linear documents. Every caller treats a
// failure here as "fall back to a task link" rather than as a failed turn, so a schema that
// has moved costs a nicer comment and nothing else.
func (c *Client) PrepareUpload(ctx context.Context, filename, contentType string, size int64) (UploadTarget, error) {
	var out struct {
		FileUpload struct {
			Success    bool `json:"success"`
			UploadFile *struct {
				UploadURL string         `json:"uploadUrl"`
				AssetURL  string         `json:"assetUrl"`
				Headers   []UploadHeader `json:"headers"`
			} `json:"uploadFile"`
		} `json:"fileUpload"`
	}
	vars := map[string]any{"contentType": contentType, "filename": filename, "size": size}
	if err := c.do(ctx, "PodiumUploadFile", fileUploadMutation, vars, &out); err != nil {
		return UploadTarget{}, err
	}
	file := out.FileUpload.UploadFile
	if !out.FileUpload.Success || file == nil || file.UploadURL == "" || file.AssetURL == "" {
		return UploadTarget{}, fmt.Errorf("linear: fileUpload for %s returned no upload target", filename)
	}
	return UploadTarget{UploadURL: file.UploadURL, AssetURL: file.AssetURL, Headers: file.Headers}, nil
}
