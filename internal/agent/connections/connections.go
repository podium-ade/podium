// Package connections is the Slack, GitHub and Linear configuration an operator saves from
// Settings. The conductor reads it at start and again whenever a save changes it. The settings table holds what is not
// a credential. The credentials are global secrets in Podium's encrypted store, under
// spec.ConnectionSecretPrefix, which the conductor alone may read back. Slack still falls
// back to the environment when nothing is saved. The GitHub App does not: those
// environment variables are ignored. Nor does the Linear app, which has none:
// PODIUM_AGENT_LINEAR_API_KEY is the poller's key, not the app's.
package connections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
)

const (
	// SlackKey is the settings row for the Socket Mode pair.
	SlackKey = "connection.slack"
	// GitHubKey is the settings row for the GitHub App.
	GitHubKey = "connection.github"
	// LinearKey is the settings row for the Linear agent app.
	LinearKey = "connection.linear"

	// SourceSaved is a connection this database holds.
	SourceSaved = "saved"
	// SourceEnvironment is a connection that still comes from the process environment.
	SourceEnvironment = "environment"
)

// The secrets a saved connection's credentials are stored as.
const (
	GitHubPrivateKeySecret    = spec.ConnectionSecretPrefix + "github.private_key"
	GitHubWebhookSecretSecret = spec.ConnectionSecretPrefix + "github.webhook_secret"
	GitHubClientSecretSecret  = spec.ConnectionSecretPrefix + "github.client_secret"
	SlackAppTokenSecret       = spec.ConnectionSecretPrefix + "slack.app_token"
	SlackBotTokenSecret       = spec.ConnectionSecretPrefix + "slack.bot_token"
	LinearClientSecretSecret  = spec.ConnectionSecretPrefix + "linear.client_secret"
	LinearWebhookSecretSecret = spec.ConnectionSecretPrefix + "linear.webhook_secret"
	// LinearAccessTokenSecret is the app's access token and its expiry, kept so that every
	// conductor on this database signs in with one token rather than minting its own.
	LinearAccessTokenSecret = spec.ConnectionSecretPrefix + "linear.access_token"
)

// LinearWebhookSecretPrefix starts every Linear webhook signing secret.
const LinearWebhookSecretPrefix = "lin_wh_"

// Vault is Podium's secret store, as the conductor reaches it. *podium.Client satisfies it.
type Vault interface {
	SetSecret(ctx context.Context, name string, value []byte) (int32, error)
	DeleteSecret(ctx context.Context, name string) error
	ReadSecret(ctx context.Context, owner, name string) ([]byte, error)
}

// appIDRe is the numeric id on a GitHub App's settings page.
var appIDRe = regexp.MustCompile(`^[0-9]+$`)

// Slack is the saved Socket Mode pair. The tokens are credentials: they live in the vault
// and are never copied into an API response. A row from before the vault still carries
// them in its JSON, and Load moves them.
type Slack struct {
	AppToken string    `json:"app_token,omitempty"`
	BotToken string    `json:"bot_token,omitempty"`
	SetBy    string    `json:"set_by"`
	SetAt    time.Time `json:"set_at"`
	// Vaulted means the tokens are in the vault rather than in this row.
	Vaulted bool `json:"vaulted,omitempty"`
}

// GitHub is the saved App. PrivateKey, WebhookSecret and ClientSecret are credentials.
// ClientID and ClientSecret are the App's OAuth client, which lets people connect their own
// GitHub accounts. They are read when someone connects, so they need no restart.
type GitHub struct {
	AppID         string    `json:"app_id"`
	PrivateKey    string    `json:"private_key,omitempty"`
	WebhookSecret string    `json:"webhook_secret,omitempty"`
	WebhookListen string    `json:"webhook_listen"`
	ClientID      string    `json:"client_id,omitempty"`
	ClientSecret  string    `json:"client_secret,omitempty"`
	SetBy         string    `json:"set_by"`
	SetAt         time.Time `json:"set_at"`
	// Vaulted means the key and secrets are in the vault rather than in this row.
	Vaulted bool `json:"vaulted,omitempty"`
}

