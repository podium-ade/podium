// Package memory is the conductor's client of Hindsight, the agents' shared memory. Podium
// stores no memory of its own: this file is a few hundred lines of net/http against
// Hindsight's REST API and that is the whole of Podium's memory code.
//
// Everything read back out of here is CONTENT. A memory was written by a task, from
// material a human or a repository or a ticket supplied, and nothing in Podium interprets
// it — the API hands it to the UI and the UI shows it to a person, whose job is to notice
// a memory that should not be there and forget it.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds one call. Recall runs an embedding and a rerank; retain hands work
// to Hindsight's own worker and returns immediately.
const DefaultTimeout = 15 * time.Second

// maxErrorBody is how much of a failure response is quoted in an error. Hindsight answers
// with {"detail": …}, which is short; a captive portal is not.
const maxErrorBody = 2 << 10

// DefaultLimit is how many memories a page holds when the caller does not say.
const DefaultLimit = 50

// MaxLimit caps a page. Hindsight's own default is 100.
const MaxLimit = 200

// Fact types Hindsight classifies a memory as. Only world and experience are curatable;
// an observation is derived by consolidation and disappears with the facts under it.
const (
	FactWorld       = "world"
	FactExperience  = "experience"
	FactObservation = "observation"
)

// ErrUnauthorized is a 401 or 403 from Hindsight: the API key is wrong, or there is no key
// and the tenant extension is on. It is separated because it is the one failure an operator
// fixes by editing .env rather than by reading a log.
var ErrUnauthorized = errors.New("hindsight refused the memory API key")

// ErrNotFound is a 404 for a named memory. Hindsight answers a list of an unknown bank with
// an empty page rather than a 404, so this only ever means "no such memory".
var ErrNotFound = errors.New("no such memory")

// ErrRedacted is what Retain answers when the content carries a redaction marker. A marker
// means a secret was in that text and the node caught it on the way out; whatever the
// sentence around it says, it is not a durable fact about the organisation, and a memory
// every future turn reads is the last place it should end up.
var ErrRedacted = errors.New("content carries a redaction marker")

// RedactionMarker is the prefix both redaction paths share: the node's log redaction writes
// `[redacted:NAME]` (internal/node/redact.go) and the runtime's clone output writes a bare
// `[redacted]`. Matching the common prefix catches both.
const RedactionMarker = "[redacted"

// Memory is one thing the organisation remembers, as Podium shows it.
//
// Hindsight reports the same memory differently on the list and recall paths — the fact type
// is `fact_type` on one and `type` on the other, and entities are a comma-joined string on
// one and an array on the other. This struct is the single shape the rest of Podium sees.
type Memory struct {
	ID   string
	Text string
	// FactType is world, experience or observation.
	FactType string
	Tags     []string
	Metadata map[string]string
	// Entities are the people, systems and concepts Hindsight linked the memory to.
	Entities []string
	// Context is the retaining caller's own note about where the memory came from.
	Context string
	// DocumentID is what the retainer grouped the memory under. The conductor sets it to
	// the turn id, so this is the provenance that cannot be edited away.
	DocumentID string
	// LearnedAt is Hindsight's mentioned_at: when the memory was learned. There is no
	// created_at on the wire; updated_at is the fallback.
	LearnedAt time.Time
}

// Operation is one unit of work Hindsight took on. It matters because Retain is
// asynchronous: it returns once the work is ACCEPTED, and whether any fact came out of it
// is decided later, by a worker this process never hears from. An operation is the only
// place that outcome is recorded.
type Operation struct {
	ID string
	// Type is Hindsight's task_type: retain, batch_retain, consolidation, and so on.
	Type string
	// Status is pending, processing, completed, failed or cancelled.
	Status string
	// DocumentID is what the retainer grouped the work under — the conductor's turn id, for
	// a retain it started. Empty on the maintenance tasks Hindsight schedules itself.
	DocumentID string
	// ErrorMessage is Hindsight's own words about why it gave up, after its retries.
	ErrorMessage string
	RetryCount   int
	UpdatedAt    time.Time
}

// OperationFailed is the terminal status that means no fact was written and none will be.
const OperationFailed = "failed"

