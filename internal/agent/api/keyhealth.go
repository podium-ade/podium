package api

// Noticing that a stored API key stopped working.
//
// A key is validated when it is saved, and nothing used to look at it again. Keys do not stay
// good: they expire, they are revoked in the provider's console, a workspace is disabled. The
// first anybody heard of it was a turn failing in public — on a pull request, three times in a
// row, with the provider's raw "invalid x-api-key" as the bot's whole answer.
//
// So the refresh pass re-asks the provider about each stored global key about once an hour,
// with the same free model-list call a save makes. A key the provider refuses is recorded on
// its settings row; the conductor reads that before starting a turn that would spend it, and
// the person who saved it is told once, outside any conversation. A turn that fails on a
// refused credential asks for a check straight away instead of waiting out the hour.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// keyCheckInterval is how often a stored global key is re-validated. A check is one free
// model-list request, so the bound is about not being a nuisance to the provider rather
// than cost; an hour means a key that dies overnight is caught before the morning's turns.
const keyCheckInterval = time.Hour

// keySuspectFloor is how soon after the last check a failed turn may ask for another. A
// burst of failures is one check, not one per turn.
const keySuspectFloor = time.Minute

// keySuspectTimeout bounds a check a failed turn asked for, which runs on its own.
const keySuspectTimeout = 30 * time.Second

// KeyAlert tells one person, outside any conversation, that a key they saved stopped
// working. It is the conductor's notifiers — a Slack direct message, then email — and is
// handed in after the service is built, because they are.
type KeyAlert func(ctx context.Context, login, subject, body string) error

// keyHealth is the service's half of this file: who to tell, and which suspect checks are
// already running.
type keyHealth struct {
	// mu serialises writing a provider's settings row between a key being saved and a key
	// being checked, so a check that started before a save cannot write the old key's
	// verdict over the new key's row.
	mu         sync.Mutex
	alert      KeyAlert
	suspecting sync.Map // provider name -> struct{}
}

// SetKeyAlert hands the service the way to tell a person their key stopped working.
func (s *AgentService) SetKeyAlert(alert KeyAlert) {
	s.keys.mu.Lock()
	s.keys.alert = alert
	s.keys.mu.Unlock()
}

// checkKeysOnce re-validates every stored global API key whose last check is older than
// keyCheckInterval. It runs on the refresh pass's tick.
func (s *AgentService) checkKeysOnce(ctx context.Context) {
	for _, p := range providerSpecs {
		s.checkKey(ctx, globalTarget(p), keyCheckInterval)
	}
}

// KeyRefused reports whether the provider is refusing the stored global key for provider,
// as of the last check. It is what the conductor asks before starting a turn that would
// spend that key, so its common answer — no — costs one database read.
//
// A refusal is only believed about the key it was recorded for: a key replaced with
// `podium secret set`, which never touches the row, is not refused, and the next pass checks
// it.
func (s *AgentService) KeyRefused(ctx context.Context, provider string) (bool, error) {
	if s.store == nil || s.secrets == nil {
		return false, nil
	}
	p, err := findProvider(provider)
	if err != nil {
		return false, err
	}
	t := globalTarget(p)
	row, ok, err := s.providerRow(ctx, t)
	if err != nil || !ok || row.RefusedAt.IsZero() || row.authKind() != AuthAPIKey {
		return false, err
	}
	version, err := s.credSecretVersion(ctx, t)
	if err != nil {
		return false, err
	}
	return version == row.SecretVersion, nil
}

// SuspectKey asks for the stored global key for provider to be checked now, because a turn
// that spent it was refused. It returns at once; the check runs on its own, and a check
// already running for that provider absorbs this one.
func (s *AgentService) SuspectKey(provider string) {
	if s.store == nil || s.secrets == nil {
		return
	}
	p, err := findProvider(provider)
	if err != nil {
		return
	}
	if _, running := s.keys.suspecting.LoadOrStore(p.name, struct{}{}); running {
		return
	}
	go func() {
		defer s.keys.suspecting.Delete(p.name)
		ctx, cancel := context.WithTimeout(context.Background(), keySuspectTimeout)
		defer cancel()
		s.checkKey(ctx, globalTarget(p), keySuspectFloor)
	}()
}

