package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/api"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/connections"
	"github.com/podium-ade/podium/internal/agent/ghreview"
	"github.com/podium-ade/podium/internal/agent/github"
	agentlinear "github.com/podium-ade/podium/internal/agent/linear"
	agentslack "github.com/podium-ade/podium/internal/agent/slack"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/internal/agent/webhooks"
	"github.com/podium-ade/podium/internal/netlisten"
)

// checkTimeout bounds the calls that try a pasted connection against Slack, GitHub and
// Linear.
const checkTimeout = 15 * time.Second

// liveConnections is the half of the conductor Settings → Connections changes: the Slack
// source, the GitHub review source, the GitHub App turns mint from, and the Linear source.
// Apply swaps them while everything else keeps running, turns in flight included. A turn
// that started on the old Slack source keeps posting through it, which still works for as
// long as the old bot token does.
type liveConnections struct {
	// env is the process configuration before any saved connection was laid over it.
	env    config.Config
	store  *store.Store
	vault  connections.Vault
	logger *slog.Logger
	// webhooks owns the webhook listeners. The GitHub App's and the Linear app's may be one
	// address, which only one owner can bind, so neither source binds its own.
	webhooks *webhooks.Server

	// Set by New once they exist, before Run.
	conductor *conductor.Conductor
	svc       *api.AgentService

	// linearPlaybook is the playbook Linear runs, the one marked linear: true. Set by New.
	linearPlaybook func() string
	// linearMetrics is shared by every Linear source this process builds, because their
	// collectors are registered once.
	linearMetrics *agentlinear.Metrics
	// linearSession and linearCursor are the store, as the poller is allowed to see it.
	linearSession agentlinear.SessionLookup
	linearCursor  agentlinear.CursorStore

	mu      sync.Mutex
	running config.Config
	slack   *agentslack.Source
	gh      *ghreview.Source
	// The Linear source is one of two, never both: each would start every ticket. A saved
	// app runs its webhooks (linearApp); with none, PODIUM_AGENT_LINEAR_API_KEY runs the
	// poller (linearPoll). linearRow is the saved app's row, which says who it signed in as.
	linearApp  *agentlinear.AppSource
	linearPoll *agentlinear.Source
	linearRow  *connections.Linear
	// ctx is Run's, nil before it. stop ends the sources started on it and waits for them
	// to return, so a webhook listener has let go of its port before a new one binds it.
	// stopLinear is the same for the Linear source, which is swapped on its own.
	ctx        context.Context
	stop       func()
	stopLinear func()
}

var _ api.ConnectionApplier = (*liveConnections)(nil)

