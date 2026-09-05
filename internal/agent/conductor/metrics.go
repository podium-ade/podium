package conductor

import "github.com/prometheus/client_golang/prometheus"

// Metrics is what the conductor reports on /metrics. It is a struct rather than package
// globals so two conductors in one test process do not fight over a default registry.
type Metrics struct {
	Turns                  *prometheus.CounterVec
	TurnDuration           *prometheus.HistogramVec
	TurnsWithoutAccounting prometheus.Counter
	RelayedMessages        *prometheus.CounterVec
	SourceEvents           *prometheus.CounterVec
	FollowReconnects       prometheus.Counter
	MemoryRetains          *prometheus.CounterVec
}

// NewMetrics registers the conductor's collectors on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Turns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_turns_total",
			Help: "Turns that reached a terminal status, by source, skill and status.",
		}, []string{"source", "skill", "status"}),
		TurnDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "podium_agent_turn_duration_seconds",
			Help:    "Wall time from the inbound event to the turn's terminal status.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 12),
		}, []string{"skill"}),
		TurnsWithoutAccounting: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "podium_agent_turns_without_accounting_total",
			Help: "Turns that succeeded without reporting num_turns and cost_usd, which are " +
				"left null. Anything above zero is lost accounting, not a failed turn.",
		}),
		RelayedMessages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_relayed_messages_total",
			Help: "Task messages relayed into a conversation, by message type.",
		}, []string{"type"}),
		SourceEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_source_events_total",
			Help: "Inbound events accepted from a source.",
		}, []string{"source"}),
		FollowReconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "podium_agent_follow_reconnects_total",
			Help: "Reconnects of the task event stream.",
		}),
		MemoryRetains: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_memory_retain_total",
			Help: "End-of-turn writes to the shared memory, by result: ok, error or redacted. " +
				"A rising error count is a memory outage and never a failed turn.",
		}, []string{"result"}),
	}
	if reg != nil {
		reg.MustRegister(m.Turns, m.TurnDuration, m.TurnsWithoutAccounting, m.RelayedMessages,
			m.SourceEvents, m.FollowReconnects, m.MemoryRetains)
	}
	return m
}
