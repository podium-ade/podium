package linear

import "github.com/prometheus/client_golang/prometheus"

// Poll results, as the polls_total metric labels them.
const (
	PollOK          = "ok"
	PollError       = "error"
	PollRateLimited = "ratelimited"
)

// Event kinds, as the events_total metric labels them. A delegation and a mention are the
// agent app's assignments; its follow-ups are prompts in a session.
const (
	EventAssignment = "assignment"
	EventFollowup   = "followup"
	EventDelegation = "delegation"
	EventMention    = "mention"
)

// Webhook delivery outcomes, as the webhook_deliveries_total metric labels them.
const (
	DeliveryRejected  = "rejected"
	DeliveryDuplicate = "duplicate"
	DeliveryIgnored   = "ignored"
	DeliveryAccepted  = "accepted"
	DeliveryLogged    = "logged"
	DeliveryRevoked   = "revoked"
)

// Metrics is what the Linear source reports on /metrics. A struct rather than package
// globals, for the same reason the conductor's is: two sources in one test process must not
// fight over a default registry.
type Metrics struct {
	Polls   *prometheus.CounterVec
	Events  *prometheus.CounterVec
	Backoff prometheus.Gauge
	// Deliveries counts the agent app's webhook deliveries, by the payload's type and what
	// was done with it. A delivery refused before its body could be trusted has type
	// "unknown", so a forger cannot mint label values.
	Deliveries *prometheus.CounterVec
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
			Help: "Inbound events derived from Linear, by kind: assignment or followup when " +
				"polled, and delegation, mention or followup from the agent app.",
		}, []string{"kind"}),
		Backoff: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "podium_agent_linear_backoff_seconds",
			Help: "How long the next Linear poll is being delayed by rate-limit backoff. " +
				"Zero when polling at the configured interval.",
		}),
		Deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "podium_agent_linear_webhook_deliveries_total",
			Help: "Linear agent app webhook deliveries, by type and outcome: rejected, duplicate, " +
				"ignored, accepted, logged or revoked.",
		}, []string{"type", "outcome"}),
	}
	if reg != nil {
		reg.MustRegister(m.Polls, m.Events, m.Backoff, m.Deliveries)
	}
	return m
}