// checkKey re-validates one stored API key when its last check is older than every, and
// records the answer on its row. Only a refusal and a success are recorded: a provider that
// could not be reached says nothing about the key, so the row is left alone and the next
// tick tries again.
func (s *AgentService) checkKey(ctx context.Context, t credTarget, every time.Duration) {
	row, ok, err := s.providerRow(ctx, t)
	if err != nil || !ok || row.authKind() != AuthAPIKey {
		return
	}
	if !row.CheckedAt.IsZero() && time.Since(row.CheckedAt) < every {
		return
	}
	version, err := s.credSecretVersion(ctx, t)
	if err != nil || version == 0 || version != row.SecretVersion {
		// Unreadable, removed, or replaced behind the row's back: there is no key this row
		// describes to check.
		return
	}
	key, err := s.secrets.ReadSecret(ctx, t.owner, t.secret())
	if err != nil {
		s.logger.WarnContext(ctx, "reading a stored key to check it failed; this will be tried again",
			"provider", t.p.name, "error", err)
		return
	}
	defer zero(key)

	callCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	_, verr := t.p.validate(callCtx, s, key)
	cancel()
	said := operatorDetail(validationDetail(verr), key)
	if verr != nil && !errors.Is(verr, errKeyRefused) {
		s.logger.WarnContext(ctx, "could not check a stored key with its provider; this will be tried again",
			"provider", t.p.name, "provider_message", said)
		return
	}

	// The alert, when there is one, goes out after the lock is released: a slow Slack or
	// mail server must not hold up somebody saving a key.
	var tell func()
	defer func() {
		if tell != nil {
			tell()
		}
	}()
	s.keys.mu.Lock()
	defer s.keys.mu.Unlock()
	cur, ok, err := s.providerRow(ctx, t)
	if err != nil || !ok || cur.SecretVersion != row.SecretVersion {
		// Saved again while the provider was being asked. The verdict is about a key that is
		// no longer stored.
		return
	}
	now := time.Now().UTC()
	cur.CheckedAt = now
	if verr == nil {
		was := cur.RefusedAt
		cur.RefusedAt, cur.Refusal = time.Time{}, ""
		if _, err := s.putProviderRow(ctx, t, cur); err != nil {
			s.logger.WarnContext(ctx, "recording a key check failed", "provider", t.p.name, "error", err)
			return
		}
		if !was.IsZero() {
			s.logger.InfoContext(ctx, "a stored key the provider had refused works again",
				"provider", t.p.name, "key_hint", cur.KeyHint, "refused_at", was)
		}
		return
	}

	first := cur.RefusedAt.IsZero()
	if first {
		cur.RefusedAt = now
	}
	cur.Refusal = said
	if _, err := s.putProviderRow(ctx, t, cur); err != nil {
		s.logger.ErrorContext(ctx, "the provider refused a stored key, and recording that failed",
			"provider", t.p.name, "key_hint", cur.KeyHint, "provider_message", said, "error", err)
		return
	}
	if !first {
		return
	}
	s.logger.ErrorContext(ctx, "the provider refused a stored key; turns that would spend it are "+
		"stopped until a working one is saved", "provider", t.p.name, "key_hint", cur.KeyHint,
		"set_by", cur.SetBy, "provider_message", said)
	alert := s.keys.alert
	tell = func() { s.alertRefused(ctx, alert, t, cur) }
}

// alertRefused tells the person who saved a key that it stopped working. Somebody who saved
// it with the dev token or the local transport has no address, and the log line above is all
// there is.
func (s *AgentService) alertRefused(ctx context.Context, alert KeyAlert, t credTarget, row providerRow) {
	if alert == nil || !strings.Contains(row.SetBy, "@") {
		return
	}
	subject := fmt.Sprintf("Podium's %s API key stopped working", t.p.label)
	var b strings.Builder
	fmt.Fprintf(&b, "%s refused the company %s API key", t.p.label, t.p.label)
	if row.KeyHint != "" {
		fmt.Fprintf(&b, " (…%s)", row.KeyHint)
	}
	fmt.Fprintf(&b, " at %s.", row.RefusedAt.Format("2006-01-02 15:04 MST"))
	if row.Refusal != "" {
		fmt.Fprintf(&b, " It said: %q.", row.Refusal)
	}
	b.WriteString("\n\nTurns that would use it are stopped before they start until a working key " +
		"is saved under Settings → Models.")
	if base := strings.TrimSuffix(s.env.PublicURL, "/"); base != "" {
		fmt.Fprintf(&b, "\n\n%s/agent/settings/models", base)
	}
	if err := alert(ctx, row.SetBy, subject, b.String()); err != nil {
		s.logger.WarnContext(ctx, "telling the person who saved a refused key failed",
			"provider", t.p.name, "set_by", row.SetBy, "error", err)
		return
	}
	s.logger.InfoContext(ctx, "told the person who saved a refused key", "provider", t.p.name,
		"set_by", row.SetBy)
}
