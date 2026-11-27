//go:build integration

package nodes_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/server/store"
)

// TestTheConductorRecordsAuditThroughTheServer is the conductor's audit row, sent the way
// the conductor sends it. AuditService was registered and never mounted, so the request fell
// through to the UI handler and came back 405, and every row was dropped with a warning.
func TestTheConductorRecordsAuditThroughTheServer(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	conductor := podiumv1connect.NewAuditServiceClient(h2cClient(agentToken), h.url)
	_, err := conductor.RecordAudit(ctx, connect.NewRequest(&podiumv1.RecordAuditRequest{
		Actor:       "ada@acme.com",
		Action:      "identity.on_behalf",
		Subject:     "sess_audit",
		DetailsJson: `{"login":"ada"}`,
	}))
	require.NoError(t, err)

	st, err := store.New(ctx, h.databaseURL)
	require.NoError(t, err)
	defer st.Close()
	rows, err := st.ListAudit(ctx, "identity.on_behalf", "sess_audit", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "ada@acme.com", rows[0].Actor)

	// Anybody but the conductor reaches the handler too, and is refused by it rather than by
	// the routing: the refusal is a permission, not a missing route.
	_, err = podiumv1connect.NewAuditServiceClient(h.http, h.url).RecordAudit(ctx,
		connect.NewRequest(&podiumv1.RecordAuditRequest{Actor: "x", Action: "identity.on_behalf", Subject: "y"}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}