// Item is one thing to remember. Hindsight extracts facts from Content with an LLM, so
// Content is prose rather than a structured record.
type Item struct {
	// Content is the text facts are extracted from. Required.
	Content string
	// Context is a sentence about where the content came from.
	Context string
	// Tags are recorded for a later scoping step and never filtered on here.
	Tags []string
	// Metadata is provenance. It survives verbatim onto every fact extracted from Content
	// and is what the UI's provenance chips read.
	Metadata map[string]string
	// DocumentID groups the facts and is the idempotency key: retaining the same
	// DocumentID again replaces what was there rather than adding a duplicate.
	DocumentID string
}

// Client is the part of Hindsight the conductor uses. It is an interface so the turn loop
// can be tested with a recorder and so an outage is easy to simulate.
type Client interface {
	// Retain hands one item to Hindsight for extraction. It returns once Hindsight has
	// accepted the work, not once the facts exist.
	Retain(ctx context.Context, item Item) error
	// Recall is the semantic search behind SearchMemories.
	Recall(ctx context.Context, query string, limit int) ([]Memory, error)
	// List is the newest-first page behind ListMemories. The cursor is opaque; an empty
	// one starts at the beginning and an empty return means there is no further page.
	List(ctx context.Context, cursor string, limit int) (items []Memory, next string, err error)
	// Forget excludes one memory from every future recall. Hindsight has no
	// single-memory delete: this is its curation tombstone, which keeps the row for audit
	// and is reversible from Hindsight's own API.
	Forget(ctx context.Context, id string) error
	// FailedOperations is the work Hindsight accepted and then could not finish. Retain
	// reports only that the work was taken, so without this a total extraction outage —
	// a rejected model key, a model that no longer exists — is invisible to Podium.
	FailedOperations(ctx context.Context, limit int) ([]Operation, error)
	// Ready reports whether Hindsight and its database are reachable.
	Ready(ctx context.Context) error
}

// HTTP is the Hindsight client.
type HTTP struct {
	base   *url.URL
	bank   string
	apiKey string
	http   *http.Client
}

// Options is what New needs.
type Options struct {
	// BaseURL is Hindsight's base URL as this process reaches it. Required.
	BaseURL string
	// Bank is the memory bank. Required.
	Bank string
	// APIKey is the bearer. Required: Hindsight ships with no authentication, so a client
	// with no key is a client talking to something that should not exist.
	APIKey string
	// HTTPClient lets a test point at an httptest server. Nil means one with DefaultTimeout.
	HTTPClient *http.Client
}