// Linear is the saved agent app. ClientSecret and WebhookSecret are credentials. The app
// signs in with Linear's client-credentials grant, so there is no install step to record:
// the client id and secret are the whole sign-in.
//
// AppUserID through OrganizationURLKey are what Settings learned from Linear when the app
// was saved: who the credentials sign in as and in which workspace. They are not settings.
type Linear struct {
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
	// WebhookListen is the host:port the webhook handler binds. It may be the GitHub App's
	// address, and then the two share one listener.
	WebhookListen string `json:"webhook_listen"`

	AppUserID          string `json:"app_user_id,omitempty"`
	AppUserName        string `json:"app_user_name,omitempty"`
	OrganizationID     string `json:"organization_id,omitempty"`
	OrganizationName   string `json:"organization_name,omitempty"`
	OrganizationURLKey string `json:"organization_url_key,omitempty"`

	SetBy string    `json:"set_by"`
	SetAt time.Time `json:"set_at"`
	// RevokedAt is when Linear said the app was revoked: de-authorized in the workspace, so
	// nothing it is sent can be answered. It is not a setting either, and the next save that
	// passes the check clears it, because MergeLinear never carries it over.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	// Vaulted means the secrets are in the vault rather than in this row.
	Vaulted bool `json:"vaulted,omitempty"`
}

