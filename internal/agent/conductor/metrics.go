package conductor

import "github.com/prometheus/client_golang/prometheus"

// Metrics is what the conductor reports on /metrics. It is a struct rather than package
// globals so two conductors in one test process do not fight over a default registry.
type Metrics struct {
	Turns                   *prometheus.CounterVec
	TurnDuration            *prometheus.HistogramVec
	TurnsWithoutAccounting  prometheus.Counter
	HostTurnsQueued         prometheus.Counter
	HostWarmEvictions       prometheus.Counter
	WarmInjects             *prometheus.CounterVec
	TurnFirstMessage        *prometheus.HistogramVec
	Delegations             *prometheus.CounterVec
	RelayedMessages         *prometheus.CounterVec
	SourceEvents            *prometheus.CounterVec
	FollowReconnects        prometheus.Counter
	MemoryRetains           *prometheus.CounterVec
	MemoryExtractionsFailed prometheus.Gauge
}

// NewMetrics registers the conductor's collectors on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Turns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_turns_total",
			Help: "Turns that reached a terminal status, by source, playbook and status.",
		}, []string{"source", "playbook", "status"}),
		TurnDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "podium_agent_turn_duration_seconds",
			Help:    "Wall time from the inbound event to the turn's terminal status.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 12),
		}, []string{"playbook"}),
		TurnsWithoutAccounting: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "podium_agent_turns_without_accounting_total",
			Help: "Turns that succeeded without reporting num_turns and cost_usd, which are " +
				"left null. Anything above zero is lost accounting, not a failed turn.",
		}),
		HostTurnsQueued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "podium_agent_host_turns_queued_total",
			Help: "Host turns that had to wait for a concurrency slot. Anything much above " +
				"zero means this host is the bottleneck, not the fleet.",
		}),
		HostWarmEvictions: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "podium_agent_host_warm_evictions_total",
			Help: "Warm host turns asked to give their slot up to a turn with work. A high rate " +
				"means the warm window is buying little on this host: raise the slots or shorten it.",
		}),
		WarmInjects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_warm_injects_total",
			Help: "Messages answered by a turn that was still warm, with no new process or " +
				"task, by where the assistant runs.",
		}, []string{"placement"}),
		TurnFirstMessage: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "podium_agent_turn_first_message_seconds",
			Help: "Wall time from the inbound event to the first thing the turn said, by where it " +
				"ran: host, nodes, a sandbox provider's name, or playbook for a playbook's task. " +
				"It is the boot a person waits through, which is what placement trades.",
			Buckets: prometheus.ExponentialBuckets(0.25, 2, 12),
		}, []string{"placement"}),
		Delegations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_delegations_total",
			Help: "Tasks a host turn delegated that reached a terminal status, by playbook " +
				"and status.",
		}, []string{"playbook", "status"}),
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
			Help: "End-of-turn hand-offs to the shared memory, by result: accepted, error or " +
				"redacted. `accepted` means Hindsight took the work, NOT that a fact was " +
				"written — extraction happens afterwards and is counted by " +
				"podium_agent_memory_extraction_failed. A rising error count is a memory " +
				"outage and never a failed turn.",
		}, []string{"result"}),
		MemoryExtractionsFailed: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "podium_agent_memory_extraction_failed",
			Help: "Retains Hindsight accepted and then failed to extract any fact from, as of " +
				"the last check. Anything above zero means memories are being lost silently: " +
				"the turns looked fine and the retains were counted as accepted.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.Turns, m.TurnDuration, m.TurnsWithoutAccounting, m.HostTurnsQueued,
			m.HostWarmEvictions, m.WarmInjects, m.TurnFirstMessage,
			m.Delegations, m.RelayedMessages,
			m.SourceEvents, m.FollowReconnects, m.MemoryRetains, m.MemoryExtractionsFailed)
	}
	return m
}
