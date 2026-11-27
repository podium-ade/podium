package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// IdentityPolicy is who a turn acts as, capability by capability, and what Podium does for
// somebody who writes to it from Slack, Linear or GitHub without being a Podium person. Admins
// set it once for the organization (docs/agent.md, "Who a turn acts as").
//
// A turn has an actor: the Podium person behind it, or nobody. Each capability then says
// whose credential the turn uses: the person's own, or Podium's — the Linear app, the GitHub
// App, the company's model key. A run as Podium (a system routine's) always uses Podium's,
// whatever this says.
type IdentityPolicy struct {
	// Linear is a turn's Linear MCP server: IdentityPersonThenApp, IdentityPersonOnly or
	// IdentityApp.
	Linear string `json:"linear"`
	// GitHubWrite is who a playbook pushes and opens pull requests as: IdentityPersonThenApp,
	// IdentityPersonOnly or IdentityApp.
	GitHubWrite string `json:"github_write"`
	// GitHubRead is who the assistant reads GitHub as: IdentityPersonOnly or
	// IdentityPersonThenApp.
	GitHubRead string `json:"github_read"`
	// ModelKey is whose model key a private conversation spends: IdentityPersonThenApp or
	// IdentityApp. A shared conversation is SharedModelKey's.
	ModelKey string `json:"model_key"`
	// SharedModelKey is whose model credential a turn of a shared conversation spends — a
	// multiplayer chat, a Slack thread, a Linear issue, a pull request: IdentityApp, the
	// company's whoever asks, or IdentityPlanThenApp, the asker's own subscription when they
	// signed one in and the company's otherwise. Only ever the asker's: nobody's credential
	// pays for somebody else's question.
	SharedModelKey string `json:"shared_model_key"`
	// SlackOutsiders is OutsidersRefuse or OutsidersAnswer.
	SlackOutsiders string `json:"slack_outsiders"`
	// LinearOutsiders is OutsidersRefuse, OutsidersFixed or OutsidersAnswer.
	LinearOutsiders string `json:"linear_outsiders"`
	// GitHub has no setting for somebody outside Podium: a comment of theirs starts nothing
	// but a routine on a command, whose own conditions say who may ask (PodiumPeopleOnly).
	// A policy saved with github_outsiders still has it in its JSON, read once by the
	// migration that made the review command a routine (connections.EnsureCommandRoutine).
}

// Whose credential a capability uses.
const (
	// IdentityPersonThenApp is the person's own, and Podium's when they have not connected it.
	IdentityPersonThenApp = "person_then_app"
	// IdentityPersonOnly is the person's own; without it the turn goes without, and says so.
	IdentityPersonOnly = "person_only"
	// IdentityApp is always Podium's, whoever asked.
	IdentityApp = "app"
	// IdentityPlanThenApp is the person's own subscription — a Claude, ChatGPT or Grok plan
	// they signed in with — and Podium's when they have none. A personal API key is not a
	// plan, so it is never spent this way.
	IdentityPlanThenApp = "plan_then_app"
)

// What Podium does for somebody outside it.
const (
	// OutsidersRefuse answers nothing: Slack shows 🚫, Linear says so, GitHub is confused.
	OutsidersRefuse = "refuse"
	// OutsidersFixed runs one fixed thing as Podium, and nothing they wrote is a prompt: on
	// Linear, an issue delegated to Podium is worked on.
	OutsidersFixed = "fixed"
	// OutsidersAnswer answers them as Podium, on the company's key, whatever they ask.
	OutsidersAnswer = "answer"
)

// DefaultIdentityPolicy is the policy until an admin sets one. It is how Podium behaved before
// there was a setting, except that Linear falls back to the app.
var DefaultIdentityPolicy = IdentityPolicy{
	Linear:          IdentityPersonThenApp,
	GitHubWrite:     IdentityPersonThenApp,
	GitHubRead:      IdentityPersonOnly,
	ModelKey:        IdentityPersonThenApp,
	SharedModelKey:  IdentityApp,
	SlackOutsiders:  OutsidersRefuse,
	LinearOutsiders: OutsidersRefuse,
}

// ErrInvalidIdentityPolicy is a policy with a choice its capability does not offer.
var ErrInvalidIdentityPolicy = errors.New("invalid identity policy")

// IdentityPolicyKey is the setting the policy is saved under.
const IdentityPolicyKey = "identity.policy"

