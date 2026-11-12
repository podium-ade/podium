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

	podiumv1connect.TaskServiceCreateTaskProcedure:          store.RoleMember,
	podiumv1connect.TaskServiceGetTaskProcedure:             store.RoleMember,
	podiumv1connect.TaskServiceListTasksProcedure:           store.RoleMember,
	podiumv1connect.TaskServiceCancelTaskProcedure:          store.RoleMember,
	podiumv1connect.TaskServiceInjectTaskProcedure:          store.RoleMember,
	podiumv1connect.TaskServiceReleasePreviewProcedure:      store.RoleMember,
	podiumv1connect.TaskServiceCreateDesktopTicketProcedure: store.RoleMember,
	podiumv1connect.TaskServiceStreamTaskEventsProcedure:    store.RoleMember,

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
	podiumv1connect.NodeServiceGetSandboxSnapshotProcedure:        store.RoleAdmin,
	podiumv1connect.NodeServicePutSandboxSnapshotProcedure:        store.RoleAdmin,

	// Set and Delete are member so a person can write their own secret. The handler
	// refuses a global write from anyone who is not an admin. KindAgent and the dev
	// token skip this map and are checked in the handler too.
	podiumv1connect.SecretServiceListSecretsProcedure:  store.RoleMember,
	podiumv1connect.SecretServiceSetSecretProcedure:    store.RoleMember,
	podiumv1connect.SecretServiceDeleteSecretProcedure: store.RoleMember,
	// The handler refuses everyone but the conductor, which skips this table.
	podiumv1connect.SecretServiceReadSecretProcedure: store.RoleOwner,
	// The conductor writing a person's sharing decisions to audit_log. The handler refuses
	// everyone but the conductor, which skips this table; the owner role only keeps a person
	// from even reaching it.
	podiumv1connect.AuditServiceRecordAuditProcedure: store.RoleOwner,

	podiumv1connect.RegistryServiceListRegistriesProcedure: store.RoleMember,
	podiumv1connect.RegistryServiceSetRegistryProcedure:    store.RoleAdmin,
	podiumv1connect.RegistryServiceDeleteRegistryProcedure: store.RoleAdmin,

	podiumv1connect.SnapshotServiceListSnapshotsProcedure:               store.RoleMember,
	podiumv1connect.SnapshotServiceDeleteSnapshotProcedure:              store.RoleAdmin,
	podiumv1connect.SnapshotServiceSetSnapshotRetentionProcedure:        store.RoleAdmin,
	podiumv1connect.SnapshotServiceListSandboxSnapshotsProcedure:        store.RoleMember,
	podiumv1connect.SnapshotServiceDeleteSandboxSnapshotsProcedure:      store.RoleAdmin,
	podiumv1connect.SnapshotServiceSetSandboxSnapshotRetentionProcedure: store.RoleAdmin,

	podiumv1connect.SandboxProviderServiceListSandboxProvidersProcedure:  store.RoleMember,
	podiumv1connect.SandboxProviderServiceSetSandboxProviderProcedure:    store.RoleAdmin,
	podiumv1connect.SandboxProviderServiceDeleteSandboxProviderProcedure: store.RoleAdmin,

	podiumv1connect.OrganizationServiceGetOrganizationProcedure:    store.RoleMember,
	podiumv1connect.OrganizationServiceUpdateOrganizationProcedure: store.RoleAdmin,

	agentv1connect.AgentServiceListSessionsProcedure:   store.RoleMember,
	agentv1connect.AgentServiceGetSessionProcedure:     store.RoleMember,
	agentv1connect.AgentServiceListTurnsProcedure:      store.RoleMember,
	agentv1connect.AgentServiceGetUsageProcedure:       store.RoleMember,
	agentv1connect.AgentServiceGetSettingsProcedure:    store.RoleMember,
	agentv1connect.AgentServiceGetRunsOnProcedure:      store.RoleMember,
	agentv1connect.AgentServiceListAgentsProcedure:     store.RoleMember,
	agentv1connect.AgentServiceListMemoriesProcedure:   store.RoleMember,
	agentv1connect.AgentServiceSearchMemoriesProcedure: store.RoleMember,
	agentv1connect.AgentServiceListPlaybooksProcedure:  store.RoleMember,
	agentv1connect.AgentServiceGetProfileProcedure:     store.RoleMember,
	// The raw file is the Assistant source editor. Members keep GetProfile because the
	// playbook screen reads it; they do not get the file, and they do not see the page.
	agentv1connect.AgentServiceGetProfileFileProcedure:    store.RoleAdmin,
	agentv1connect.AgentServiceListSkillsProcedure:        store.RoleMember,
	agentv1connect.AgentServiceListMcpServersProcedure:    store.RoleMember,
	agentv1connect.AgentServiceListSlackChannelsProcedure: store.RoleMember,
	agentv1connect.AgentServiceCreateChatProcedure:        store.RoleMember,
	agentv1connect.AgentServiceListChatsProcedure:         store.RoleMember,
	// A personality is one member's voice. The handler refuses a row whose login is
	// not the caller, so member is the gate and ownership is the check.
	agentv1connect.AgentServiceListPersonalitiesProcedure: store.RoleMember,
	agentv1connect.AgentServiceCreatePersonalityProcedure: store.RoleMember,
	agentv1connect.AgentServiceUpdatePersonalityProcedure: store.RoleMember,
	agentv1connect.AgentServiceDeletePersonalityProcedure: store.RoleMember,
	agentv1connect.AgentServiceRenameChatProcedure:        store.RoleMember,
	agentv1connect.AgentServiceDeleteChatProcedure:        store.RoleMember,
	agentv1connect.AgentServiceSendChatMessageProcedure:   store.RoleMember,
	// Retrying a delivery re-sends what the bot already said, where it was owed; the
	// conductor checks the caller can read the chat.
	agentv1connect.AgentServiceRetryChatDeliveryProcedure: store.RoleMember,
	// Phase 4: a member takes back their own queued turn (the store matches the login),
	// stops the answer in a chat they may write (the handler checks), and looks people up
	// for the composer's @-mention list.
	agentv1connect.AgentServiceCancelQueuedTurnProcedure: store.RoleMember,
	agentv1connect.AgentServiceCancelChatTurnProcedure:   store.RoleMember,
	agentv1connect.AgentServiceSearchMembersProcedure:    store.RoleMember,
	// Phase 5: sharing. Any member may share a chat they own, leave one they are in, keep
	// their own read marks and read their own inbox; the conductor answers who owns what
	// (chat_role), so the member role here is only "a signed-in person".
	agentv1connect.AgentServiceGetChatSharingProcedure:        store.RoleMember,
	agentv1connect.AgentServiceUpdateChatSharingProcedure:     store.RoleMember,
	agentv1connect.AgentServiceShareChatProcedure:             store.RoleMember,
	agentv1connect.AgentServiceUnshareChatProcedure:           store.RoleMember,
	agentv1connect.AgentServiceLeaveChatProcedure:             store.RoleMember,
	agentv1connect.AgentServiceMarkChatReadProcedure:          store.RoleMember,
	agentv1connect.AgentServiceListNotificationsProcedure:     store.RoleMember,
	agentv1connect.AgentServiceMarkNotificationsReadProcedure: store.RoleMember,
	agentv1connect.AgentServiceStreamChatProcedure:            store.RoleMember,
	agentv1connect.AgentServiceAttachChatPullRequestProcedure: store.RoleMember,
	agentv1connect.AgentServiceDetachChatPullRequestProcedure: store.RoleMember,
	// Routines: any member keeps their own, and the conductor answers only for routines the
	// caller owns (another person's is not found), so member is the gate and ownership is the
	// check, as for personalities. A system routine is the conductor's to refuse: it checks
	// the infra header the proxy sets for admins and owners.
	agentv1connect.AgentServiceListRoutinesProcedure:         store.RoleMember,
	agentv1connect.AgentServiceGetRoutineProcedure:           store.RoleMember,
	agentv1connect.AgentServiceCreateRoutineProcedure:        store.RoleMember,
	agentv1connect.AgentServiceUpdateRoutineProcedure:        store.RoleMember,
	agentv1connect.AgentServiceDeleteRoutineProcedure:        store.RoleMember,
	agentv1connect.AgentServicePauseRoutineProcedure:         store.RoleMember,
	agentv1connect.AgentServiceResumeRoutineProcedure:        store.RoleMember,
	agentv1connect.AgentServiceRunRoutineNowProcedure:        store.RoleMember,
	agentv1connect.AgentServiceResetRoutineStateProcedure:    store.RoleMember,
	agentv1connect.AgentServiceResolveRoutineNoteProcedure:   store.RoleMember,
	agentv1connect.AgentServiceListRoutineRunsProcedure:      store.RoleMember,
	agentv1connect.AgentServiceListRoutineRevisionsProcedure: store.RoleMember,
	agentv1connect.AgentServiceCancelRoutineRunProcedure:     store.RoleMember,
	agentv1connect.AgentServiceGetRoutineLimitsProcedure:     store.RoleMember,
	// Every person's routines as metadata, and the per-person limits: admins.
	agentv1connect.AgentServiceListMemberRoutinesProcedure: store.RoleAdmin,
	agentv1connect.AgentServiceSetRoutineLimitsProcedure:   store.RoleAdmin,

	agentv1connect.AgentServiceSetRunsOnProcedure: store.RoleAdmin,
	// A member saves their own model credential; the handler refuses a global write from
	// anyone the proxy did not mark as infra, the same split as the MCP registry.
	agentv1connect.AgentServiceSetProviderKeyProcedure:             store.RoleMember,
	agentv1connect.AgentServiceClearProviderKeyProcedure:           store.RoleMember,
	agentv1connect.AgentServiceStartProviderOAuthProcedure:         store.RoleMember,
	agentv1connect.AgentServicePollProviderOAuthProcedure:          store.RoleMember,
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
	agentv1connect.AgentServiceCreateMcpServerProcedure:            store.RoleMember,
	agentv1connect.AgentServiceUpdateMcpServerProcedure:            store.RoleMember,
	agentv1connect.AgentServiceDeleteMcpServerProcedure:            store.RoleMember,
	agentv1connect.AgentServiceSetMcpServerTokenProcedure:          store.RoleMember,
	agentv1connect.AgentServiceClearMcpServerTokenProcedure:        store.RoleMember,
	agentv1connect.AgentServiceStartMcpOAuthProcedure:              store.RoleMember,
	agentv1connect.AgentServiceCompleteMcpOAuthProcedure:           store.RoleMember,
	agentv1connect.AgentServiceGetGitHubAccountProcedure:           store.RoleMember,
	agentv1connect.AgentServiceStartGitHubOAuthProcedure:           store.RoleMember,
	agentv1connect.AgentServiceCompleteGitHubOAuthProcedure:        store.RoleMember,
	agentv1connect.AgentServiceDisconnectGitHubAccountProcedure:    store.RoleMember,
	agentv1connect.AgentServiceSetSlackChannelDescriptionProcedure: store.RoleAdmin,

	// GitHub, Slack and Linear live under Settings → Connections. Hints are still credentials.
	agentv1connect.AgentServiceGetConnectionsProcedure:          store.RoleAdmin,
	agentv1connect.AgentServiceSetSlackConnectionProcedure:      store.RoleAdmin,
	agentv1connect.AgentServiceClearSlackConnectionProcedure:    store.RoleAdmin,
	agentv1connect.AgentServiceSetGitHubConnectionProcedure:     store.RoleAdmin,
	agentv1connect.AgentServiceClearGitHubConnectionProcedure:   store.RoleAdmin,
	agentv1connect.AgentServiceSetGitHubWebhookFiltersProcedure: store.RoleAdmin,
	agentv1connect.AgentServiceSetLinearConnectionProcedure:     store.RoleAdmin,
	agentv1connect.AgentServiceClearLinearConnectionProcedure:   store.RoleAdmin,
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
