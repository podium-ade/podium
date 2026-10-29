package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/api"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/connections"
	"github.com/podium-ade/podium/internal/agent/ghreview"
	"github.com/podium-ade/podium/internal/agent/github"
	agentslack "github.com/podium-ade/podium/internal/agent/slack"
	"github.com/podium-ade/podium/internal/agent/store"
)

// checkTimeout bounds the calls that try a pasted connection against Slack and GitHub.
const checkTimeout = 15 * time.Second

// liveConnections is the half of the conductor Settings → Connections changes: the Slack
// source, the GitHub review source and the GitHub App turns mint from. Apply swaps them
// while everything else keeps running, turns in flight included. A turn that started on
// the old Slack source keeps posting through it, which still works for as long as the old
// bot token does.
type liveConnections struct {
	// env is the process configuration before any saved connection was laid over it.
	env    config.Config
	store  *store.Store
	vault  connections.Vault
	logger *slog.Logger

	// Set by New once they exist, before Run.
	conductor *conductor.Conductor
	svc       *api.AgentService

	mu      sync.Mutex
	running config.Config
	slack   *agentslack.Source
	gh      *ghreview.Source
	// ctx is Run's, nil before it. stop ends the sources started on it and waits for them
	// to return, so a webhook listener has let go of its port before a new one binds it.
	ctx  context.Context
	stop func()
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
		})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	var gh *ghreview.Source
	if cfg.GitHubSourceEnabled() {
		var err error
		gh, err = newGitHubSource(cfg, l.store, slack, l.logger)
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
}

func (l *liveConnections) startLocked(fatal chan<- error) {
	ctx, cancel := context.WithCancel(l.ctx)
	var wg sync.WaitGroup
	run := func(name string, run func(context.Context) error) {
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
	if l.slack != nil {
		run(agentslack.Kind, l.slack.Run)
	}
	if l.gh != nil {
		run(ghreview.Kind, l.gh.Run)
	}
	l.stop = func() {
		cancel()
		wg.Wait()
	}
}

// Check implements api.ConnectionApplier.
func (l *liveConnections) Check(ctx context.Context, next config.Config) error {
	l.mu.Lock()
	running := l.running
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	if next.SlackEnabled() && connections.SlackDiffers(next, running, nil) {
		if err := agentslack.Check(ctx, next.SlackAppToken, next.SlackBotToken); err != nil {
			return err
		}
	}
	if next.GitHubAppEnabled() && connections.GitHubDiffers(next, running, nil) {
		pem, err := next.GitHubAppPrivateKey()
		if err != nil {
			return err
		}
		app, err := github.New(github.Options{AppID: next.GitHubAppID, PrivateKeyPEM: pem})
		if err != nil {
			return err
		}
		if _, err := app.Identity(ctx); err != nil {
			return fmt.Errorf("github did not accept the app id and private key: %w", err)
		}
	}
	// A new webhook address has to be one this host can listen on. The running one is
	// already held by this process, which lets go of it when the source is swapped.
	if next.GitHubSourceEnabled() && next.GitHubWebhookListen != running.GitHubWebhookListen {
		ln, err := net.Listen("tcp", next.GitHubWebhookListen)
		if err != nil {
			return fmt.Errorf("the webhook address cannot be listened on: %w", err)
		}
		_ = ln.Close()
	}
	return nil
}

// Apply implements api.ConnectionApplier.
func (l *liveConnections) Apply(ctx context.Context) error {
	savedSlack, savedGitHub, err := connections.Load(ctx, l.store, l.vault)
	if err != nil {
		return err
	}
	next := connections.Overlay(l.env, savedSlack, savedGitHub)
	if err := next.Validate(); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	slackChanged := connections.SlackDiffers(next, l.running, nil)
	githubChanged := connections.GitHubDiffers(next, l.running, nil)
	if !slackChanged && !githubChanged {
		return nil
	}
	if l.ctx == nil || l.ctx.Err() != nil {
		return errors.New("the conductor is not running")
	}
	slack, gh, app, err := l.build(next)
	if err != nil {
		return err
	}
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
	l.running = next

	var dir api.SlackDirectory
	if slack != nil {
		dir = slack
	}
	l.svc.SetRunning(next, dir)
	l.logger.InfoContext(ctx, "connections: switched to the saved connections",
		"slack", slack != nil, "github_reviews", gh != nil, "github_app", app != nil,
		"slack_changed", slackChanged, "github_changed", githubChanged)
	return nil
}

// newGitHubSource is the pull-request review source: the App's webhooks, and the threads in
// Slack bound to a review.
func newGitHubSource(cfg config.Config, st *store.Store, slack *agentslack.Source, logger *slog.Logger) (*ghreview.Source, error) {
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
	return ghreview.NewSource(ghreview.SourceOptions{
		AppID:         cfg.GitHubAppID,
		PrivateKey:    key,
		WebhookSecret: cfg.GitHubWebhookSecret,
		Listen:        cfg.GitHubWebhookListen,
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
