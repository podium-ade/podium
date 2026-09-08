package conductor

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/memory"
)

// stubMemory answers FailedOperations and nothing else: watchExtractions is the only caller
// under test, and a method it never reaches should panic rather than quietly return a zero.
type stubMemory struct {
	failed []memory.Operation
	err    error
	calls  int
}

func (s *stubMemory) FailedOperations(context.Context, int) ([]memory.Operation, error) {
	s.calls++
	return s.failed, s.err
}

func (s *stubMemory) Retain(context.Context, memory.Item) error { panic("not under test") }
func (s *stubMemory) Recall(context.Context, string, int) ([]memory.Memory, error) {
	panic("not under test")
}

func (s *stubMemory) List(context.Context, string, int) ([]memory.Memory, string, error) {
	panic("not under test")
}
func (s *stubMemory) Forget(context.Context, string) error { panic("not under test") }
func (s *stubMemory) Ready(context.Context) error          { panic("not under test") }

func newCheckedConductor(mem memory.Client) (*Conductor, *prometheus.Registry) {
	reg := prometheus.NewRegistry()
	return &Conductor{
		memories: mem,
		metrics:  NewMetrics(reg),
		logger:   slog.New(slog.DiscardHandler),
	}, reg
}

// gaugeValue reads one gauge back off the registry. Going through Gather rather than
// through testutil keeps this test from adding a dependency for one assertion.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		require.Len(t, f.GetMetric(), 1)
		return f.GetMetric()[0].GetGauge().GetValue()
	}
	t.Fatalf("%s was never registered", name)
	return 0
}

const failedGauge = "podium_agent_memory_extraction_failed"

func TestExtractionFailuresAreCountedRatherThanAssumedAway(t *testing.T) {
	mem := &stubMemory{failed: []memory.Operation{
		{ID: "a", Status: memory.OperationFailed, DocumentID: "turn_1", RetryCount: 3,
			ErrorMessage: "AuthenticationError: 401 invalid x-api-key"},
		{ID: "b", Status: memory.OperationFailed, DocumentID: "turn_2", RetryCount: 3},
	}}
	c, reg := newCheckedConductor(mem)

	c.checkExtractions(context.Background())

	assert.Equal(t, 1, mem.calls)
	assert.Equal(t, 2.0, gaugeValue(t, reg, failedGauge),
		"two retains were accepted and extracted nothing; the gauge is the only place that shows")
}

func TestAHealthyMemoryReportsZeroFailures(t *testing.T) {
	c, reg := newCheckedConductor(&stubMemory{})

	c.checkExtractions(context.Background())

	assert.Equal(t, 0.0, gaugeValue(t, reg, failedGauge))
}

// A recovered outage has to clear, or the gauge is a high-water mark and an operator who
// fixed the key never sees it go green.
func TestTheGaugeClearsWhenExtractionRecovers(t *testing.T) {
	mem := &stubMemory{failed: []memory.Operation{{ID: "a", Status: memory.OperationFailed}}}
	c, reg := newCheckedConductor(mem)
	c.checkExtractions(context.Background())
	require.Equal(t, 1.0, gaugeValue(t, reg, failedGauge))

	mem.failed = nil
	c.checkExtractions(context.Background())

	assert.Equal(t, 0.0, gaugeValue(t, reg, failedGauge))
}

// The check failing is not the same as extraction failing: one is a memory outage, which
// /readyz already reports, and the other is memories being silently lost. Conflating them
// would make an unreachable Hindsight look like data loss.
func TestAnUnreachableMemoryDoesNotCountAsLostMemories(t *testing.T) {
	c, reg := newCheckedConductor(&stubMemory{err: errors.New("connection refused")})

	c.checkExtractions(context.Background())

	assert.Equal(t, 0.0, gaugeValue(t, reg, failedGauge))
}

// Memory is optional, so the loop has to return at once rather than dereference a nil
// client or sit on its ticker for five minutes holding the conductor's WaitGroup open.
func TestWatchExtractionsIsANoOpWithoutMemory(t *testing.T) {
	c := &Conductor{metrics: NewMetrics(nil), logger: slog.New(slog.DiscardHandler)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.watchExtractions(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchExtractions blocked with no memory configured; it must return at once")
	}
}

// TestAFailureFromLastWeekIsNotNews. The bug this closed: `status=failed` has no time bound,
// so one retain that failed on a Tuesday was re-warned every five minutes for ever and held
// the gauge above zero. Observed on the live stack — two failures from the 5th still being
// reported on the 8th, with ninety-seven successful retains in between.
func TestAFailureFromLastWeekIsNotNews(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour)
	mem := &stubMemory{failed: []memory.Operation{
		{ID: "a", Status: memory.OperationFailed, DocumentID: "turn_1", UpdatedAt: old},
	}}
	c, reg := newCheckedConductor(mem)

	// A first pass measures from one interval ago, not from the beginning of time, so a
	// restart does not replay what an operator already fixed.
	c.checkExtractions(context.Background())
	assert.Equal(t, 0.0, gaugeValue(t, reg, failedGauge))

	// And it stays quiet, which is the difference between a gauge and a high-water mark.
	c.checkExtractions(context.Background())
	assert.Equal(t, 0.0, gaugeValue(t, reg, failedGauge))
}

// A live outage fails retain after retain, so every pass finds something newer than the
// last look and the warning keeps coming. Bounding the window must not buy quiet at the
// price of missing the thing this check exists for.
func TestAnOutageThatIsStillHappeningKeepsBeingReported(t *testing.T) {
	mem := &stubMemory{}
	c, reg := newCheckedConductor(mem)

	for i := 0; i < 3; i++ {
		mem.failed = []memory.Operation{
			{ID: "a", Status: memory.OperationFailed, UpdatedAt: time.Now()},
		}
		c.checkExtractions(context.Background())
		assert.Equal(t, 1.0, gaugeValue(t, reg, failedGauge), "pass %d", i)
	}
}

// An unknown timestamp counts as new. This check exists to catch memories being lost
// silently, so a Hindsight that stops sending `updated_at` has to make it noisy rather than
// blind — treating an unknown time as old would go quiet in exactly the case where something
// upstream had already changed under us.
func TestAFailureWithNoTimestampIsTreatedAsNew(t *testing.T) {
	mem := &stubMemory{failed: []memory.Operation{
		{ID: "a", Status: memory.OperationFailed}, // zero UpdatedAt
	}}
	c, reg := newCheckedConductor(mem)

	c.checkExtractions(context.Background())
	assert.Equal(t, 1.0, gaugeValue(t, reg, failedGauge))
	c.checkExtractions(context.Background())
	assert.Equal(t, 1.0, gaugeValue(t, reg, failedGauge), "still unknown, still news")
}

// The recovery case, end to end through the window: it broke, it was fixed, and the gauge
// has to reach zero without anybody restarting the conductor.
func TestTheGaugeClearsOnceTheFailuresStopBeingRecent(t *testing.T) {
	mem := &stubMemory{failed: []memory.Operation{
		{ID: "a", Status: memory.OperationFailed, UpdatedAt: time.Now()},
	}}
	c, reg := newCheckedConductor(mem)
	c.checkExtractions(context.Background())
	require.Equal(t, 1.0, gaugeValue(t, reg, failedGauge))

	// Hindsight still reports the same failed row — it always will — but nothing has failed
	// since the last look.
	c.checkExtractions(context.Background())

	assert.Equal(t, 0.0, gaugeValue(t, reg, failedGauge),
		"the row is history now, and history is not an outage")
}