// New validates the options and returns a client. It makes no request: a conductor must
// start with Hindsight down.
func New(opts Options) (*HTTP, error) {
	switch {
	case opts.BaseURL == "":
		return nil, errors.New("memory: a Hindsight base URL is required")
	case opts.Bank == "":
		return nil, errors.New("memory: a bank is required")
	case opts.APIKey == "":
		return nil, errors.New("memory: an API key is required")
	}
	base, err := url.Parse(strings.TrimSuffix(opts.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("memory: parse %q: %w", opts.BaseURL, err)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return &HTTP{base: base, bank: opts.Bank, apiKey: opts.APIKey, http: client}, nil
}

// MCPURL is the endpoint a task container connects its MCP client to, built from a base URL
// that is NOT this client's: the conductor and a task see Hindsight from different places.
// The trailing slash and the bank in the path are what put Hindsight in single-bank mode,
// where the tools are bank-scoped and take no bank argument.
func MCPURL(taskBaseURL, bank string) string {
	return strings.TrimSuffix(taskBaseURL, "/") + "/mcp/" + bank + "/"
}

// Retain hands one item over. It is asynchronous on Hindsight's side on purpose: extraction
// is an LLM call of unbounded duration, and a turn is already finished by the time this
// runs. A synchronous retain would make a slow extraction look like a memory outage.
func (c *HTTP) Retain(ctx context.Context, item Item) error {
	if strings.TrimSpace(item.Content) == "" {
		return errors.New("memory: retain needs content")
	}
	// The check is here, at the boundary, rather than only in the caller: this is the one
	// function that can put a redaction marker into shared memory, so it is the one place
	// that has to refuse. Nothing is sent — the request is never built.
	if strings.Contains(item.Content, RedactionMarker) {
		return fmt.Errorf("%w: nothing was retained", ErrRedacted)
	}
	body := retainRequest{
		Items: []memoryItem{{
			Content:    item.Content,
			Context:    item.Context,
			Tags:       item.Tags,
			Metadata:   item.Metadata,
			DocumentID: item.DocumentID,
		}},
		Async: true,
	}
	return c.do(ctx, http.MethodPost, c.path("memories"), nil, body, nil)
}

// Recall is Hindsight's semantic search. The limit is applied here: recall is budgeted in
// tokens rather than rows, so it can return more than was asked for.
func (c *HTTP) Recall(ctx context.Context, query string, limit int) ([]Memory, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("memory: recall needs a query")
	}
	limit = clampLimit(limit)
	var out recallResponse
	if err := c.do(ctx, http.MethodPost, c.path("memories", "recall"), nil,
		recallRequest{Query: query, MaxTokens: recallTokenBudget(limit)}, &out); err != nil {
		return nil, err
	}
	items := make([]Memory, 0, min(len(out.Results), limit))
	for _, r := range out.Results {
		if len(items) == limit {
			break
		}
		items = append(items, r.memory())
	}
	return items, nil
}

// List is one newest-first page. The cursor is Hindsight's offset, rendered as a string so
// nothing outside this package depends on it being one.
func (c *HTTP) List(ctx context.Context, cursor string, limit int) ([]Memory, string, error) {
	offset, err := parseCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = clampLimit(limit)
	query := url.Values{
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	var out listResponse
	if err := c.do(ctx, http.MethodGet, c.path("memories", "list"), query, nil, &out); err != nil {
		return nil, "", err
	}
	items := make([]Memory, 0, len(out.Items))
	for _, u := range out.Items {
		items = append(items, u.memory())
	}
	var next string
	if offset+len(items) < out.Total && len(items) > 0 {
		next = strconv.Itoa(offset + len(items))
	}
	return items, next, nil
}

// Forget invalidates one memory. Hindsight excludes an invalidated memory from recall, from
// consolidation and from the graph, prunes the observations derived from it, and keeps the
// row in an archive — which is why the UI's word is "forget" and not "delete".
func (c *HTTP) Forget(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("memory: forget needs a memory id")
	}
	body := updateRequest{State: "invalidated", Reason: "forgotten from the Podium UI"}
	return c.do(ctx, http.MethodPatch, c.path("memories", id), nil, body, nil)
}

// FailedOperations is the newest page of work Hindsight gave up on.
//
// exclude_parents is what keeps the count honest: a retain is recorded twice, once as the
// batch that wraps it and once as the chunk inside, and both go to failed together. The
// parent carries no error the child does not.
func (c *HTTP) FailedOperations(ctx context.Context, limit int) ([]Operation, error) {
	query := url.Values{
		"status":          {OperationFailed},
		"limit":           {strconv.Itoa(clampLimit(limit))},
		"exclude_parents": {"true"},
	}
	var out operationsResponse
	if err := c.do(ctx, http.MethodGet, c.path("operations"), query, nil, &out); err != nil {
		return nil, err
	}
	items := make([]Operation, 0, len(out.Operations))
	for _, o := range out.Operations {
		items = append(items, o.operation())
	}
	return items, nil
}

// Ready is the /readyz probe. It is Hindsight's own /health, which reports its database
// too and answers 503 when that is unreachable. GET / is a 404 on this service; do not
// probe it.
func (c *HTTP) Ready(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil, nil)
}

// path builds a bank-scoped REST path. `default` is a literal segment in Hindsight's URL
// space, not a variable — there is no other tenant to name.
func (c *HTTP) path(parts ...string) string {
	return "/v1/default/banks/" + url.PathEscape(c.bank) + "/" + strings.Join(parts, "/")
}

// do is the whole transport: one request, the bearer, and a typed error for the two
// statuses a caller branches on.
func (c *HTTP) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	target := c.base.JoinPath(path)
	if query != nil {
		target.RawQuery = query.Encode()
	}

	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("memory: encode %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return fmt.Errorf("memory: build %s %s: %w", method, path, err)
	}
	// Hindsight accepts a bare token too, but Bearer is what its documentation uses and
	// what the MCP client in the runtime sends.
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("memory: %s %s: %w", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode/100 != 2 {
		return statusError(method, path, res)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("memory: decode %s %s: %w", method, path, err)
	}
	return nil
}

// statusError turns a non-2xx into an error that names what happened without pasting a
// whole error page into a log line.
func statusError(method, path string, res *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	detail := strings.TrimSpace(string(raw))
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Detail) > 0 {
		var text string
		if json.Unmarshal(envelope.Detail, &text) == nil {
			detail = text
		} else {
			detail = string(envelope.Detail)
		}
	}
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (%s %s): %s", ErrUnauthorized, method, path, detail)
	case http.StatusNotFound:
		return fmt.Errorf("%w (%s %s): %s", ErrNotFound, method, path, detail)
	}
	return fmt.Errorf("memory: %s %s: %s: %s", method, path, res.Status, detail)
}