// LinearToken is the app's access token as it is kept in the vault. ClientID names the
// app it was minted for, so a token outlives neither a change of app nor a clear.
type LinearToken struct {
	ClientID string `json:"client_id"`
	// AccessToken is SENSITIVE: never log it.
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// UserAuth reports whether people can connect their own GitHub accounts through this App.
func (g *GitHub) UserAuth() bool {
	return g != nil && g.ClientID != "" && g.ClientSecret != ""
}

// Load reads the saved connections with their credentials. A nil pointer means that
// connection has no row. For Slack the environment still applies. For GitHub and Linear
// the app is off.
//
// A row saved before the vault carries its credentials in clear. Load moves them into the
// vault and rewrites the row without them, so the first start after upgrading is the last
// time they sit in the conductor's database.
func Load(ctx context.Context, st *store.Store, v Vault) (*Slack, *GitHub, *Linear, error) {
	if st == nil || v == nil {
		return nil, nil, nil, errors.New("connections: no store or secret store")
	}
	slack, err := LoadSlack(ctx, st, v)
	if err != nil {
		return nil, nil, nil, err
	}
	gh, err := LoadGitHub(ctx, st, v)
	if err != nil {
		return nil, nil, nil, err
	}
	lin, err := LoadLinear(ctx, st, v)
	if err != nil {
		return nil, nil, nil, err
	}
	return slack, gh, lin, nil
}

// LoadSlack is Load for Slack alone.
func LoadSlack(ctx context.Context, st *store.Store, v Vault) (*Slack, error) {
	var row Slack
	err := st.GetSetting(ctx, SlackKey, &row)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !row.Vaulted {
		if err := SaveSlack(ctx, st, v, row, nil); err != nil {
			return nil, fmt.Errorf("connections: moving the saved Slack tokens into the secret store: %w", err)
		}
		return &row, nil
	}
	if row.AppToken, err = read(ctx, v, SlackAppTokenSecret); err != nil {
		return nil, err
	}
	if row.BotToken, err = read(ctx, v, SlackBotTokenSecret); err != nil {
		return nil, err
	}
	return &row, nil
}

// LoadGitHub is Load for the GitHub App alone.
func LoadGitHub(ctx context.Context, st *store.Store, v Vault) (*GitHub, error) {
	var row GitHub
	err := st.GetSetting(ctx, GitHubKey, &row)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !row.Vaulted {
		if err := SaveGitHub(ctx, st, v, row, nil); err != nil {
			return nil, fmt.Errorf("connections: moving the saved GitHub App credentials into the secret store: %w", err)
		}
		return &row, nil
	}
	if row.PrivateKey, err = read(ctx, v, GitHubPrivateKeySecret); err != nil {
		return nil, err
	}
	if row.WebhookSecret, err = read(ctx, v, GitHubWebhookSecretSecret); err != nil {
		return nil, err
	}
	if row.ClientSecret, err = read(ctx, v, GitHubClientSecretSecret); err != nil {
		return nil, err
	}
	return &row, nil
}

// LoadLinear is Load for the Linear app alone. The Linear app was always saved to the
// vault, so a row in clear is not expected; it is moved the same way if one turns up.
func LoadLinear(ctx context.Context, st *store.Store, v Vault) (*Linear, error) {
	var row Linear
	err := st.GetSetting(ctx, LinearKey, &row)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !row.Vaulted {
		if err := SaveLinear(ctx, st, v, row, nil); err != nil {
			return nil, fmt.Errorf("connections: moving the saved Linear app's secrets into the secret store: %w", err)
		}
		return &row, nil
	}
	if row.ClientSecret, err = read(ctx, v, LinearClientSecretSecret); err != nil {
		return nil, err
	}
	if row.WebhookSecret, err = read(ctx, v, LinearWebhookSecretSecret); err != nil {
		return nil, err
	}
	return &row, nil
}

// SaveSlack writes the tokens that changed since prev to the vault, then the row without
// them. prev nil writes every token.
func SaveSlack(ctx context.Context, st *store.Store, v Vault, row Slack, prev *Slack) error {
	if prev == nil {
		prev = &Slack{}
	}
	if err := put(ctx, v, SlackAppTokenSecret, row.AppToken, prev.AppToken); err != nil {
		return err
	}
	if err := put(ctx, v, SlackBotTokenSecret, row.BotToken, prev.BotToken); err != nil {
		return err
	}
	row.AppToken, row.BotToken, row.Vaulted = "", "", true
	return st.PutSetting(ctx, SlackKey, row)
}

// SaveGitHub writes the credentials that changed since prev to the vault, then the row
// without them. prev nil writes every credential.
func SaveGitHub(ctx context.Context, st *store.Store, v Vault, row GitHub, prev *GitHub) error {
	if prev == nil {
		prev = &GitHub{}
	}
	if err := put(ctx, v, GitHubPrivateKeySecret, row.PrivateKey, prev.PrivateKey); err != nil {
		return err
	}
	if err := put(ctx, v, GitHubWebhookSecretSecret, row.WebhookSecret, prev.WebhookSecret); err != nil {
		return err
	}
	if err := put(ctx, v, GitHubClientSecretSecret, row.ClientSecret, prev.ClientSecret); err != nil {
		return err
	}
	row.PrivateKey, row.WebhookSecret, row.ClientSecret, row.Vaulted = "", "", "", true
	return st.PutSetting(ctx, GitHubKey, row)
}

// SaveLinear writes the secrets that changed since prev to the vault, then the row without
// them. prev nil writes every secret. A new client id or secret also drops the saved
// access token: it was minted for the app as it was.
func SaveLinear(ctx context.Context, st *store.Store, v Vault, row Linear, prev *Linear) error {
	if prev == nil {
		prev = &Linear{}
	}
	if row.ClientID != prev.ClientID || row.ClientSecret != prev.ClientSecret {
		if err := drop(ctx, v, LinearAccessTokenSecret); err != nil {
			return err
		}
	}
	if err := put(ctx, v, LinearClientSecretSecret, row.ClientSecret, prev.ClientSecret); err != nil {
		return err
	}
	if err := put(ctx, v, LinearWebhookSecretSecret, row.WebhookSecret, prev.WebhookSecret); err != nil {
		return err
	}
	row.ClientSecret, row.WebhookSecret, row.Vaulted = "", "", true
	return st.PutSetting(ctx, LinearKey, row)
}

// LoadLinearToken is the access token saved for the app with clientID. A zero LinearToken
// means there is none, or the one saved belongs to another app. It is the Load hook of
// linear.TokenSource.
func LoadLinearToken(ctx context.Context, v Vault, clientID string) (LinearToken, error) {
	raw, err := read(ctx, v, LinearAccessTokenSecret)
	if err != nil || raw == "" {
		return LinearToken{}, err
	}
	var tok LinearToken
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		// Not worth failing over: the caller mints a new token and saves it over this one.
		return LinearToken{}, fmt.Errorf("connections: reading %s: %w", LinearAccessTokenSecret, err)
	}
	if tok.ClientID != clientID {
		return LinearToken{}, nil
	}
	return tok, nil
}

