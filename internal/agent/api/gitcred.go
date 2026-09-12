package api

// GitCredentialService: the one thing a TASK may ask the conductor for.
//
// Every other surface in this package is reachable only from the conductor's own host —
// AgentService through podium-server's proxy, TurnService not at all. This one is
// deliberately exposed to task containers, because the caller is a git credential helper
// inside one. That makes its authentication the whole of its security, so it is worth
// stating what that is:
//
//   - The credential is a capability the conductor SIGNED, naming one turn and the
//     repositories that turn's playbook listed. It is not an operator bearer and it cannot
//     be used against any other surface.
//   - The call takes no arguments. A turn cannot widen its own scope by asking differently,
//     because there is nothing to ask.
//   - The capability stops working when its turn does, which the conductor checks against
//     the database on every call.
//
// See internal/agent/conductor/gitcred.go for the other half, and docs/security.md for what
// exposing this changed.

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/conductor"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// GitMinter is the conductor, as this service needs it. An interface so the handler can be
// tested without a GitHub App, a database or a turn loop behind it.
type GitMinter interface {
	MintGitToken(ctx context.Context, capability string) (conductor.GitCredential, error)
}

// GitCredentialService implements podium.agent.v1.GitCredentialService.
type GitCredentialService struct {
	minter GitMinter
	logger *slog.Logger
}

// NewGitCredentialService returns the handler. A nil minter is a conductor with no GitHub
// App: every call then answers FailedPrecondition, which is the truth rather than a crash.
func NewGitCredentialService(minter GitMinter, logger *slog.Logger) *GitCredentialService {
	if logger == nil {
		logger = slog.Default()
	}
	return &GitCredentialService{minter: minter, logger: logger}
}

// MintToken issues a GitHub installation token for the calling turn.
func (s *GitCredentialService) MintToken(
	ctx context.Context, req *connect.Request[agentv1.MintTokenRequest],
) (*connect.Response[agentv1.MintTokenResponse], error) {
	if s.minter == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no GitHub App configured"))
	}
	capability := strings.TrimSpace(req.Header().Get(TurnTokenHeader))
	if capability == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated,
			errors.New(TurnTokenHeader+" is required"))
	}
	cred, err := s.minter.MintGitToken(ctx, capability)
	switch {
	case errors.Is(err, conductor.ErrBadCapability):
		// One answer for malformed, forged and expired alike. A caller that could tell
		// them apart would learn which of its guesses was closest, and the only honest
		// caller here already knows which it holds.
		return nil, connect.NewError(connect.CodePermissionDenied, conductor.ErrBadCapability)
	case errors.Is(err, conductor.ErrNoGitApp):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		// GitHub said no, or could not be reached. The turn can act on that — it is the
		// difference between "retry the push" and "tell a human" — so the message goes
		// back rather than only into the log. It never carries a token: the github package
		// quotes GitHub's own message and nothing of what it was minting.
		s.logger.ErrorContext(ctx, "minting a github token for a turn failed", "error", err)
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&agentv1.MintTokenResponse{
		Token:       cred.Token,
		Username:    cred.Username,
		ExpiresAt:   timestamppb.New(cred.ExpiresAt),
		AuthorName:  cred.Identity.Name,
		AuthorEmail: cred.Identity.Email,
	}), nil
}