// ---------------------------------------------------------------------------
// the wire, exactly as hindsight 0.9.2 speaks it
// ---------------------------------------------------------------------------

type retainRequest struct {
	Items []memoryItem `json:"items"`
	// Async hands the work to Hindsight's worker and returns at once. See Retain.
	Async bool `json:"async"`
}

type memoryItem struct {
	Content    string            `json:"content"`
	Context    string            `json:"context,omitempty"`
	Tags       []string          `json:"tags,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	DocumentID string            `json:"document_id,omitempty"`
}

type recallRequest struct {
	Query     string `json:"query"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type recallResponse struct {
	Results []recallResult `json:"results"`
}

// recallResult is Hindsight's RecallResult. Note `type` and an entities ARRAY, where the
// list endpoint has `fact_type` and an entities STRING.
type recallResult struct {
	ID          string            `json:"id"`
	Text        string            `json:"text"`
	Type        string            `json:"type"`
	Entities    []string          `json:"entities"`
	Context     string            `json:"context"`
	MentionedAt string            `json:"mentioned_at"`
	DocumentID  string            `json:"document_id"`
	Metadata    map[string]string `json:"metadata"`
	Tags        []string          `json:"tags"`
}

func (r recallResult) memory() Memory {
	return Memory{
		ID:         r.ID,
		Text:       r.Text,
		FactType:   r.Type,
		Tags:       r.Tags,
		Metadata:   r.Metadata,
		Entities:   r.Entities,
		Context:    r.Context,
		DocumentID: r.DocumentID,
		LearnedAt:  parseTime(r.MentionedAt),
	}
}

type listResponse struct {
	Items  []memoryUnit `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// memoryUnit is one row of Hindsight's list endpoint. There is no created_at on it.
type memoryUnit struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	FactType string `json:"fact_type"`
	Context  string `json:"context"`
	// Entities is comma-joined here rather than an array. Hindsight's own doing.
	Entities    string            `json:"entities"`
	DocumentID  string            `json:"document_id"`
	Tags        []string          `json:"tags"`
	Metadata    map[string]string `json:"metadata"`
	MentionedAt string            `json:"mentioned_at"`
	UpdatedAt   string            `json:"updated_at"`
}

func (u memoryUnit) memory() Memory {
	learned := parseTime(u.MentionedAt)
	if learned.IsZero() {
		learned = parseTime(u.UpdatedAt)
	}
	return Memory{
		ID:         u.ID,
		Text:       u.Text,
		FactType:   u.FactType,
		Tags:       u.Tags,
		Metadata:   u.Metadata,
		Entities:   splitEntities(u.Entities),
		Context:    u.Context,
		DocumentID: u.DocumentID,
		LearnedAt:  learned,
	}
}

type updateRequest struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type operationsResponse struct {
	Operations []operation `json:"operations"`
	Total      int         `json:"total"`
}

type operation struct {
	ID           string `json:"id"`
	TaskType     string `json:"task_type"`
	Status       string `json:"status"`
	DocumentID   string `json:"document_id"`
	ErrorMessage string `json:"error_message"`
	RetryCount   int    `json:"retry_count"`
	UpdatedAt    string `json:"updated_at"`
}

func (o operation) operation() Operation {
	return Operation{
		ID:           o.ID,
		Type:         o.TaskType,
		Status:       o.Status,
		DocumentID:   o.DocumentID,
		ErrorMessage: o.ErrorMessage,
		RetryCount:   o.RetryCount,
		UpdatedAt:    parseTime(o.UpdatedAt),
	}
}

// ---------------------------------------------------------------------------

func splitEntities(joined string) []string {
	if strings.TrimSpace(joined) == "" {
		return nil
	}
	parts := strings.Split(joined, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseTime is lenient on purpose: a timestamp that does not parse is a missing timestamp,
// not a failed page. Hindsight sends Python isoformat, which is RFC 3339 in practice.
func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	return time.Time{}
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	}
	return limit
}

// recallTokenBudget converts a row count into the token budget recall actually takes. A
// memory is a sentence or two, so a couple of hundred tokens each is generous.
func recallTokenBudget(limit int) int {
	return limit * 256
}

func parseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("memory: %q is not a page cursor this build issued", cursor)
	}
	return offset, nil
}
