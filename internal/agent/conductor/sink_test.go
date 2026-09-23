package conductor

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// saidSource records what a sink said, in order. It is a plain Source; drawingSource adds
// ActivityPoster on top of it.
type saidSource struct {
	mu   sync.Mutex
	said []Outbound
}

func (s *saidSource) record(out Outbound) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.said = append(s.said, out)
}

func (s *saidSource) Kind() string                                  { return "said" }
func (s *saidSource) Events() <-chan InboundEvent                   { return nil }
func (s *saidSource) React(context.Context, string, Reaction) error { return nil }
func (s *saidSource) Attach(context.Context, string, Attachment) error {
	return nil
}
func (s *saidSource) FetchTranscript(context.Context, string) ([]BriefEntry, error) {
	return nil, nil
}
func (s *saidSource) Post(_ context.Context, _ string, out Outbound) (string, error) {
	s.record(out)
	return "m1", nil
}
func (s *saidSource) Edit(_ context.Context, _, _ string, out Outbound) error {
	s.record(out)
	return nil
}

type drawingSource struct{ saidSource }

func (s *drawingSource) PostActivity(_ context.Context, _ string, out Outbound) error {
	s.record(out)
	return nil
}

func newTestSink(src Source) *sink {
	return &sink{
		c:      &Conductor{metrics: NewMetrics(nil), logger: slog.New(slog.DiscardHandler)},
		src:    src,
		ref:    "chat_1",
		taskID: "task_1",
	}
}

func TestActivityReachesOnlyASourceThatDrawsIt(t *testing.T) {
	ctx := context.Background()
	doc := `{"kind":"tool","tool":"bash"}`

	plain := &saidSource{}
	newTestSink(plain).deliver(ctx, MsgActivity, doc, nil)
	assert.Empty(t, plain.said, "Slack and Linear are told none of it")

	drawing := &drawingSource{}
	newTestSink(drawing).deliver(ctx, MsgActivity, doc, nil)
	require.Len(t, drawing.said, 1)
	assert.Equal(t, Outbound{Type: OutActivity, TaskID: "task_1", Text: doc}, drawing.said[0])
}

func TestActivityThatIsNotJSONIsDropped(t *testing.T) {
	src := &drawingSource{}
	newTestSink(src).deliver(context.Background(), MsgActivity, "not json", nil)
	assert.Empty(t, src.said)
}

func TestHeldProgressIsShownBeforeTheActivityAfterIt(t *testing.T) {
	ctx := context.Background()
	src := &drawingSource{}
	s := newTestSink(src)

	s.deliver(ctx, OutProgress, "first", nil)
	// Inside the throttle window, so this one is held rather than shown.
	s.deliver(ctx, OutProgress, "reading the schema", nil)
	s.deliver(ctx, MsgActivity, `{"kind":"tool","tool":"read"}`, nil)

	require.Len(t, src.said, 3)
	assert.Equal(t, ProgressPrefix+"reading the schema", src.said[1].Text)
	assert.Equal(t, OutActivity, src.said[2].Type)
}
