//go:build integration

package nodes_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
)

// Under the dev transport there is no Tailscale device, so a node enrolls unbound and its stream
// is never checked against one. This is the no-regression half of the binding rule.
func TestDevEnrollmentLeavesTheNodeUnbound(t *testing.T) {
	h := newHarness(t)
	node := enrollNode(t, h, "worker-dev", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	node.open(ctx)
	h.awaitNodeStatus(node.id, podiumv1.NodeStatus_NODE_STATUS_ONLINE, 10*time.Second)

	require.Empty(t, h.node(node.id).GetTsStableId(),
		"a dev-transport node has no Tailscale device to bind to")
}

// RekeyNode is the admin escape hatch for a rebuilt worker: the node keeps its identity and the
// next connection binds it to whatever device it arrives from.
func TestRekeyNodeClearsTheBinding(t *testing.T) {
	h := newHarness(t)
	node := enrollNode(t, h, "worker-rekey", nil)
	ctx := context.Background()

	res, err := h.admin.RekeyNode(ctx, connect.NewRequest(&podiumv1.RekeyNodeRequest{NodeId: node.id}))
	require.NoError(t, err)
	require.Equal(t, node.id, res.Msg.GetNode().GetId())
	require.Empty(t, res.Msg.GetNode().GetTsStableId())

	_, err = h.admin.RekeyNode(ctx, connect.NewRequest(&podiumv1.RekeyNodeRequest{NodeId: "node_nope"}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = h.admin.RekeyNode(ctx, connect.NewRequest(&podiumv1.RekeyNodeRequest{}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// Enroll is rate limited per remote IP, before the token is even looked at: an unlimited Enroll
// is an unlimited guessing run at a 32-byte token.
func TestEnrollIsRateLimitedPerAddress(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	var lastErr error
	for i := range 8 {
		_, err := h.nodes.Enroll(ctx, connect.NewRequest(&podiumv1.EnrollRequest{
			Token:    "definitely-not-a-real-token",
			Hostname: "attacker",
		}))
		require.Error(t, err, "attempt %d used a bogus token and must fail", i+1)
		lastErr = err
	}
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(lastErr),
		"the budget should be spent well before the eighth attempt")
}

// WhoAmI is what the web UI probes before deciding whether to ask for a token. Under the dev
// transport it answers "dev_token", which is the UI's signal that a prompt is still required.
func TestWhoAmIUnderTheDevTransport(t *testing.T) {
	h := newHarness(t)
	identity := podiumv1connect.NewIdentityServiceClient(h.http, h.url)

	res, err := identity.WhoAmI(context.Background(), connect.NewRequest(&podiumv1.WhoAmIRequest{}))
	require.NoError(t, err)
	require.Equal(t, "dev", res.Msg.GetLogin())
	require.Equal(t, podiumv1.IdentityKind_IDENTITY_KIND_DEV_TOKEN, res.Msg.GetKind())
	require.Empty(t, res.Msg.GetTags())
}

// Without a token WhoAmI is 401, and that is exactly how a browser learns the server is not on a
// tailnet and it must ask the operator for the dev token.
func TestWhoAmIWithoutATokenIsUnauthenticated(t *testing.T) {
	h := newHarness(t)
	anon := podiumv1connect.NewIdentityServiceClient(h2cClient(""), h.url)

	_, err := anon.WhoAmI(context.Background(), connect.NewRequest(&podiumv1.WhoAmIRequest{}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// The enrollment token is still what proves *which* node this is, tag or no tag.
func TestEnrollStillRefusesASpentToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	tok, err := h.admin.CreateEnrollmentToken(ctx, connect.NewRequest(&podiumv1.CreateEnrollmentTokenRequest{
		Ttl: durationpb.New(time.Hour),
	}))
	require.NoError(t, err)

	_, err = h.nodes.Enroll(ctx, connect.NewRequest(&podiumv1.EnrollRequest{
		Token: tok.Msg.GetToken(), Hostname: "first",
	}))
	require.NoError(t, err)

	_, err = h.nodes.Enroll(ctx, connect.NewRequest(&podiumv1.EnrollRequest{
		Token: tok.Msg.GetToken(), Hostname: "second",
	}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.ErrorContains(t, err, "already used")
}
