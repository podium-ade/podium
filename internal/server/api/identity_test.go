package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/transport"
)

func TestWhoAmI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   transport.Identity
		want *podiumv1.WhoAmIResponse
	}{
		{
			name: "a tailnet user",
			id: transport.Identity{
				Kind:        transport.KindUser,
				Login:       "alvaro@affiniti.com",
				DisplayName: "Alvaro Ibarguen",
			},
			want: &podiumv1.WhoAmIResponse{
				Login:       "alvaro@affiniti.com",
				DisplayName: "Alvaro Ibarguen",
				Kind:        podiumv1.IdentityKind_IDENTITY_KIND_USER,
			},
		},
		{
			name: "a node, with its tags",
			id: transport.Identity{
				Kind:     transport.KindNode,
				Login:    "podiumbot1.taila79bf6.ts.net",
				NodeTags: []string{"tag:podium-node"},
			},
			want: &podiumv1.WhoAmIResponse{
				Login: "podiumbot1.taila79bf6.ts.net",
				Kind:  podiumv1.IdentityKind_IDENTITY_KIND_NODE,
				Tags:  []string{"tag:podium-node"},
			},
		},
		{
			name: "the local transport's shared token",
			id:   transport.Identity{Kind: transport.KindLocalToken, Login: "local"},
			want: &podiumv1.WhoAmIResponse{
				Login: "local",
				Kind:  podiumv1.IdentityKind_IDENTITY_KIND_LOCAL_TOKEN,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := transport.NewContext(t.Context(), tc.id)
			res, err := NewIdentityService(false).WhoAmI(ctx, connect.NewRequest(&podiumv1.WhoAmIRequest{}))
			require.NoError(t, err)
			require.Equal(t, tc.want.GetLogin(), res.Msg.GetLogin())
			require.Equal(t, tc.want.GetDisplayName(), res.Msg.GetDisplayName())
			require.Equal(t, tc.want.GetKind(), res.Msg.GetKind())
			require.Equal(t, tc.want.GetTags(), res.Msg.GetTags())
		})
	}
}

func TestWhoAmIWithoutTheMiddlewareIsUnauthenticated(t *testing.T) {
	t.Parallel()
	_, err := NewIdentityService(false).WhoAmI(context.Background(), connect.NewRequest(&podiumv1.WhoAmIRequest{}))
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
