package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/podium-ade/podium/internal/server/store"
	"github.com/podium-ade/podium/internal/transport"
)

// artifactDownloadPrefix is api.ArtifactDownloadPrefix, duplicated so this package does
// not import the API. Members may download; the bytes are already gated by identity.
const artifactDownloadPrefix = "/artifacts/"

// catalog is the role each Connect procedure on podium-server requires of a KindUser
// after the instance is claimed. KindLocalToken, KindAgent, and KindNode skip it entirely.
//
// An unclassified path defaults to admin: a new mutation is not accidentally public.
// TestCatalogCoversEveryServerProcedure fails if a procedure is missing here.
var catalog = map[string]string{
	podiumv1connect.IdentityServiceWhoAmIProcedure: "",
	podiumv1connect.IdentityServiceClaimProcedure:  "",

	podiumv1connect.UserServiceListUsersProcedure:   store.RoleMember,
	podiumv1connect.UserServiceSetUserRoleProcedure: store.RoleOwner,

	podiumv1connect.TaskServiceCreateTaskProcedure:       store.RoleMember,
	podiumv1connect.TaskServiceGetTaskProcedure:          store.RoleMember,
	podiumv1connect.TaskServiceListTasksProcedure:        store.RoleMember,
	podiumv1connect.TaskServiceCancelTaskProcedure:       store.RoleMember,
	podiumv1connect.TaskServiceInjectTaskProcedure:       store.RoleMember,
	podiumv1connect.TaskServiceStreamTaskEventsProcedure: store.RoleMember,

	podiumv1connect.ArtifactServiceListArtifactsProcedure:  store.RoleMember,
	podiumv1connect.ArtifactServiceGetArtifactURLProcedure: store.RoleMember,

	podiumv1connect.NodeAdminServiceListNodesProcedure:             store.RoleMember,
	podiumv1connect.NodeAdminServiceCreateEnrollmentTokenProcedure: store.RoleAdmin,
	podiumv1connect.NodeAdminServiceRekeyNodeProcedure:             store.RoleAdmin,
	podiumv1connect.NodeAdminServiceDrainNodeProcedure:             store.RoleAdmin,
	podiumv1connect.NodeAdminServiceUndrainNodeProcedure:           store.RoleAdmin,
	podiumv1connect.NodeAdminServiceSetNodeSlotsProcedure:          store.RoleAdmin,
	podiumv1connect.NodeAdminServiceSetNodeLabelsProcedure:         store.RoleAdmin,
	podiumv1connect.NodeAdminServiceDeleteNodeProcedure:            store.RoleAdmin,

	// NodeService is the worker protocol. KindNode bypasses this map; a browser
	// hitting it is an accident and is treated as infra.
	podiumv1connect.NodeServiceEnrollProcedure:                    store.RoleAdmin,
	podiumv1connect.NodeServiceStreamProcedure:                    store.RoleAdmin,
	podiumv1connect.NodeServiceUploadArtifactProcedure:            store.RoleAdmin,
	podiumv1connect.NodeServiceUploadWorkspaceSnapshotProcedure:   store.RoleAdmin,
	podiumv1connect.NodeServiceDownloadWorkspaceSnapshotProcedure: store.RoleAdmin,

	podiumv1connect.SecretServiceListSecretsProcedure:  store.RoleMember,
	podiumv1connect.SecretServiceSetSecretProcedure:    store.RoleAdmin,
	podiumv1connect.SecretServiceDeleteSecretProcedure: store.RoleAdmin,

	podiumv1connect.RegistryServiceListRegistriesProcedure: store.RoleMember,
	podiumv1connect.RegistryServiceSetRegistryProcedure:    store.RoleAdmin,
	podiumv1connect.RegistryServiceDeleteRegistryProcedure: store.RoleAdmin,

	agentv1connect.AgentServiceListSessionsProcedure:          store.RoleMember,
	agentv1connect.AgentServiceGetSessionProcedure:            store.RoleMember,
	agentv1connect.AgentServiceListTurnsProcedure:             store.RoleMember,
	agentv1connect.AgentServiceGetUsageProcedure:              store.RoleMember,
	agentv1connect.AgentServiceGetSettingsProcedure:           store.RoleMember,
	agentv1connect.AgentServiceListAgentsProcedure:            store.RoleMember,
	agentv1connect.AgentServiceListMemoriesProcedure:          store.RoleMember,
	agentv1connect.AgentServiceSearchMemoriesProcedure:        store.RoleMember,
	agentv1connect.AgentServiceListPlaybooksProcedure:         store.RoleMember,
	agentv1connect.AgentServiceGetProfileProcedure:            store.RoleMember,
	agentv1connect.AgentServiceGetProfileFileProcedure:        store.RoleMember,
	agentv1connect.AgentServiceListSkillsProcedure:            store.RoleMember,
	agentv1connect.AgentServiceListMcpServersProcedure:        store.RoleMember,
	agentv1connect.AgentServiceListSlackChannelsProcedure:     store.RoleMember,
	agentv1connect.AgentServiceCreateChatProcedure:            store.RoleMember,
	agentv1connect.AgentServiceListChatsProcedure:             store.RoleMember,
	agentv1connect.AgentServiceRenameChatProcedure:            store.RoleMember,
	agentv1connect.AgentServiceDeleteChatProcedure:            store.RoleMember,
	agentv1connect.AgentServiceSendChatMessageProcedure:       store.RoleMember,
	agentv1connect.AgentServiceStreamChatProcedure:            store.RoleMember,
	agentv1connect.AgentServiceAttachChatPullRequestProcedure: store.RoleMember,
	agentv1connect.AgentServiceDetachChatPullRequestProcedure: store.RoleMember,

	agentv1connect.AgentServiceSetProviderKeyProcedure:             store.RoleAdmin,
	agentv1connect.AgentServiceClearProviderKeyProcedure:           store.RoleAdmin,
	agentv1connect.AgentServiceStartProviderOAuthProcedure:         store.RoleAdmin,
	agentv1connect.AgentServicePollProviderOAuthProcedure:          store.RoleAdmin,
	agentv1connect.AgentServiceDeleteMemoryProcedure:               store.RoleAdmin,
	agentv1connect.AgentServiceUpdateProfileProcedure:              store.RoleAdmin,
	agentv1connect.AgentServiceUpdateProfileFileProcedure:          store.RoleAdmin,
	agentv1connect.AgentServiceReloadProfileDirProcedure:           store.RoleAdmin,
	agentv1connect.AgentServiceCreatePlaybookProcedure:             store.RoleAdmin,
	agentv1connect.AgentServiceUpdatePlaybookProcedure:             store.RoleAdmin,
	agentv1connect.AgentServiceDeletePlaybookProcedure:             store.RoleAdmin,
	agentv1connect.AgentServiceUploadSkillProcedure:                store.RoleAdmin,
	agentv1connect.AgentServiceSetSkillEnabledProcedure:            store.RoleAdmin,
	agentv1connect.AgentServiceDeleteSkillProcedure:                store.RoleAdmin,
	agentv1connect.AgentServiceCreateMcpServerProcedure:            store.RoleAdmin,
	agentv1connect.AgentServiceUpdateMcpServerProcedure:            store.RoleAdmin,
	agentv1connect.AgentServiceDeleteMcpServerProcedure:            store.RoleAdmin,
	agentv1connect.AgentServiceSetMcpServerTokenProcedure:          store.RoleAdmin,
	agentv1connect.AgentServiceClearMcpServerTokenProcedure:        store.RoleAdmin,
	agentv1connect.AgentServiceStartMcpOAuthProcedure:              store.RoleAdmin,
	agentv1connect.AgentServiceCompleteMcpOAuthProcedure:           store.RoleAdmin,
	agentv1connect.AgentServiceSetSlackChannelDescriptionProcedure: store.RoleAdmin,
}

// RestrictRBAC forbids a KindUser from procedures their role cannot call, once the
// instance is claimed and Google sign-in is on. Nodes, the local token, and the
// conductor are not humans and are not gated. A no-op when googleEnabled is false,
// so existing tailnet-only deployments do not change.
func RestrictRBAC(st instanceView, googleEnabled bool, next http.Handler) http.Handler {
	if st == nil || !googleEnabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := transport.From(r.Context())
		if !ok || id.Kind != transport.KindUser {
			next.ServeHTTP(w, r)
			return
		}
		_, err := st.GetInstance(r.Context())
		if errors.Is(err, store.ErrNotFound) {
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		need := requiredRole(r.URL.Path)
		if need == "" {
			next.ServeHTTP(w, r)
			return
		}
		roles := []string{}
		if user, err := st.GetUser(r.Context(), id.Login); err == nil {
			roles = user.Roles
		}
		if store.HasRole(roles, need) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, fmt.Sprintf("this action requires the %s role", need), http.StatusForbidden)
	})
}

func requiredRole(path string) string {
	if strings.HasPrefix(path, artifactDownloadPrefix) {
		return store.RoleMember
	}
	if r, ok := catalog[path]; ok {
		return r
	}
	return store.RoleAdmin
}
