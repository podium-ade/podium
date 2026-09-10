package nodes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/transport"
)

// The node key in identity.json is a bearer credential: whoever holds the file owns the node.
// Pinning a node to the Tailscale device it enrolled from is what makes a copied identity.json
// useless anywhere else, so this table is the security property, not a detail.
func TestDecideBinding(t *testing.T) {
	t.Parallel()
	const enrolledFrom = "nPODIUMBOT1CNTRL"
	const someoneElse = "nATTACKERCNTRL"

	tests := []struct {
		name      string
		stored    string
		presented string
		want      bindingDecision
	}{
		{
			name: "local transport: no devices anywhere, nothing to do",
			want: bindingNoop,
		},
		{
			name:   "local transport reconnecting to a node that was bound over the tailnet",
			stored: enrolledFrom,
			want:   bindingNoop,
		},
		{
			name:      "first tailnet connection of an unbound node binds it",
			presented: enrolledFrom,
			want:      bindingBind,
		},
		{
			name:      "the same device reconnecting is fine",
			stored:    enrolledFrom,
			presented: enrolledFrom,
			want:      bindingNoop,
		},
		{
			name:      "a stolen identity.json on another device is rejected",
			stored:    enrolledFrom,
			presented: someoneElse,
			want:      bindingReject,
		},
		{
			name:      "after a rekey the next device binds",
			stored:    "",
			presented: someoneElse,
			want:      bindingBind,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, decideBinding(tc.stored, tc.presented))
		})
	}
}

func TestMayEnroll(t *testing.T) {
	t.Parallel()
	s := &Service{}

	require.True(t, s.mayEnroll(transport.Identity{Kind: transport.KindNode}),
		"a tagged device is what enrollment is for")
	require.True(t, s.mayEnroll(transport.Identity{Kind: transport.KindLocalToken}),
		"the local transport has no device tags at all")
	require.False(t, s.mayEnroll(transport.Identity{Kind: transport.KindUser}),
		"an untagged tailnet device must not enroll by default")
	require.False(t, s.mayEnroll(transport.Identity{}))

	s.SetAllowUntaggedNodes(true)
	require.True(t, s.mayEnroll(transport.Identity{Kind: transport.KindUser}),
		"PODIUM_TS_ALLOW_UNTAGGED_NODES is the documented escape hatch")
	require.False(t, s.mayEnroll(transport.Identity{}),
		"the escape hatch does not admit an identity the transport never produced")
}

func TestTailnetNodeTagHintNamesWhatTheDeviceCarries(t *testing.T) {
	t.Parallel()
	require.Equal(t, "tag:podium-node", tailnetNodeTagHint(transport.Identity{}))
	require.Equal(t, "tag:podium-node (this device carries tag:ci,tag:prod)",
		tailnetNodeTagHint(transport.Identity{NodeTags: []string{"tag:ci", "tag:prod"}}))
}
