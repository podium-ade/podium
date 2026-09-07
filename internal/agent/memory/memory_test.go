package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKey is an obvious fake. Nothing in this package has ever seen a real one.
const testKey = "memtoken"

// call is one request the fake Hindsight saw.
type call struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Accept string
	Body   string
}

// fake is a Hindsight that records what it was asked and answers what it is told to. Every
// response body below is copied from what hindsight 0.9.2 actually returned on this
// machine — see the wire types in memory.go.
type fake struct {
	t      *testing.T
	calls  []call
	status int
	body   string
}

func newFake(t *testing.T) (*fake, *HTTP) {
	t.Helper()
	f := &fake{t: t, status: http.StatusOK, body: "{}"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	c, err := New(Options{
		BaseURL:    srv.URL,
		Bank:       "podium",
		APIKey:     testKey,
		HTTPClient: srv.Client(),
	})
	require.NoError(t, err)
	return f, c
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, call{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Auth:   r.Header.Get("Authorization"),
		Accept: r.Header.Get("Accept"),
		Body:   string(raw),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	_, _ = w.Write([]byte(f.body))
}

func (f *fake) last() call {
	f.t.Helper()
	require.NotEmpty(f.t, f.calls, "no request reached the fake")
	return f.calls[len(f.calls)-1]
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNewRefusesAnIncompleteClient(t *testing.T) {
	for name, opts := range map[string]Options{
		"no base url": {Bank: "podium", APIKey: testKey},
		"no bank":     {BaseURL: "http://127.0.0.1:8888", APIKey: testKey},
		// Hindsight has no authentication until it is given a key, so a client with no key
		// is a client talking to a memory anybody can read and rewrite.
		"no api key": {BaseURL: "http://127.0.0.1:8888", Bank: "podium"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(opts)
			require.Error(t, err)
		})
	}
}

func TestNewMakesNoRequest(t *testing.T) {
	f, _ := newFake(t)
	assert.Empty(t, f.calls, "a conductor has to start with Hindsight down")
}

func TestMCPURLPutsTheBankInThePath(t *testing.T) {
	// The bank in the path and the trailing slash are what put Hindsight in single-bank
	// mode, where the tools are bank-scoped and take no bank argument.
	assert.Equal(t, "http://host.docker.internal:8888/mcp/podium/",
		MCPURL("http://host.docker.internal:8888", "podium"))
	assert.Equal(t, "http://host.docker.internal:8888/mcp/podium/",
		MCPURL("http://host.docker.internal:8888/", "podium"))
}

// ---------------------------------------------------------------------------
// the bearer
// ---------------------------------------------------------------------------

func TestEveryCallCarriesTheBearer(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()

	f.body = `{"success":true,"bank_id":"podium","items_count":1,"async":true}`
	require.NoError(t, c.Retain(ctx, Item{Content: "a fact"}))

	f.body = `{"results":[]}`
	_, err := c.Recall(ctx, "anything", 5)
	require.NoError(t, err)

	f.body = `{"items":[],"total":0,"limit":50,"offset":0}`
	_, _, err = c.List(ctx, "", 0)
	require.NoError(t, err)

	f.body = `{}`
	require.NoError(t, c.Forget(ctx, "ed1bd235-bd25-483c-beff-4d54ff776e52"))
	require.NoError(t, c.Ready(ctx))

	require.Len(t, f.calls, 5)
	for _, got := range f.calls {
		assert.Equal(t, "Bearer "+testKey, got.Auth, "%s %s", got.Method, got.Path)
	}
}

// ---------------------------------------------------------------------------
// Retain
// ---------------------------------------------------------------------------

func TestRetainSendsOneAsynchronousItem(t *testing.T) {
	f, c := newFake(t)
	f.body = `{"success":true,"bank_id":"podium","items_count":1,"async":true}`

	require.NoError(t, c.Retain(context.Background(), Item{
		Content:    "alice asked: who owns the scheduler?\n\nPodium answered: Bob does.",
		Context:    "podium agent, playbook general",
		Tags:       []string{"source:slack", "playbook:general"},
		Metadata:   map[string]string{"turn_id": "turn_01", "task_id": "task_01"},
		DocumentID: "turn_01",
	}))

	got := f.last()
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v1/default/banks/podium/memories", got.Path)

	var body struct {
		Items []struct {
			Content    string            `json:"content"`
			Context    string            `json:"context"`
			Tags       []string          `json:"tags"`
			Metadata   map[string]string `json:"metadata"`
			DocumentID string            `json:"document_id"`
		} `json:"items"`
		Async bool `json:"async"`
	}
	require.NoError(t, json.Unmarshal([]byte(got.Body), &body))
	require.Len(t, body.Items, 1)
	assert.Contains(t, body.Items[0].Content, "who owns the scheduler")
	assert.Equal(t, "podium agent, playbook general", body.Items[0].Context)
	assert.Equal(t, []string{"source:slack", "playbook:general"}, body.Items[0].Tags)
	assert.Equal(t, map[string]string{"turn_id": "turn_01", "task_id": "task_01"}, body.Items[0].Metadata)
	assert.Equal(t, "turn_01", body.Items[0].DocumentID)
	// Extraction is an LLM call of unbounded duration and the turn is already over, so the
	// work is handed to Hindsight's worker rather than waited for.
	assert.True(t, body.Async, "retain must be asynchronous on Hindsight's side")
}

func TestRetainRefusesEmptyContent(t *testing.T) {
	f, c := newFake(t)
	require.Error(t, c.Retain(context.Background(), Item{Content: "   \n"}))
	assert.Empty(t, f.calls, "nothing reaches Hindsight")
}

// TestRetainNeverSendsARedactionMarker is the acceptance item, pinned at the boundary that
// would otherwise leak: a marker means the node caught a secret in that text, and a memory
// every future turn reads is the last place it belongs.
func TestRetainNeverSendsARedactionMarker(t *testing.T) {
	for name, content := range map[string]string{
		// What internal/node/redact.go writes into a task's output.
		"the node's log marker": "the password is [redacted:DB_PASSWORD], so use that",
		// What the runtime's own clone redaction writes.
		"the runtime's marker": "clone failed: https://x-access-token:[redacted]@github.com/o/r",
	} {
		t.Run(name, func(t *testing.T) {
			f, c := newFake(t)
			err := c.Retain(context.Background(), Item{Content: content, DocumentID: "turn_01"})
			require.ErrorIs(t, err, ErrRedacted)
			assert.Empty(t, f.calls, "not one byte of it may reach Hindsight")
		})
	}
}

// ---------------------------------------------------------------------------
// Recall
// ---------------------------------------------------------------------------

// recallBody is one real recall response from hindsight 0.9.2. Note `type` and an entities
// ARRAY, where the list endpoint has `fact_type` and an entities STRING.
const recallBody = `{"results":[
  {"id":"ed1bd235-bd25-483c-beff-4d54ff776e52",
   "text":"Bob owns the Podium scheduler.","type":"world",
   "entities":["Bob","scheduler"],"context":"podium agent, playbook general",
   "mentioned_at":"2026-09-03T19:12:04.756733+00:00","document_id":"turn_01probe",
   "metadata":{"task_id":"task_01probe","turn_id":"turn_01probe"},
   "chunk_id":"podium_turn_01probe_0","tags":["source:slack","playbook:general"],
   "scores":{"final":1.09}},
  {"id":"2","text":"second","type":"observation"}
]}`

func TestRecallMapsTheWireOntoOneShape(t *testing.T) {
	f, c := newFake(t)
	f.body = recallBody

	items, err := c.Recall(context.Background(), "who owns the scheduler", 10)
	require.NoError(t, err)

	got := f.last()
	assert.Equal(t, http.MethodPost, got.Method)
	assert.Equal(t, "/v1/default/banks/podium/memories/recall", got.Path)
	assert.JSONEq(t, `{"query":"who owns the scheduler","max_tokens":2560}`, got.Body)

	require.Len(t, items, 2)
	assert.Equal(t, "ed1bd235-bd25-483c-beff-4d54ff776e52", items[0].ID)
	assert.Equal(t, "Bob owns the Podium scheduler.", items[0].Text)
	assert.Equal(t, FactWorld, items[0].FactType, "recall calls it `type`, not `fact_type`")
	assert.Equal(t, []string{"Bob", "scheduler"}, items[0].Entities)
	assert.Equal(t, "podium agent, playbook general", items[0].Context)
	assert.Equal(t, "turn_01probe", items[0].DocumentID)
	assert.Equal(t, map[string]string{"task_id": "task_01probe", "turn_id": "turn_01probe"}, items[0].Metadata)
	assert.Equal(t, []string{"source:slack", "playbook:general"}, items[0].Tags)
	assert.Equal(t,
		time.Date(2026, 9, 3, 19, 12, 4, 756733000, time.UTC),
		items[0].LearnedAt.UTC())
	assert.Equal(t, FactObservation, items[1].FactType)
}

// Recall is budgeted in tokens rather than rows, so it can hand back more than was asked
// for. The limit is applied on this side.
func TestRecallAppliesTheLimitItself(t *testing.T) {
	f, c := newFake(t)
	f.body = recallBody

	items, err := c.Recall(context.Background(), "anything", 1)
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

func TestRecallRefusesAnEmptyQuery(t *testing.T) {
	f, c := newFake(t)
	_, err := c.Recall(context.Background(), "  ", 10)
	require.Error(t, err)
	assert.Empty(t, f.calls)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

// listBody is one real list response from hindsight 0.9.2, trimmed to the keys this client
// reads. There is deliberately no created_at: the wire has none.
const listBody = `{"items":[
  {"id":"ed1bd235-bd25-483c-beff-4d54ff776e52",
   "text":"Bob owns the Podium scheduler.","context":"podium agent, playbook general",
   "date":"2026-09-03T19:12:04.756733+00:00","fact_type":"world",
   "document_id":"turn_01probe","mentioned_at":"2026-09-03T19:12:04.756733+00:00",
   "entities":"Bob, scheduler","chunk_id":"podium_turn_01probe_0","proof_count":1,
   "tags":["source:slack","playbook:general"],
   "metadata":{"task_id":"task_01probe","turn_id":"turn_01probe"},
   "state":"valid","updated_at":"2026-09-03T19:12:04.953152+00:00","source_memory_ids":[]}
],"total":3,"limit":1,"offset":0}`

func TestListMapsTheWireAndPages(t *testing.T) {
	f, c := newFake(t)
	f.body = listBody

	items, next, err := c.List(context.Background(), "", 1)
	require.NoError(t, err)

	got := f.last()
	assert.Equal(t, http.MethodGet, got.Method)
	assert.Equal(t, "/v1/default/banks/podium/memories/list", got.Path)
	assert.Equal(t, "limit=1&offset=0", got.Query)

	require.Len(t, items, 1)
	assert.Equal(t, FactWorld, items[0].FactType, "list calls it `fact_type`, not `type`")
	assert.Equal(t, []string{"Bob", "scheduler"}, items[0].Entities,
		"list joins entities with commas; recall sends an array")
	assert.Equal(t, "turn_01probe", items[0].DocumentID)
	assert.Equal(t,
		time.Date(2026, 9, 3, 19, 12, 4, 756733000, time.UTC),
		items[0].LearnedAt.UTC())

	// 1 of 3 read, so there is more.
	assert.Equal(t, "1", next)
}

func TestListFollowsItsOwnCursor(t *testing.T) {
	f, c := newFake(t)
	f.body = `{"items":[{"id":"a","text":"a"},{"id":"b","text":"b"}],"total":5,"limit":2,"offset":2}`

	_, next, err := c.List(context.Background(), "2", 2)
	require.NoError(t, err)
	assert.Equal(t, "limit=2&offset=2", f.last().Query)
	assert.Equal(t, "4", next)

	f.body = `{"items":[{"id":"e","text":"e"}],"total":5,"limit":2,"offset":4}`
	_, next, err = c.List(context.Background(), "4", 2)
	require.NoError(t, err)
	assert.Empty(t, next, "the last page has no cursor")
}

func TestListRefusesACursorThisBuildDidNotIssue(t *testing.T) {
	f, c := newFake(t)
	for _, cursor := range []string{"not-a-number", "-1"} {
		_, _, err := c.List(context.Background(), cursor, 10)
		require.Error(t, err, "cursor %q", cursor)
	}
	assert.Empty(t, f.calls)
}

func TestListClampsTheLimit(t *testing.T) {
	f, c := newFake(t)
	f.body = `{"items":[],"total":0,"limit":0,"offset":0}`

	_, _, err := c.List(context.Background(), "", 0)
	require.NoError(t, err)
	assert.Equal(t, "limit=50&offset=0", f.last().Query)

	_, _, err = c.List(context.Background(), "", 100000)
	require.NoError(t, err)
	assert.Equal(t, "limit=200&offset=0", f.last().Query)
}

// An unknown bank is an empty page, not a 404 — which is why nothing here creates a bank:
// Hindsight makes one on its first write.
func TestListOfAnUnwrittenBankIsAnEmptyPage(t *testing.T) {
	f, c := newFake(t)
	f.body = `{"items":[],"total":0,"limit":50,"offset":0}`

	items, next, err := c.List(context.Background(), "", 0)
	require.NoError(t, err)
	assert.Empty(t, items)
	assert.Empty(t, next)
}

// ---------------------------------------------------------------------------
// Forget
// ---------------------------------------------------------------------------

// Hindsight has no single-memory DELETE. Forgetting is a PATCH to state=invalidated, which
// takes the memory out of recall and consolidation and keeps the row for audit.
func TestForgetPatchesTheCurationState(t *testing.T) {
	f, c := newFake(t)
	f.body = `{"id":"ed1bd235","state":"invalidated"}`

	require.NoError(t, c.Forget(context.Background(), "ed1bd235-bd25-483c-beff-4d54ff776e52"))

	got := f.last()
	assert.Equal(t, http.MethodPatch, got.Method)
	assert.Equal(t, "/v1/default/banks/podium/memories/ed1bd235-bd25-483c-beff-4d54ff776e52", got.Path)
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(got.Body), &body))
	assert.Equal(t, "invalidated", body["state"])
	assert.NotEmpty(t, body["reason"], "the archive records why")
}

func TestForgetRefusesAnEmptyID(t *testing.T) {
	f, c := newFake(t)
	require.Error(t, c.Forget(context.Background(), ""))
	assert.Empty(t, f.calls)
}

// ---------------------------------------------------------------------------
// Ready
// ---------------------------------------------------------------------------

// GET / is a 404 on Hindsight 0.9.2. /health is the endpoint that reports its database too,
// and it needs no bearer — though sending one costs nothing.
func TestReadyProbesHealthAndNotTheRoot(t *testing.T) {
	f, c := newFake(t)
	require.NoError(t, c.Ready(context.Background()))
	assert.Equal(t, "/health", f.last().Path)

	f.status = http.StatusServiceUnavailable
	f.body = `{"detail":"database unreachable"}`
	require.Error(t, c.Ready(context.Background()))
}

func TestReadyFailsWhenHindsightIsNotThere(t *testing.T) {
	c, err := New(Options{
		BaseURL: "http://127.0.0.1:1",
		Bank:    "podium",
		APIKey:  testKey,
		// A short timeout so a machine with a firewall that blackholes rather than
		// refuses does not make this test a two-minute wait.
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
	})
	require.NoError(t, err)
	require.Error(t, c.Ready(context.Background()))
}

// ---------------------------------------------------------------------------
// errors
// ---------------------------------------------------------------------------

// A wrong key is the one memory failure an operator fixes by editing .env rather than by
// reading a log, so it is a typed error.
func TestAWrongKeyIsATypedError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f, c := newFake(t)
		f.status = status
		f.body = `{"detail":"Authentication failed: Invalid API key"}`

		_, _, err := c.List(context.Background(), "", 10)
		require.ErrorIs(t, err, ErrUnauthorized)
		assert.Contains(t, err.Error(), "Invalid API key", "the provider's own words reach the log")
	}
}

