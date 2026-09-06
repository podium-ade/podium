package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/memory"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// fakeMemory is the shared memory as these handlers see it: what it was asked, what it
// answers, and an error it can be told to answer with instead.
type fakeMemory struct {
	mu sync.Mutex

	items  []memory.Memory
	next   string
	err    error
	forgot []string
	// listedCursor, listedLimit and query record the last call's arguments.
	listedCursor string
	listedLimit  int
	query        string
	queryLimit   int
	failedOps    []memory.Operation
}

func (f *fakeMemory) Retain(context.Context, memory.Item) error { return f.err }

func (f *fakeMemory) Recall(_ context.Context, query string, limit int) ([]memory.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.query, f.queryLimit = query, limit
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func (f *fakeMemory) List(_ context.Context, cursor string, limit int) ([]memory.Memory, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listedCursor, f.listedLimit = cursor, limit
	if f.err != nil {
		return nil, "", f.err
	}
	return f.items, f.next, nil
}

func (f *fakeMemory) Forget(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.forgot = append(f.forgot, id)
	return nil
}

func (f *fakeMemory) Ready(context.Context) error { return f.err }

func (f *fakeMemory) FailedOperations(context.Context, int) ([]memory.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failedOps, f.err
}

func (f *fakeMemory) forgotten() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.forgot...)
}

// oneMemory is what the client hands the handlers after it has flattened the two shapes
// Hindsight reports a memory in.
func oneMemory() memory.Memory {
	return memory.Memory{
		ID:       "ed1bd235-bd25-483c-beff-4d54ff776e52",
		Text:     "Bob owns the Podium scheduler.",
		FactType: memory.FactWorld,
		Tags:     []string{"source:slack", "skill:general"},
		Metadata: map[string]string{
			"turn_id":    "turn_01",
			"task_id":    "task_01",
			"session_id": "sess_01",
			"source_url": "https://example.slack.com/archives/C1/p11",
		},
		Entities:   []string{"Bob", "scheduler"},
		Context:    "podium agent, skill general",
		DocumentID: "turn_01",
		LearnedAt:  time.Date(2026, 9, 3, 19, 12, 4, 0, time.UTC),
	}
}

func memoryService(t *testing.T, mem memory.Client) *AgentService {
	t.Helper()
	return NewAgentService(AgentServiceOptions{Memory: mem, Logger: quietLogger()})
}

// ---------------------------------------------------------------------------
// not configured
// ---------------------------------------------------------------------------