// identityChoices is what each field may be. The first is what an empty field means.
var identityChoices = []struct {
	name    string
	field   func(*IdentityPolicy) *string
	allowed []string
}{
	{"linear", func(p *IdentityPolicy) *string { return &p.Linear },
		[]string{IdentityPersonThenApp, IdentityPersonOnly, IdentityApp}},
	{"github_write", func(p *IdentityPolicy) *string { return &p.GitHubWrite },
		[]string{IdentityPersonThenApp, IdentityPersonOnly, IdentityApp}},
	{"github_read", func(p *IdentityPolicy) *string { return &p.GitHubRead },
		[]string{IdentityPersonOnly, IdentityPersonThenApp}},
	{"model_key", func(p *IdentityPolicy) *string { return &p.ModelKey },
		[]string{IdentityPersonThenApp, IdentityApp}},
	{"shared_model_key", func(p *IdentityPolicy) *string { return &p.SharedModelKey },
		[]string{IdentityApp, IdentityPlanThenApp}},
	{"slack_outsiders", func(p *IdentityPolicy) *string { return &p.SlackOutsiders },
		[]string{OutsidersRefuse, OutsidersAnswer}},
	{"linear_outsiders", func(p *IdentityPolicy) *string { return &p.LinearOutsiders },
		[]string{OutsidersRefuse, OutsidersFixed, OutsidersAnswer}},
}

// withDefaults fills every empty field with its default, so a policy saved before a field
// existed reads as that field's default.
func (p IdentityPolicy) withDefaults() IdentityPolicy {
	for _, c := range identityChoices {
		if f := c.field(&p); *f == "" {
			*f = *c.field(&DefaultIdentityPolicy)
		}
	}
	return p
}

// Validate reports the first field whose choice its capability does not offer. Empty fields
// are their defaults.
func (p IdentityPolicy) Validate() error {
	p = p.withDefaults()
	for _, c := range identityChoices {
		v := *c.field(&p)
		ok := false
		for _, a := range c.allowed {
			ok = ok || v == a
		}
		if !ok {
			return fmt.Errorf("%w: %s cannot be %q", ErrInvalidIdentityPolicy, c.name, v)
		}
	}
	return nil
}

// GetIdentityPolicy is the organization's identity policy, the default until an admin sets
// one. A stored choice that is no longer offered reads as its default rather than failing
// every turn.
func (s *Store) GetIdentityPolicy(ctx context.Context) (IdentityPolicy, error) {
	var p IdentityPolicy
	err := s.GetSetting(ctx, IdentityPolicyKey, &p)
	if errors.Is(err, ErrNotFound) {
		return DefaultIdentityPolicy, nil
	}
	if err != nil {
		return DefaultIdentityPolicy, err
	}
	p = p.withDefaults()
	for _, c := range identityChoices {
		f := c.field(&p)
		ok := false
		for _, a := range c.allowed {
			ok = ok || *f == a
		}
		if !ok {
			*f = *c.field(&DefaultIdentityPolicy)
		}
	}
	return p, nil
}

// UpdateIdentityPolicy changes the fields change sets and keeps the rest as they are stored,
// in one transaction, so two admins changing different settings at once both keep theirs.
func (s *Store) UpdateIdentityPolicy(ctx context.Context, change IdentityPolicy) (IdentityPolicy, error) {
	var out IdentityPolicy
	err := pgx.BeginFunc(ctx, s.pool, func(t pgx.Tx) error {
		// Serialises updates: each reads what the one before it wrote.
		if _, err := t.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", IdentityPolicyKey); err != nil {
			return err
		}
		tx := &Store{pool: s.pool, q: s.q.WithTx(t)}
		cur, err := tx.GetIdentityPolicy(ctx)
		if err != nil {
			return err
		}
		for _, c := range identityChoices {
			if v := *c.field(&change); v != "" {
				*c.field(&cur) = v
			}
		}
		out, err = tx.PutIdentityPolicy(ctx, cur)
		return err
	})
	return out, err
}

// PutIdentityPolicy stores the organization's identity policy, with empty fields as their
// defaults.
func (s *Store) PutIdentityPolicy(ctx context.Context, p IdentityPolicy) (IdentityPolicy, error) {
	if err := p.Validate(); err != nil {
		return IdentityPolicy{}, err
	}
	p = p.withDefaults()
	if err := s.PutSetting(ctx, IdentityPolicyKey, p); err != nil {
		return IdentityPolicy{}, err
	}
	return p, nil
}
