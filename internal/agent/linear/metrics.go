package linear

import "github.com/prometheus/client_golang/prometheus"

// Poll results, as the polls_total metric labels them.
const (
	PollOK          = "ok"
	PollError       = "error"
	PollRateLimited = "ratelimited"
)

// Event kinds, as the events_total metric labels them.
const (
	EventAssignment = "assignment"
	EventFollowup   = "followup"
)

// Metrics is what the Linear source reports on /metrics. A struct rather than package
// globals, for the same reason the conductor's is: two sources in one test process must not
// fight over a default registry.
type Metrics struct {
	Polls   *prometheus.CounterVec
	Events  *prometheus.CounterVec
	Backoff prometheus.Gauge
}

// NewMetrics registers the collectors on reg. A nil registerer is allowed and registers
// nothing, which is what a unit test wants.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Polls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_linear_polls_total",
			Help: "Linear poll ticks, by result: ok, error or ratelimited.",
		}, []string{"result"}),
		Events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_linear_events_total",
			Help: "Inbound events derived from Linear, by kind: assignment or followup.",
		}, []string{"kind"}),
		Backoff: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "podium_agent_linear_backoff_seconds",
			Help: "How long the next Linear poll is being delayed by rate-limit backoff. " +
				"Zero when polling at the configured interval.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.Polls, m.Events, m.Backoff)
	}
	return m
}