// StoreLinearToken saves the app's access token for the next conductor to find. It is the
// Store hook of linear.TokenSource. The token and its expiry are one secret, so a reader
// never pairs one token with another's expiry.
func StoreLinearToken(ctx context.Context, v Vault, tok LinearToken) error {
	raw, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("connections: encoding %s: %w", LinearAccessTokenSecret, err)
	}
	if _, err := v.SetSecret(ctx, LinearAccessTokenSecret, raw); err != nil {
		return fmt.Errorf("connections: storing %s: %w", LinearAccessTokenSecret, err)
	}
	return nil
}

// ClearSlack removes the saved pair and its tokens.
func ClearSlack(ctx context.Context, st *store.Store, v Vault) error {
	for _, name := range []string{SlackAppTokenSecret, SlackBotTokenSecret} {
		if err := drop(ctx, v, name); err != nil {
			return err
		}
	}
	return st.DeleteSetting(ctx, SlackKey)
}

// ClearGitHub removes the saved App and its credentials.
func ClearGitHub(ctx context.Context, st *store.Store, v Vault) error {
	for _, name := range []string{GitHubPrivateKeySecret, GitHubWebhookSecretSecret, GitHubClientSecretSecret} {
		if err := drop(ctx, v, name); err != nil {
			return err
		}
	}
	return st.DeleteSetting(ctx, GitHubKey)
}

// MarkLinearRevoked records on the saved app that Linear revoked it at at. It touches the row
// and nothing in the vault. A row that is gone, or that is now another app, is left alone:
// the revoke was for an app that is no longer saved.
func MarkLinearRevoked(ctx context.Context, st *store.Store, clientID string, at time.Time) error {
	var row Linear
	err := st.GetSetting(ctx, LinearKey, &row)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.ClientID != clientID || !row.Vaulted {
		return nil
	}
	at = at.UTC()
	row.RevokedAt = &at
	return st.PutSetting(ctx, LinearKey, row)
}

// ClearLinear removes the saved app, its secrets and its access token.
func ClearLinear(ctx context.Context, st *store.Store, v Vault) error {
	for _, name := range []string{LinearClientSecretSecret, LinearWebhookSecretSecret, LinearAccessTokenSecret} {
		if err := drop(ctx, v, name); err != nil {
			return err
		}
	}
	return st.DeleteSetting(ctx, LinearKey)
}

// read is one credential, or "" when it was never stored.
func read(ctx context.Context, v Vault, name string) (string, error) {
	value, err := v.ReadSecret(ctx, "", name)
	if connect.CodeOf(err) == connect.CodeNotFound {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("connections: reading %s: %w", name, err)
	}
	return string(value), nil
}

// put stores value when it changed, and removes the secret when it was cleared.
func put(ctx context.Context, v Vault, name, value, prev string) error {
	switch value {
	case prev:
		return nil
	case "":
		return drop(ctx, v, name)
	}
	if _, err := v.SetSecret(ctx, name, []byte(value)); err != nil {
		return fmt.Errorf("connections: storing %s: %w", name, err)
	}
	return nil
}

func drop(ctx context.Context, v Vault, name string) error {
	err := v.DeleteSecret(ctx, name)
	if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("connections: removing %s: %w", name, err)
	}
	return nil
}

// Overlay returns cfg with a saved connection written over it. A nil Slack pointer
// leaves the environment pair. A nil GitHub or Linear pointer leaves that app unset: it is
// not read from the environment. A saved GitHub App clears the key file, because the PEM
// in the row is the key and a file path beside it would make Validate refuse the pair.
func Overlay(cfg config.Config, slack *Slack, gh *GitHub, lin *Linear) config.Config {
	if slack != nil {
		cfg.SlackAppToken = slack.AppToken
		cfg.SlackBotToken = slack.BotToken
	}
	if gh != nil {
		cfg.GitHubAppID = gh.AppID
		cfg.GitHubAppKey = gh.PrivateKey
		cfg.GitHubAppKeyFile = ""
		cfg.GitHubWebhookSecret = gh.WebhookSecret
		cfg.GitHubWebhookListen = gh.WebhookListen
	}
	if lin != nil {
		cfg.LinearClientID = lin.ClientID
		cfg.LinearClientSecret = lin.ClientSecret
		cfg.LinearWebhookSecret = lin.WebhookSecret
		cfg.LinearWebhookListen = lin.WebhookListen
	}
	return cfg
}

