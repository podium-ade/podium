// Package linear is the bot's Linear identity: assigned issues polled into inbound events,
// and the five ways the conductor talks back. It implements conductor.Source and knows
// nothing about sessions, tasks or briefs.
//
// It DIALS OUT and never listens. Linear's own documentation prefers webhooks, and a
// webhook would be better — AppUserNotification carries an `issueAssignedToYou` action,
// which is the exact predicate this file has to reconstruct. It needs an OAuth `actor=app`
// application and an inbound listener, and Podium had neither when this was written. So:
// polling. The agent app is being built now (docs/linear-agent-plan.md): the client can
// already sign in as one with a TokenSource, and the webhook source that replaces the
// poller comes after it.
package linear

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds one GraphQL call. A poll page with its comments is the slowest
// thing here and Linear answers it in well under a second.
const DefaultTimeout = 30 * time.Second

// maxErrorBody is how much of a failure response is quoted in an error. Linear answers with
// a JSON envelope, which is short; an egress proxy's login page is not.
const maxErrorBody = 4 << 10

// Error codes Linear reports in extensions.code. There are more; these are the two the
// caller branches on.
const (
	// CodeRateLimited arrives with HTTP 400, not 429. Linear's leaky bucket rejects rather
	// than queues, and it does it with the status you would use for a malformed query.
	CodeRateLimited = "RATELIMITED"
	// CodeAuthentication is a wrong or missing API key. It arrives with HTTP 401.
	CodeAuthentication = "AUTHENTICATION_ERROR"
)

// GraphQLError is a Linear refusal: the first message from the errors array, its extension
// code, and the HTTP status it came with. GraphQL puts application errors in a 200 as often
// as not, so the status alone says very little.
type GraphQLError struct {
	// Op is the operation name, so a log line says which query failed.
	Op string
	// Message is the first error's message. Linear's messages are written for a human.
	Message string
	// Code is extensions.code, empty when Linear did not send one.
	Code string
	// Status is the HTTP status.
	Status int
}

func (e *GraphQLError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("linear: %s: %s (%s, HTTP %d)", e.Op, e.Message, e.Code, e.Status)
	}
	return fmt.Sprintf("linear: %s: %s (HTTP %d)", e.Op, e.Message, e.Status)
}

// RateLimited reports whether Linear throttled the request. The code is the reliable half:
// throttling arrives as HTTP 400, and 429 is checked too because the documentation of the
// two disagrees and both are cheap to accept.
func (e *GraphQLError) RateLimited() bool {
	return e.Code == CodeRateLimited || e.Status == http.StatusTooManyRequests
}

// Unauthorized reports whether the API key or access token is wrong or missing. It is
// separated because it is the one failure an operator fixes by editing .env rather than by
// reading a log, and the one an app's client fixes by minting a new token.
func (e *GraphQLError) Unauthorized() bool {
	return e.Code == CodeAuthentication || e.Status == http.StatusUnauthorized
}

// isRateLimited reports whether err is a Linear throttle anywhere in its chain.
func isRateLimited(err error) bool {
	var gqlErr *GraphQLError
	return errors.As(err, &gqlErr) && gqlErr.RateLimited()
}

// Client is the GraphQL client. It is hand-written rather than @linear/sdk's generated Go
// equivalent for the same reason internal/agent/memory is: six operations, and the two
// places Linear's schema surprises you (a bare Authorization header, a nullable comment
// author) are worth a comment each rather than a generated type.
type Client struct {
	endpoint string
	// Exactly one of apiKey and tokens is set: the poller's personal key, or the agent
	// app's OAuth tokens.
	apiKey string
	tokens *TokenSource
	http   *http.Client
}

// ClientOptions is what NewClient needs.
type ClientOptions struct {
	// Endpoint is the GraphQL endpoint. Required.
	Endpoint string
	// APIKey is a PERSONAL API key belonging to the bot user. SENSITIVE. Set this or
	// Tokens, not both.
	APIKey string
	// Tokens signs requests in as an OAuth app instead, with a bearer token it mints.
	Tokens *TokenSource
	// HTTPClient lets a test point at an httptest server. Nil means one with DefaultTimeout.
	HTTPClient *http.Client
}

