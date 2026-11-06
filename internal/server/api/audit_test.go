package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/transport"
)

type auditRow struct {
	actor, action, subject string
	details                map[string]any
}

type fakeAuditStore struct{ rows []auditRow }

func (f *fakeAuditStore) Audit(_ context.Context, actor, action, subject string, details map[string]any) error {
	f.rows = append(f.rows, auditRow{actor, action, subject, details})
	return nil
}

// Only the conductor may write, and only the chat actions it has: its credential cannot be
// used to forge a row that looks like one of the server's own.
func TestRecordAuditIsTheConductorsAlone(t *testing.T) {
	t.Parallel()
	st := &fakeAuditStore{}
	svc := NewAuditService(st, nil)
	req := func(action, details string) *connect.Request[podiumv1.RecordAuditRequest] {
		return connect.NewRequest(&podiumv1.RecordAuditRequest{
			Actor: "alice@acme.com", Action: action, Subject: "chat_1", DetailsJson: details,
		})
	}
	as := func(kind transport.IdentityKind) context.Context {
		return transport.NewContext(context.Background(), transport.Identity{Kind: kind, Login: "x"})
	}

	for _, kind := range []transport.IdentityKind{transport.KindUser, transport.KindLocalToken, transport.KindNode} {
		_, err := svc.RecordAudit(as(kind), req("chat.share", ""))
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), kind)
	}
	_, err := svc.RecordAudit(context.Background(), req("chat.share", ""))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "no identity at all")
	_, err = svc.RecordAudit(as(transport.KindAgent), req("secret.set", ""))
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "not a chat action")
	_, err = svc.RecordAudit(as(transport.KindAgent), req("chat.share", "[1,2]"))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "details must be an object")
	assert.Empty(t, st.rows)

	_, err = svc.RecordAudit(as(transport.KindAgent), req("chat.share", `{"login":"bob@acme.com","role":"editor"}`))
	require.NoError(t, err)
	require.Len(t, st.rows, 1)
	assert.Equal(t, auditRow{"alice@acme.com", "chat.share", "chat_1",
		map[string]any{"login": "bob@acme.com", "role": "editor"}}, st.rows[0])
}