// MergeSlack combines a paste with the saved pair. A blank field keeps the saved
// token so one of them can be rotated. The first save has to carry both.
func MergeSlack(existing *Slack, appToken, botToken string) (Slack, error) {
	appToken = strings.TrimSpace(appToken)
	botToken = strings.TrimSpace(botToken)
	if appToken == "" && existing != nil {
		appToken = existing.AppToken
	}
	if botToken == "" && existing != nil {
		botToken = existing.BotToken
	}
	switch {
	case appToken == "" && botToken == "":
		return Slack{}, errors.New("the Slack app-level token and bot token are both required")
	case appToken == "":
		return Slack{}, errors.New("the Slack app-level token is required (it starts with xapp-)")
	case botToken == "":
		return Slack{}, errors.New("the Slack bot token is required (it starts with xoxb-)")
	case !strings.HasPrefix(appToken, "xapp-"):
		return Slack{}, errors.New("the Slack app-level token has to start with xapp-")
	case !strings.HasPrefix(botToken, "xoxb-"):
		return Slack{}, errors.New("the Slack bot token has to start with xoxb-")
	}
	return Slack{AppToken: appToken, BotToken: botToken}, nil
}

// MergeGitHub combines a paste with the saved App. A blank private key, webhook secret or
// client secret keeps the saved one. Clearing the listen address turns reviews off, and
// clearing the client id turns account connection off.
func MergeGitHub(
	existing *GitHub, appID, privateKey, webhookSecret, webhookListen, clientID, clientSecret string,
) (GitHub, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" && existing != nil {
		appID = existing.AppID
	}
	if appID == "" {
		return GitHub{}, errors.New("the GitHub App id is required")
	}
	if !appIDRe.MatchString(appID) {
		return GitHub{}, errors.New("the GitHub App id is the number on the app's settings page")
	}

	privateKey = strings.TrimSpace(privateKey)
	if privateKey == "" && existing != nil {
		privateKey = existing.PrivateKey
	}
	if privateKey == "" {
		return GitHub{}, errors.New("the GitHub App private key is required")
	}

	webhookListen = strings.TrimSpace(webhookListen)
	webhookSecret = strings.TrimSpace(webhookSecret)
	if webhookListen == "" {
		webhookSecret = ""
	} else if webhookSecret == "" && existing != nil {
		webhookSecret = existing.WebhookSecret
	}
	if webhookListen != "" {
		if _, _, err := net.SplitHostPort(webhookListen); err != nil {
			return GitHub{}, errors.New("the webhook listen address must be host:port, such as 0.0.0.0:8091")
		}
		if webhookSecret == "" {
			return GitHub{}, errors.New("the webhook secret is required when a listen address is set")
		}
	}

	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" {
		clientSecret = ""
	} else if clientSecret == "" && existing != nil && existing.ClientID != "" {
		clientSecret = existing.ClientSecret
	}
	if clientID != "" && clientSecret == "" {
		return GitHub{}, errors.New("the client secret is required when a client id is set")
	}
	return GitHub{
		AppID:         appID,
		PrivateKey:    privateKey,
		WebhookSecret: webhookSecret,
		WebhookListen: webhookListen,
		ClientID:      clientID,
		ClientSecret:  clientSecret,
	}, nil
}