func TestForgettingAMemoryThatIsNotThereIsNotFound(t *testing.T) {
	f, c := newFake(t)
	f.status = http.StatusNotFound
	f.body = `{"detail":"Memory unit '0000' not found"}`

	err := c.Forget(context.Background(), "0000")
	require.ErrorIs(t, err, ErrNotFound)
}

// A retain whose extraction fails is a 500 with a detail string. It must be an error the
// conductor can count, and it must not look like anything else.
func TestAFailedExtractionIsAPlainError(t *testing.T) {
	f, c := newFake(t)
	f.status = http.StatusInternalServerError
	f.body = `{"detail":"Fact extraction failed: 1/1 chunks failed."}`

	err := c.Retain(context.Background(), Item{Content: "a fact"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnauthorized)
	assert.NotErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "Fact extraction failed")
}

// A 422 from Hindsight's own validation carries a LIST in `detail`, not a string. It has to
// survive reaching a log line rather than being swallowed.
func TestAValidationFailureSurvivesItsListShapedDetail(t *testing.T) {
	f, c := newFake(t)
	f.status = http.StatusUnprocessableEntity
	f.body = `{"detail":[{"type":"value_error","msg":"operation_id must be a valid UUID"}]}`

	err := c.Retain(context.Background(), Item{Content: "a fact"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a valid UUID")
}

func TestAnErrorPageIsNotPastedWholeIntoTheLog(t *testing.T) {
	f, c := newFake(t)
	f.status = http.StatusBadGateway
	f.body = "<html>" + strings.Repeat("x", 64<<10) + "</html>"

	err := c.Retain(context.Background(), Item{Content: "a fact"})
	require.Error(t, err)
	assert.Less(t, len(err.Error()), 4<<10, "an error page is quoted, not reproduced")
}

func TestABodyThatIsNotJSONIsAnError(t *testing.T) {
	f, c := newFake(t)
	f.body = "not json"

	_, _, err := c.List(context.Background(), "", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode")
}

func TestACancelledContextStopsTheCall(t *testing.T) {
	_, c := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Retain(ctx, Item{Content: "a fact"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
}

// The interface is what the conductor and the API handlers depend on; the concrete client
// has to satisfy it.
var _ Client = (*HTTP)(nil)

// operationsBody is what hindsight 0.9.2 answered on this machine when every retain was
// failing: the model key in the container was a placeholder, so extraction 401'd, retried
// three times and gave up — while /health stayed green and Retain went on returning 202.
const operationsBody = `{"bank_id":"podium","total":2,"limit":3,"offset":0,"operations":[
  {"id":"81e013c4-f15d-4cac-97b6-e22f0359aba4","task_type":"retain","items_count":1,
   "document_id":"turn_01m1s5xrrr5wwjh1t0w5djy2xy","filename":null,"details":null,
   "created_at":"2026-09-05T19:57:37.134654+00:00","updated_at":"2026-09-05T20:00:39.262692+00:00",
   "status":"failed","retry_count":3,"next_retry_at":null,"progress":null,
   "error_message":"RuntimeError: Fact extraction failed: 1/1 chunks failed. First failures: chunk 0: AuthenticationError: Error code: 401 - {'type': 'error', 'error': {'type': 'authentication_error', 'message': 'invalid x-api-key'}}"},
  {"id":"1f3f6b47-1fbf-4025-a111-895891a45521","task_type":"retain","items_count":1,
   "document_id":"turn_01m1me0mcbtn5seejpjpbvt4se","filename":null,"details":null,
   "created_at":"2026-09-05T16:20:20.618426+00:00","updated_at":"2026-09-05T16:23:22.474889+00:00",
   "status":"failed","retry_count":3,"next_retry_at":null,"progress":null,
   "error_message":"RuntimeError: Fact extraction failed: 1/1 chunks failed."}]}`

func TestFailedOperationsReportsWhatRetainCannot(t *testing.T) {
	f, c := newFake(t)
	f.body = operationsBody

	ops, err := c.FailedOperations(context.Background(), 3)
	require.NoError(t, err)

	got := f.last()
	assert.Equal(t, http.MethodGet, got.Method)
	assert.Equal(t, "/v1/default/banks/podium/operations", got.Path)
	assert.Contains(t, got.Query, "status=failed")
	assert.Contains(t, got.Query, "exclude_parents=true",
		"a retain is recorded as both a batch and the chunk inside it, and both fail "+
			"together — counting the parent too doubles every number")

	require.Len(t, ops, 2)
	assert.Equal(t, "retain", ops[0].Type)
	assert.Equal(t, OperationFailed, ops[0].Status)
	assert.Equal(t, "turn_01m1s5xrrr5wwjh1t0w5djy2xy", ops[0].DocumentID,
		"the document id is the turn id, which is how a lost memory is traced back")
	assert.Equal(t, 3, ops[0].RetryCount)
	assert.Contains(t, ops[0].ErrorMessage, "invalid x-api-key")
	assert.Equal(t,
		time.Date(2026, 9, 5, 20, 0, 39, 262692000, time.UTC),
		ops[0].UpdatedAt.UTC())
}

func TestFailedOperationsOfAHealthyBankIsEmpty(t *testing.T) {
	f, c := newFake(t)
	f.body = `{"bank_id":"podium","total":0,"limit":50,"offset":0,"operations":[]}`

	ops, err := c.FailedOperations(context.Background(), 0)
	require.NoError(t, err)
	assert.Empty(t, ops)
}

func TestFailedOperationsSurfacesARefusedKey(t *testing.T) {
	f, c := newFake(t)
	f.status = http.StatusUnauthorized
	f.body = `{"detail":"invalid api key"}`

	_, err := c.FailedOperations(context.Background(), 3)
	require.ErrorIs(t, err, ErrUnauthorized)
}
