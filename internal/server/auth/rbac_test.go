package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/internal/transport"
)

type fakeView struct {
	inst    store.Instance
	instErr error
	users   map[string]store.User
}

func (f fakeView) GetInstance(context.Context) (store.Instance, error) {
	if f.instErr != nil {
		return store.Instance{}, f.instErr
	}
	return f.inst, nil
}

func (f fakeView) GetUser(_ context.Context, login string) (store.User, error) {
	u, ok := f.users[login]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return u, nil
}

func TestRestrictRBACNoopWhenGoogleOff(t *testing.T) {
	t.Parallel()
	called := false
	h := RestrictRBAC(nil, false, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, podiumv1connect.NodeAdminServiceDrainNodeProcedure, nil)
	h.ServeHTTP(rec, req)
	require.True(t, called)
}

func TestRestrictRBACSkipsMachines(t *testing.T) {
	t.Parallel()
	view := claimedView("alice@acme.com", store.RoleMember)
	for _, kind := range []transport.IdentityKind{transport.KindLocalToken, transport.KindNode, transport.KindAgent} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			called := false
			h := RestrictRBAC(view, true, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			rec := hit(h, podiumv1connect.NodeAdminServiceDrainNodeProcedure, transport.Identity{
				Kind:  kind,
				Login: "local",
			})
			require.Equal(t, http.StatusOK, rec.Code)
			require.True(t, called)
		})
	}
}

func TestRestrictRBACMemberCannotDrain(t *testing.T) {
	t.Parallel()
	view := claimedView("bob@acme.com", store.RoleMember)
	h := RestrictRBAC(view, true, okHandler())
	rec := hit(h, podiumv1connect.NodeAdminServiceDrainNodeProcedure, user("bob@acme.com"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "admin")
}

func TestRestrictRBACAdminCanDrainButNotSetRoles(t *testing.T) {
	t.Parallel()
	view := claimedView("cara@acme.com", store.RoleAdmin)
	h := RestrictRBAC(view, true, okHandler())

	drain := hit(h, podiumv1connect.NodeAdminServiceDrainNodeProcedure, user("cara@acme.com"))
	require.Equal(t, http.StatusOK, drain.Code)

	roles := hit(h, podiumv1connect.UserServiceSetUserRoleProcedure, user("cara@acme.com"))
	require.Equal(t, http.StatusForbidden, roles.Code)
	require.Contains(t, roles.Body.String(), "owner")
}

func TestRestrictRBACOwnerCanSetRoles(t *testing.T) {
	t.Parallel()
	view := claimedView("alice@acme.com", store.RoleOwner)
	h := RestrictRBAC(view, true, okHandler())
	rec := hit(h, podiumv1connect.UserServiceSetUserRoleProcedure, user("alice@acme.com"))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestRestrictRBACMemberCanListAndCreateTasks(t *testing.T) {
	t.Parallel()
	view := claimedView("bob@acme.com", store.RoleMember)
	h := RestrictRBAC(view, true, okHandler())
	require.Equal(t, http.StatusOK, hit(h, podiumv1connect.TaskServiceCreateTaskProcedure, user("bob@acme.com")).Code)
	require.Equal(t, http.StatusOK, hit(h, podiumv1connect.TaskServiceListTasksProcedure, user("bob@acme.com")).Code)
	require.Equal(t, http.StatusOK, hit(h, podiumv1connect.UserServiceListUsersProcedure, user("bob@acme.com")).Code)
	require.Equal(t, http.StatusOK, hit(h, podiumv1connect.SecretServiceListSecretsProcedure, user("bob@acme.com")).Code)
	require.Equal(t, http.StatusOK, hit(h, podiumv1connect.SecretServiceSetSecretProcedure, user("bob@acme.com")).Code,
		"a member may call SetSecret; the handler refuses a global write")
	require.Equal(t, http.StatusOK, hit(h, agentv1connect.AgentServiceCreateMcpServerProcedure, user("bob@acme.com")).Code,
		"a member may register their own MCP server; the conductor refuses a bot-list write")
}

func TestRestrictRBACUnclaimedPassesThrough(t *testing.T) {
	t.Parallel()
	view := fakeView{instErr: store.ErrNotFound}
	h := RestrictRBAC(view, true, okHandler())
	rec := hit(h, podiumv1connect.TaskServiceCreateTaskProcedure, user("alice@acme.com"))
	require.Equal(t, http.StatusOK, rec.Code, "RestrictUnclaimed owns the unclaimed gate")
}

func TestRestrictRBACUnknownPathRequiresAdmin(t *testing.T) {
	t.Parallel()
	view := claimedView("bob@acme.com", store.RoleMember)
	h := RestrictRBAC(view, true, okHandler())
	rec := hit(h, "/podium.v1.FutureService/Boom", user("bob@acme.com"))
	require.Equal(t, http.StatusForbidden, rec.Code)

	admin := claimedView("cara@acme.com", store.RoleAdmin)
	h = RestrictRBAC(admin, true, okHandler())
	require.Equal(t, http.StatusOK, hit(h, "/podium.v1.FutureService/Boom", user("cara@acme.com")).Code)
}

func TestCatalogCoversEveryServerProcedure(t *testing.T) {
	t.Parallel()
	skip := map[string]bool{
		// Served on the conductor, not on podium-server's identity mux.
		"podium.agent.v1.TurnService":          true,
		"podium.agent.v1.GitCredentialService": true,
	}
	files := []protoreflect.FileDescriptor{
		podiumv1.File_podium_v1_identity_proto,
		podiumv1.File_podium_v1_user_proto,
		podiumv1.File_podium_v1_task_proto,
		podiumv1.File_podium_v1_admin_proto,
		podiumv1.File_podium_v1_node_proto,
		podiumv1.File_podium_v1_secret_proto,
		podiumv1.File_podium_v1_registry_proto,
		podiumv1.File_podium_v1_artifact_proto,
		agentv1.File_podium_agent_v1_agent_proto,
	}
	var missing []string
	seen := map[string]bool{}
	for _, fd := range files {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			svc := svcs.Get(i)
			if skip[string(svc.FullName())] {
				continue
			}
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				path := "/" + string(svc.FullName()) + "/" + string(methods.Get(j).Name())
				seen[path] = true
				if _, ok := catalog[path]; !ok {
					missing = append(missing, path)
				}
			}
		}
	}
	require.Empty(t, missing, "catalog is missing procedures; classify them in rbac.go")
	for path := range catalog {
		require.True(t, seen[path], "catalog has %s which is not a generated procedure", path)
	}
}

func claimedView(login, role string) fakeView {
	return fakeView{
		inst: store.Instance{HostedDomain: "acme.com", ClaimedBy: "alice@acme.com"},
		users: map[string]store.User{
			login: {Login: login, Roles: []string{role}},
		},
	}
}

func user(login string) transport.Identity {
	return transport.Identity{Kind: transport.KindUser, Login: login}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func hit(h http.Handler, path string, id transport.Identity) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req = req.WithContext(transport.NewContext(req.Context(), id))
	h.ServeHTTP(rec, req)
	return rec
}