// MergeLinear combines a paste with the saved app. A blank client id keeps the saved one,
// and a blank secret keeps the saved one while the client id is the same: a new client id
// is a different app, and both of its secrets have to come with it. The first save needs
// all four fields.
//
// What Settings learned about the app is kept while the client id is the same, so a save
// that only moves the listen address still says what it is connected as. A new client id
// starts empty, and the check fills it in.
func MergeLinear(existing *Linear, clientID, clientSecret, webhookSecret, webhookListen string) (Linear, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" && existing != nil {
		clientID = existing.ClientID
	}
	if clientID == "" {
		return Linear{}, errors.New("the Linear client id is required: it is on the OAuth " +
			"application's page under Settings → API")
	}
	same := existing != nil && existing.ClientID == clientID

	clientSecret = strings.TrimSpace(clientSecret)
	if clientSecret == "" && same {
		clientSecret = existing.ClientSecret
	}
	if clientSecret == "" {
		if existing != nil && !same {
			return Linear{}, errors.New("a new Linear client id needs its client secret")
		}
		return Linear{}, errors.New("the Linear client secret is required")
	}

	webhookSecret = strings.TrimSpace(webhookSecret)
	if webhookSecret == "" && same {
		webhookSecret = existing.WebhookSecret
	}
	switch {
	case webhookSecret == "":
		return Linear{}, errors.New("the Linear webhook signing secret is required (it starts with " +
			LinearWebhookSecretPrefix + ")")
	case !strings.HasPrefix(webhookSecret, LinearWebhookSecretPrefix):
		return Linear{}, errors.New("the Linear webhook signing secret has to start with " +
			LinearWebhookSecretPrefix + ": it is on the OAuth application's page, beside the webhook URL")
	}

	webhookListen = strings.TrimSpace(webhookListen)
	if webhookListen == "" && existing != nil {
		webhookListen = existing.WebhookListen
	}
	if webhookListen == "" {
		return Linear{}, errors.New("the webhook listen address is required: the host:port Linear's " +
			"webhooks reach, such as 0.0.0.0:8091")
	}
	if _, _, err := net.SplitHostPort(webhookListen); err != nil {
		return Linear{}, errors.New("the webhook listen address must be host:port, such as 0.0.0.0:8091")
	}

	out := Linear{
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		WebhookSecret: webhookSecret,
		WebhookListen: webhookListen,
	}
	if same {
		out.AppUserID = existing.AppUserID
		out.AppUserName = existing.AppUserName
		out.OrganizationID = existing.OrganizationID
		out.OrganizationName = existing.OrganizationName
		out.OrganizationURLKey = existing.OrganizationURLKey
	}
	return out, nil
}

// Hint is the last four characters of a secret, or "" when there is nothing to show.
// A shorter value is withheld: four characters of a three-character secret is the secret.
func Hint(secret string) string {
	secret = strings.TrimSpace(secret)
	if utf8.RuneCountInString(secret) <= 4 {
		return ""
	}
	r := []rune(secret)
	return string(r[len(r)-4:])
}

// SlackDiffers reports whether the pair that should be in force differs from the pair
// this process started with.
func SlackDiffers(env, running config.Config, saved *Slack) bool {
	wantApp, wantBot := env.SlackAppToken, env.SlackBotToken
	if saved != nil {
		wantApp, wantBot = saved.AppToken, saved.BotToken
	}
	return wantApp != running.SlackAppToken || wantBot != running.SlackBotToken
}

// GitHubDiffers reports whether the App that should be in force differs from the App
// this process started with. A key that cannot be read counts as different: the
// alternative is to claim the running process already has a key it cannot open.
func GitHubDiffers(env, running config.Config, saved *GitHub) bool {
	want := env
	if saved != nil {
		want = Overlay(config.Config{}, nil, saved, nil)
	}
	if want.GitHubAppID != running.GitHubAppID ||
		want.GitHubWebhookSecret != running.GitHubWebhookSecret ||
		want.GitHubWebhookListen != running.GitHubWebhookListen ||
		want.GitHubAppKeyFile != running.GitHubAppKeyFile {
		return true
	}
	wantKey, wantErr := want.GitHubAppPrivateKey()
	runKey, runErr := running.GitHubAppPrivateKey()
	if wantErr != nil || runErr != nil {
		// Both unreadable is the same configuration, not a pending restart.
		return wantErr == nil || runErr == nil
	}
	return string(wantKey) != string(runKey)
}

// LinearDiffers reports whether the Linear app that should be in force differs from the
// one this process holds. Like the GitHub App, it is never read from the environment, so
// env only matters with nothing saved, where it is the configuration to compare.
func LinearDiffers(env, running config.Config, saved *Linear) bool {
	want := env
	if saved != nil {
		want = Overlay(config.Config{}, nil, nil, saved)
	}
	return want.LinearClientID != running.LinearClientID ||
		want.LinearClientSecret != running.LinearClientSecret ||
		want.LinearWebhookSecret != running.LinearWebhookSecret ||
		want.LinearWebhookListen != running.LinearWebhookListen
}