// NewClient validates the options and returns a client. It makes no request: the source's
// Run is what proves the key works, and it does it where the failure can be fatal.
func NewClient(opts ClientOptions) (*Client, error) {
	switch {
	case opts.Endpoint == "":
		return nil, errors.New("linear: a GraphQL endpoint is required")
	case opts.APIKey == "" && opts.Tokens == nil:
		return nil, errors.New("linear: an API key is required")
	case opts.APIKey != "" && opts.Tokens != nil:
		return nil, errors.New("linear: an API key and a token source are both set: a client " +
			"signs in one way, so there is no question whose actions it takes")
	}
	u, err := url.Parse(opts.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("linear: parse %q: %w", opts.Endpoint, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("linear: %q is not an absolute http:// or https:// URL", opts.Endpoint)
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{endpoint: opts.Endpoint, apiKey: opts.APIKey, tokens: opts.Tokens, http: client}, nil
}

// gqlRequest is the wire request. OperationName is sent so a Linear-side log and this
// client's errors name the same thing.
type gqlRequest struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables,omitempty"`
	OperationName string         `json:"operationName,omitempty"`
}

type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

// do runs one operation. A response with a non-empty errors array is a *GraphQLError
// carrying the first message, whatever the HTTP status was.
//
// With a token source, a refused token is dropped and the operation sent once more with a
// new one. A 30-day token can be revoked early, and the grant has no refresh token: a new
// token is the only remedy, and it needs nobody. Resending a mutation is safe here because
// a request Linear did not authenticate did nothing.
func (c *Client) do(ctx context.Context, op, query string, vars map[string]any, out any) error {
	raw, err := json.Marshal(gqlRequest{Query: query, Variables: vars, OperationName: op})
	if err != nil {
		return fmt.Errorf("linear: encode %s: %w", op, err)
	}
	if c.tokens == nil {
		// A PERSONAL API key goes in Authorization RAW, with no "Bearer " prefix. Bearer
		// is for OAuth access tokens, and Linear's own SDK only adds it when one is
		// present. Getting this wrong is a 401 that reads like a wrong key.
		return c.send(ctx, op, raw, c.apiKey, out)
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}
	err = c.send(ctx, op, raw, "Bearer "+token, out)
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) || !gqlErr.Unauthorized() {
		return err
	}
	c.tokens.Invalidate(token)
	if token, err = c.tokens.Token(ctx); err != nil {
		return err
	}
	return c.send(ctx, op, raw, "Bearer "+token, out)
}

// send is one POST of an encoded operation with the given Authorization header.
func (c *Client) send(ctx context.Context, op string, raw []byte, authorization string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("linear: build %s: %w", op, err)
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %s: %w", op, err)
	}
	defer func() { _ = res.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("linear: read %s response: %w", op, err)
	}

	var envelope gqlResponse
	if jsonErr := json.Unmarshal(body, &envelope); jsonErr != nil {
		// Not JSON at all: a proxy, a gateway, a maintenance page. The status is the only
		// thing worth reporting, plus a bounded quote so an operator can recognise it.
		return &GraphQLError{Op: op, Status: res.StatusCode, Message: quote(body)}
	}
	if len(envelope.Errors) > 0 {
		first := envelope.Errors[0]
		return &GraphQLError{
			Op:      op,
			Message: first.Message,
			Code:    first.Extensions.Code,
			Status:  res.StatusCode,
		}
	}
	if res.StatusCode/100 != 2 {
		return &GraphQLError{Op: op, Status: res.StatusCode, Message: quote(body)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("linear: decode %s: %w", op, err)
	}
	return nil
}

func quote(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "empty response"
	}
	if len(text) > maxErrorBody {
		return text[:maxErrorBody] + "…"
	}
	return text
}

// newUUID is a version 4 UUID from crypto/rand. Linear's commentCreate accepts a
// client-supplied id, which is what makes a create idempotent on its side; a dependency for
// twelve lines was not worth it.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("linear: generate a comment id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