// build makes the sources and the App cfg asks for. Nothing is dialled: that is Run's.
func (l *liveConnections) build(cfg config.Config) (*agentslack.Source, *ghreview.Source, *github.Client, error) {
	var slack *agentslack.Source
	if cfg.SlackEnabled() {
		var err error
		slack, err = agentslack.New(agentslack.Options{
			AppToken: cfg.SlackAppToken,
			BotToken: cfg.SlackBotToken,
			Logger:   l.logger,
			TeamIDs:  cfg.SlackTeamIDs,
		})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	var gh *ghreview.Source
	if cfg.GitHubSourceEnabled() {
		var err error
		gh, err = newGitHubSource(cfg, l.store, slack, l.webhooks, l.logger)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if slack != nil && gh != nil {
		slack.SetReview(gh)
	}
	// The App, when one is configured. Nil leaves every playbook on the older path — its
	// own podium.agent.github_token — untouched.
	var app *github.Client
	if cfg.GitHubAppEnabled() {
		pem, err := cfg.GitHubAppPrivateKey()
		if err != nil {
			return nil, nil, nil, err
		}
		app, err = github.New(github.Options{AppID: cfg.GitHubAppID, PrivateKeyPEM: pem})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return slack, gh, app, nil
}

// start runs the current sources on ctx. fatal is where the first start's errors go: a
// bot that cannot reach Slack at boot fails loudly. A source swapped in later only logs,
// because Check already tried its credentials and a conductor serving turns should not
// stop over a connection an operator can fix and save again.
func (l *liveConnections) start(ctx context.Context, fatal chan<- error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ctx = ctx
	l.startLocked(fatal)
	l.startLinearLocked(fatal)
}

// startLocked runs Slack and the GitHub review source, which are swapped together.
func (l *liveConnections) startLocked(fatal chan<- error) {
	run, stop := l.group(fatal)
	if l.slack != nil {
		run(agentslack.Kind, l.slack.Run)
	}
	if l.gh != nil {
		run(ghreview.Kind, l.gh.Run)
	}
	l.stop = stop
}

// startLinearLocked runs the Linear source. The app's webhooks run on every conductor: a
// delivery reaches whichever one the tunnel sends it to. The poller runs on one at a time,
// because every one that polled would start the same ticket.
func (l *liveConnections) startLinearLocked(fatal chan<- error) {
	run, stop := l.group(fatal)
	switch {
	case l.linearApp != nil:
		run(agentlinear.Kind, l.linearApp.Run)
	case l.linearPoll != nil:
		poll := l.linearPoll
		run(agentlinear.Kind, func(ctx context.Context) error {
			return l.conductor.WhileLeader(ctx, "source:linear", poll.Run)
		})
	}
	l.stopLinear = stop
}

// group starts sources on a context of their own. stop ends them and waits for them to
// return. fatal is where a first start's errors go: a bot that cannot reach Slack at boot
// fails loudly. A source swapped in later only logs, because Check already tried its
// credentials and a conductor serving turns should not stop over a connection an operator
// can fix and save again.
func (l *liveConnections) group(fatal chan<- error) (run func(string, func(context.Context) error), stop func()) {
	ctx, cancel := context.WithCancel(l.ctx)
	var wg sync.WaitGroup
	run = func(name string, run func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := run(ctx)
			switch {
			case ctx.Err() != nil:
			case fatal != nil:
				fatal <- err
			case err != nil:
				l.logger.ErrorContext(ctx, "a source switched on from Settings → Connections stopped; "+
					"check the connection and save it again", "source", name, "error", err)
			}
		}()
	}
	return run, func() {
		cancel()
		wg.Wait()
	}
}

// Check implements api.ConnectionApplier.
func (l *liveConnections) Check(ctx context.Context, next config.Config) (api.Checked, error) {
	l.mu.Lock()
	running := l.running
	revoked := l.linearApp != nil && l.linearApp.Revoked()
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	var checked api.Checked
	if next.SlackEnabled() && connections.SlackDiffers(next, running, nil) {
		if err := agentslack.Check(ctx, next.SlackAppToken, next.SlackBotToken); err != nil {
			return api.Checked{}, err
		}
	}
	if next.GitHubAppEnabled() && connections.GitHubDiffers(next, running, nil) {
		pem, err := next.GitHubAppPrivateKey()
		if err != nil {
			return api.Checked{}, err
		}
		app, err := github.New(github.Options{AppID: next.GitHubAppID, PrivateKeyPEM: pem})
		if err != nil {
			return api.Checked{}, err
		}
		if _, err := app.Identity(ctx); err != nil {
			return api.Checked{}, fmt.Errorf("github did not accept the app id and private key: %w", err)
		}
	}
	// A new webhook address has to be one this host can listen on. One this process already
	// listens on is not tried: the GitHub App's or the Linear app's, which may share it. A
	// source moved onto it joins that listener, and one moved off it lets go of it.
	if next.GitHubSourceEnabled() && !l.holds(next.GitHubWebhookListen) {
		if err := canListen(next.GitHubWebhookListen); err != nil {
			return api.Checked{}, err
		}
	}
	// An app Linear revoked is tried again even unchanged: saving it is how an operator says
	// it was authorized again, and the save is refused if it was not.
	if next.LinearAppEnabled() && (connections.LinearDiffers(next, running, nil) || revoked) {
		// Refused here rather than found when the source cannot be built: the app would be
		// saved, the poller turned off, and nothing would answer.
		if l.linearPlaybook == nil || l.linearPlaybook() == "" {
			return api.Checked{}, agentlinear.ErrNoPlaybook("then save the Linear app again")
		}
		app, err := checkLinearApp(ctx, next)
		if err != nil {
			return api.Checked{}, err
		}
		checked.Linear = app
	}
	// The same for the Linear app's address.
	if next.LinearAppEnabled() && !l.holds(next.LinearWebhookListen) {
		if err := canListen(next.LinearWebhookListen); err != nil {
			return api.Checked{}, err
		}
	}
	return checked, nil
}

// holds reports whether this process is listening on addr already.
func (l *liveConnections) holds(addr string) bool {
	return l.webhooks != nil && l.webhooks.Holds(addr)
}

// canListen proves addr can be bound now, and lets go of it.
func canListen(addr string) error {
	ln, err := netlisten.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("the webhook address cannot be listened on: %w", err)
	}
	_ = ln.Close()
	return nil
}

// checkLinearApp signs in with the app's client credentials and asks Linear who that is.
// The token it mints is not kept: the credentials may yet be refused here, and the source
// that will use them mints its own. The errors say what to change in Linear, because
// that is where every one of them is fixed.
func checkLinearApp(ctx context.Context, cfg config.Config) (*api.LinearApp, error) {
	tokens, err := agentlinear.NewTokenSource(agentlinear.TokenSourceOptions{
		TokenURL:     cfg.LinearTokenURL,
		ClientID:     cfg.LinearClientID,
		ClientSecret: cfg.LinearClientSecret,
	})
	if err != nil {
		return nil, err
	}
	client, err := agentlinear.NewClient(agentlinear.ClientOptions{Endpoint: cfg.LinearURL, Tokens: tokens})
	if err != nil {
		return nil, err
	}
	id, err := client.AppIdentity(ctx)
	var tokErr *agentlinear.TokenError
	var gqlErr *agentlinear.GraphQLError
	switch {
	case errors.As(err, &tokErr):
		return nil, fmt.Errorf("linear did not accept the client id and secret (%s). Check both on the "+
			"application's page under Settings → API → OAuth applications, and that \"Client "+
			"credentials\" is turned on there", linearReason(tokErr))
	case errors.As(err, &gqlErr) && gqlErr.Unauthorized():
		return nil, fmt.Errorf("linear issued a token for the client id and secret, then refused it: %s",
			gqlErr.Message)
	case err != nil:
		return nil, fmt.Errorf("could not ask Linear who the app is: %w", err)
	}
	if !id.App {
		return nil, fmt.Errorf("the client id and secret sign in to Linear as %q, which is a person, "+
			"not an app. Use the client id and secret of an OAuth application, with \"Client "+
			"credentials\" turned on", firstNonEmpty(id.Name, id.DisplayName, id.UserID))
	}
	// An app user's name is the application's name, and its display name is the handle
	// an @mention uses. The name is what an operator recognises.
	return &api.LinearApp{
		UserID:             id.UserID,
		UserName:           firstNonEmpty(id.Name, id.DisplayName),
		OrganizationID:     id.OrganizationID,
		OrganizationName:   id.OrganizationName,
		OrganizationURLKey: id.OrganizationURLKey,
	}, nil
}

// linearReason is Linear's own words for a refused grant, or its status when it gave none.
func linearReason(e *agentlinear.TokenError) string {
	switch {
	case e.Description != "":
		return e.Description
	case e.Code != "":
		return e.Code
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Apply implements api.ConnectionApplier.
func (l *liveConnections) Apply(ctx context.Context) error {
	savedSlack, savedGitHub, savedLinear, err := connections.Load(ctx, l.store, l.vault)
	if err != nil {
		return err
	}
	next := connections.Overlay(l.env, savedSlack, savedGitHub, savedLinear)
	if err := next.Validate(); err != nil {
		return err
	}
	return l.apply(ctx, next, savedLinear)
}

// apply switches to next. Everything that changed is built before anything is stopped, so a
// source that cannot be built leaves the running ones as they were.
func (l *liveConnections) apply(ctx context.Context, next config.Config, savedLinear *connections.Linear) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	slackChanged := connections.SlackDiffers(next, l.running, nil)
	githubChanged := connections.GitHubDiffers(next, l.running, nil)
	// A save that clears a revoke changes no setting, and still has to start the app again.
	linearChanged := connections.LinearDiffers(next, l.running, nil) ||
		(l.linearApp != nil && l.linearApp.Revoked() && (savedLinear == nil || savedLinear.RevokedAt == nil))
	if !slackChanged && !githubChanged && !linearChanged {
		return nil
	}
	if l.ctx == nil || l.ctx.Err() != nil {
		return errors.New("the conductor is not running")
	}
	var (
		slack     *agentslack.Source
		gh        *ghreview.Source
		app       *github.Client
		linApp    *agentlinear.AppSource
		linPoll   *agentlinear.Source
		buildErr  error
		swapSlack = slackChanged || githubChanged
	)
	if swapSlack {
		if slack, gh, app, buildErr = l.build(next); buildErr != nil {
			return buildErr
		}
	}
	if linearChanged {
		if linApp, linPoll, buildErr = l.buildLinear(next, savedLinear); buildErr != nil {
			return buildErr
		}
	}
	if swapSlack {
		if err := l.conductor.SetGitHub(app); err != nil {
			return err
		}
		// Both sources are rebuilt whichever changed: each holds the other, Slack to hand a
		// mention of a pull request to the review and the review to post into a bound thread.
		l.stop()
		l.slack, l.gh = slack, gh
		// A nil pointer is passed as a nil interface, so ReplaceSource removes the kind
		// rather than adding a source that is not there.
		var slackSrc, ghSrc conductor.Source
		if slack != nil {
			slackSrc = slack
		}
		if gh != nil {
			ghSrc = gh
		}
		l.conductor.ReplaceSource(agentslack.Kind, slackSrc)
		l.conductor.ReplaceSource(ghreview.Kind, ghSrc)
		l.startLocked(nil)
	}
	if linearChanged {
		// The old Linear source lets go of its webhook path before the new one mounts its
		// own, which may be the same path on the same address.
		l.stopLinear()
		l.linearApp, l.linearPoll, l.linearRow = linApp, linPoll, savedLinear
		l.conductor.ReplaceSource(agentlinear.Kind, l.linearSourceLocked())
		l.startLinearLocked(nil)
	}
	l.running = next

	var dir api.SlackDirectory
	if l.slack != nil {
		dir = l.slack
	}
	if l.svc != nil {
		l.svc.SetRunning(next, dir)
	}
	l.logger.InfoContext(ctx, "connections: switched to the saved connections",
		"slack", l.slack != nil, "github_reviews", l.gh != nil, "github_app", next.GitHubAppEnabled(),
		"linear_app", l.linearApp != nil, "linear_polling", l.linearPoll != nil,
		"slack_changed", slackChanged, "github_changed", githubChanged, "linear_changed", linearChanged)
	return nil
}

// linearSource is the Linear source in force, nil when Linear is off.
func (l *liveConnections) linearSource() conductor.Source {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.linearSourceLocked()
}

// linearSourceLocked is linearSource as a nil interface when there is none, so ReplaceSource
// removes the kind rather than adding a source that is not there.
func (l *liveConnections) linearSourceLocked() conductor.Source {
	switch {
	case l.linearApp != nil:
		return l.linearApp
	case l.linearPoll != nil:
		return l.linearPoll
	}
	return nil
}

// buildLinear makes the Linear source cfg asks for: the app's webhooks when an app is saved,
// whether or not PODIUM_AGENT_LINEAR_API_KEY is set, and otherwise the poller when the key
// is. Two sources would start every ticket twice, so a saved app wins.
func (l *liveConnections) buildLinear(
	cfg config.Config, saved *connections.Linear,
) (*agentlinear.AppSource, *agentlinear.Source, error) {
	playbook := l.linearPlaybook
	if playbook == nil {
		playbook = func() string { return "" }
	}
	switch {
	case cfg.LinearAppEnabled():
		if saved == nil {
			return nil, nil, errors.New("linear: an app is configured but no saved app was read")
		}
		app, err := l.newLinearApp(cfg, saved, playbook)
		return app, nil, err
	case cfg.LinearEnabled():
		poll, err := agentlinear.New(agentlinear.Options{
			APIKey:       cfg.LinearAPIKey,
			Endpoint:     cfg.LinearURL,
			PollInterval: cfg.LinearPollInterval,
			// A ticket has no channel and no /playbook prefix, so the source names the playbook
			// and the profile's routing rules are bypassed. Zero playbooks claiming Linear is
			// only a misconfiguration when a key is set, which is exactly here.
			Playbook: playbook,
			Logger:   l.logger,
			Metrics:  l.linearMetrics,
			TaskURL:  taskURL(cfg),
			Session:  l.linearSession,
			Cursor:   l.linearCursor,
		})
		return nil, poll, err
	}
	return nil, nil, nil
}

// newLinearApp is the agent app's source, signed in with a token every conductor on this
// database shares through the vault.
func (l *liveConnections) newLinearApp(
	cfg config.Config, saved *connections.Linear, playbook func() string,
) (*agentlinear.AppSource, error) {
	opts := agentlinear.TokenSourceOptions{
		TokenURL:     cfg.LinearTokenURL,
		ClientID:     cfg.LinearClientID,
		ClientSecret: cfg.LinearClientSecret,
		Logger:       l.logger,
	}
	if l.vault != nil {
		clientID := cfg.LinearClientID
		opts.Load = func(ctx context.Context) (agentlinear.Token, error) {
			tok, err := connections.LoadLinearToken(ctx, l.vault, clientID)
			return agentlinear.Token{AccessToken: tok.AccessToken, ExpiresAt: tok.ExpiresAt}, err
		}
		opts.Store = func(ctx context.Context, tok agentlinear.Token) error {
			return connections.StoreLinearToken(ctx, l.vault, connections.LinearToken{
				ClientID: clientID, AccessToken: tok.AccessToken, ExpiresAt: tok.ExpiresAt,
			})
		}
	}
	tokens, err := agentlinear.NewTokenSource(opts)
	if err != nil {
		return nil, err
	}
	client, err := agentlinear.NewClient(agentlinear.ClientOptions{Endpoint: cfg.LinearURL, Tokens: tokens})
	if err != nil {
		return nil, err
	}
	var serve func(context.Context, string, string, http.Handler) error
	if l.webhooks != nil {
		serve = l.webhooks.Serve
	}
	return agentlinear.NewApp(agentlinear.AppOptions{
		Client:         client,
		WebhookSecret:  cfg.LinearWebhookSecret,
		Listen:         cfg.LinearWebhookListen,
		Serve:          serve,
		AppUserID:      saved.AppUserID,
		OrganizationID: saved.OrganizationID,
		AppName:        saved.AppUserName,
		Playbook:       playbook,
		TaskURL:        taskURL(cfg),
		Revoked:        saved.RevokedAt != nil,
		OnRevoked:      l.linearRevoked(cfg.LinearClientID),
		Metrics:        l.linearMetrics,
		Logger:         l.logger,
	})
}

// linearRevoked records on the saved app that Linear revoked it, so Settings → Connections
// says so until it is saved again.
func (l *liveConnections) linearRevoked(clientID string) func(context.Context) {
	return func(ctx context.Context) {
		if l.store == nil {
			return
		}
		if err := connections.MarkLinearRevoked(ctx, l.store, clientID, time.Now()); err != nil {
			l.logger.ErrorContext(ctx, "linear: recording that Linear revoked the app failed", "error", err)
		}
	}
}

// taskURL renders a link to a task page as a HUMAN reaches the web UI, which is not always
// how this process reaches the API. Linear uses it only for a fallback attachment link.
func taskURL(cfg config.Config) func(string) string {
	return func(taskID string) string {
		if taskID == "" {
			return ""
		}
		return strings.TrimSuffix(cfg.WebURL(), "/") + "/tasks/" + taskID
	}
}

// githubSource is the GitHub source running now, nil when no App with webhooks is saved.
func (l *liveConnections) githubSource() *ghreview.Source {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.gh
}

// newGitHubSource is the pull-request review source: the App's webhooks, and the threads in
// Slack bound to a review.
func newGitHubSource(
	cfg config.Config, st *store.Store, slack *agentslack.Source, hooks *webhooks.Server, logger *slog.Logger,
) (*ghreview.Source, error) {
	pem, err := cfg.GitHubAppPrivateKey()
	if err != nil {
		return nil, err
	}
	key, err := github.ParsePrivateKey(pem)
	if err != nil {
		return nil, err
	}
	// A nil *Source in the interface would read as Slack being connected.
	var bridge ghreview.SlackBridge
	if slack != nil {
		bridge = slack
	}
	var serve func(context.Context, string, string, http.Handler) error
	if hooks != nil {
		serve = hooks.Serve
	}
	return ghreview.NewSource(ghreview.SourceOptions{
		AppID:         cfg.GitHubAppID,
		PrivateKey:    key,
		WebhookSecret: cfg.GitHubWebhookSecret,
		Listen:        cfg.GitHubWebhookListen,
		Serve:         serve,
		Slack:         bridge,
		Logger:        logger,
		TaskURL: func(taskID string) string {
			if taskID == "" {
				return ""
			}
			return strings.TrimSuffix(cfg.WebURL(), "/") + "/tasks/" + taskID
		},
		Filters: func(ctx context.Context) (ghreview.Filters, error) {
			f, err := connections.LoadGitHubFilters(ctx, st)
			if err != nil {
				return ghreview.Filters{}, err
			}
			return ghreview.Filters{
				AllowedActors:    f.AllowedActors,
				IgnoredActors:    f.IgnoredActors,
				IgnoreBots:       f.IgnoreBots,
				TriggerPhrases:   f.TriggerPhrases,
				ReplyMode:        f.ReplyMode,
				NoAutoReview:     f.NoAutoReview,
				ExcludedBranches: f.ExcludedBranches,

				ConversationReplies: f.ConversationReplies,
			}, nil
		},
		Reviews: ghreview.ReviewMemory{
			Get: func(ctx context.Context, sourceKey string) (int64, error) {
				sess, err := st.GetSessionByKey(ctx, sourceKey)
				if errors.Is(err, store.ErrNotFound) {
					return 0, nil
				}
				if err != nil {
					return 0, err
				}
				return sess.GitHubReviewID, nil
			},
			Set: func(ctx context.Context, sourceKey string, reviewID int64) error {
				return st.SetGitHubReviewID(ctx, sourceKey, reviewID)
			},
		},
		Session: func(ctx context.Context, sourceKey string) (time.Time, bool) {
			sess, err := st.GetSessionByKey(ctx, sourceKey)
			if err != nil {
				return time.Time{}, false
			}
			if sess.LastTurnAt != nil {
				return *sess.LastTurnAt, true
			}
			return time.Time{}, true
		},
		Surfaces: ghreview.Surfaces{
			LookupSlack: func(ctx context.Context, slackRef string) (string, bool, error) {
				row, err := st.GetReviewSurface(ctx, store.ReviewSurfaceSlack, slackRef)
				if errors.Is(err, store.ErrNotFound) {
					return "", false, nil
				}
				if err != nil {
					return "", false, err
				}
				return row.SourceKey, true, nil
			},
			BindSlack: func(ctx context.Context, slackRef, sourceKey string) error {
				err := st.BindReviewSurface(ctx, sourceKey, store.ReviewSurfaceSlack, slackRef)
				if errors.Is(err, store.ErrSurfaceBound) {
					return ghreview.ErrBoundToOther
				}
				return err
			},
			ListSlack: func(ctx context.Context, sourceKey string) ([]string, error) {
				rows, err := st.ListReviewSurfaces(ctx, sourceKey, store.ReviewSurfaceSlack)
				if err != nil {
					return nil, err
				}
				out := make([]string, 0, len(rows))
				for _, r := range rows {
					out = append(out, r.Ref)
				}
				return out, nil
			},
		},
	})
}

// slackSource is the Slack source now, or nil when Slack is not connected.
func (l *liveConnections) slackSource() *agentslack.Source {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.slack
}