// An install with no memory service answers all three with FailedPrecondition, which is what
// the UI turns into "Memory is not configured on this host". It is not Unimplemented: the
// RPC exists and the deployment is missing something an operator can add.
func TestTheMemoryRPCsRefuseWhenMemoryIsNotConfigured(t *testing.T) {
	s := memoryService(t, nil)
	ctx := context.Background()

	_, err := s.ListMemories(ctx, connect.NewRequest(&agentv1.ListMemoriesRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "memory is not configured")

	_, err = s.SearchMemories(ctx, connect.NewRequest(&agentv1.SearchMemoriesRequest{Query: "anything"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	_, err = s.DeleteMemory(ctx, connect.NewRequest(&agentv1.DeleteMemoryRequest{Id: "x"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

// ---------------------------------------------------------------------------
// ListMemories
// ---------------------------------------------------------------------------

func TestListMemoriesReturnsTheProvenanceIntact(t *testing.T) {
	mem := &fakeMemory{items: []memory.Memory{oneMemory()}, next: "50"}
	s := memoryService(t, mem)

	res, err := s.ListMemories(context.Background(),
		connect.NewRequest(&agentv1.ListMemoriesRequest{Cursor: "0", Limit: 25}))
	require.NoError(t, err)

	assert.Equal(t, "0", mem.listedCursor)
	assert.Equal(t, 25, mem.listedLimit)
	assert.Equal(t, "50", res.Msg.GetNextCursor())

	require.Len(t, res.Msg.GetItems(), 1)
	got := res.Msg.GetItems()[0]
	assert.Equal(t, "ed1bd235-bd25-483c-beff-4d54ff776e52", got.GetId())
	assert.Equal(t, "Bob owns the Podium scheduler.", got.GetText())
	assert.Equal(t, memory.FactWorld, got.GetFactType())
	assert.Equal(t, []string{"source:slack", "skill:general"}, got.GetTags())
	assert.Equal(t, []string{"Bob", "scheduler"}, got.GetEntities())
	assert.Equal(t, "podium agent, skill general", got.GetContext())
	assert.Equal(t, "turn_01", got.GetDocumentId())
	// The provenance is the mitigation for a poisoned memory: every chip in the UI reads
	// out of this map, so nothing here may be dropped in translation.
	assert.Equal(t, "turn_01", got.GetMetadata()["turn_id"])
	assert.Equal(t, "task_01", got.GetMetadata()["task_id"])
	assert.Equal(t, "https://example.slack.com/archives/C1/p11", got.GetMetadata()["source_url"])
	require.NotNil(t, got.GetCreatedAt())
	assert.Equal(t, int64(1788462724), got.GetCreatedAt().GetSeconds())
}

// The memory engine has no created_at on the wire, so a memory whose learned-at could not be
// read has no timestamp rather than the zero time — which the UI would render as 1970.
func TestAMemoryWithNoTimestampCarriesNone(t *testing.T) {
	m := oneMemory()
	m.LearnedAt = time.Time{}
	s := memoryService(t, &fakeMemory{items: []memory.Memory{m}})

	res, err := s.ListMemories(context.Background(), connect.NewRequest(&agentv1.ListMemoriesRequest{}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetItems(), 1)
	assert.Nil(t, res.Msg.GetItems()[0].GetCreatedAt())
}

func TestListMemoriesOnAnEmptyBank(t *testing.T) {
	s := memoryService(t, &fakeMemory{})
	res, err := s.ListMemories(context.Background(), connect.NewRequest(&agentv1.ListMemoriesRequest{}))
	require.NoError(t, err)
	assert.Empty(t, res.Msg.GetItems())
	assert.Empty(t, res.Msg.GetNextCursor())
}

// ---------------------------------------------------------------------------
// SearchMemories
// ---------------------------------------------------------------------------

func TestSearchMemoriesPassesTheQueryThrough(t *testing.T) {
	mem := &fakeMemory{items: []memory.Memory{oneMemory()}}
	s := memoryService(t, mem)

	res, err := s.SearchMemories(context.Background(),
		connect.NewRequest(&agentv1.SearchMemoriesRequest{Query: "  who owns the scheduler  ", Limit: 10}))
	require.NoError(t, err)
	assert.Equal(t, "who owns the scheduler", mem.query, "the query is trimmed, not reinterpreted")
	assert.Equal(t, 10, mem.queryLimit)
	assert.Len(t, res.Msg.GetItems(), 1)
}

func TestSearchMemoriesRefusesAnEmptyQuery(t *testing.T) {
	mem := &fakeMemory{}
	s := memoryService(t, mem)

	_, err := s.SearchMemories(context.Background(),
		connect.NewRequest(&agentv1.SearchMemoriesRequest{Query: "   "}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Empty(t, mem.query, "nothing reached the memory service")
}

// ---------------------------------------------------------------------------
// DeleteMemory
// ---------------------------------------------------------------------------

func TestDeleteMemoryForgetsTheOneNamed(t *testing.T) {
	mem := &fakeMemory{}
	s := memoryService(t, mem)

	_, err := s.DeleteMemory(context.Background(),
		connect.NewRequest(&agentv1.DeleteMemoryRequest{Id: " ed1bd235 "}))
	require.NoError(t, err)
	assert.Equal(t, []string{"ed1bd235"}, mem.forgotten())
}

func TestDeleteMemoryRefusesAnEmptyID(t *testing.T) {
	mem := &fakeMemory{}
	s := memoryService(t, mem)

	_, err := s.DeleteMemory(context.Background(), connect.NewRequest(&agentv1.DeleteMemoryRequest{Id: " "}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Empty(t, mem.forgotten())
}

func TestDeletingAMemoryThatIsGoneIsNotFound(t *testing.T) {
	s := memoryService(t, &fakeMemory{err: memory.ErrNotFound})

	_, err := s.DeleteMemory(context.Background(), connect.NewRequest(&agentv1.DeleteMemoryRequest{Id: "x"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// ---------------------------------------------------------------------------
// failures
// ---------------------------------------------------------------------------

// A memory service that is down is Unavailable, which the UI shows as a strip rather than
// as a broken page.
func TestAMemoryOutageIsUnavailable(t *testing.T) {
	s := memoryService(t, &fakeMemory{err: errors.New("connection refused")})

	_, err := s.ListMemories(context.Background(), connect.NewRequest(&agentv1.ListMemoriesRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// A refused API key is this HOST's problem, not the caller's, so it is Unavailable rather
// than PermissionDenied — and the message names the variable to check. Telling an operator
// "you may not do that" would send them to the wrong place entirely.
func TestARefusedMemoryKeyIsThisHostsProblem(t *testing.T) {
	s := memoryService(t, &fakeMemory{err: memory.ErrUnauthorized})

	_, err := s.SearchMemories(context.Background(),
		connect.NewRequest(&agentv1.SearchMemoriesRequest{Query: "anything"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.NotEqual(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "PODIUM_AGENT_MEMORY_API_KEY")
}
