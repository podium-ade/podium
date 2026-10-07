import { Suspense, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Cable, Cpu, Server, Settings2, UserRound, Users } from "lucide-react";
import { Navigate, Route, Routes, useLocation } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChannelsPanel } from "../components/agent/ChannelsPanel";
import { ConnectionsPanel } from "../components/agent/ConnectionsPanel";
import { ChatPanel } from "../components/agent/ChatPanel";
import { McpCallback } from "../components/agent/McpCallback";
import { McpPanel } from "../components/agent/McpPanel";
import { ConductorDown } from "../components/agent/ConductorDown";
import { MemoryPanel } from "../components/agent/MemoryPanel";
import { ProfileCard, type ProfileFields } from "../components/agent/ProfileCard";
import { ProfileFileCard } from "../components/agent/ProfileFileCard";
import { ProviderCard } from "../components/agent/ProviderCard";
import { SandboxBackends } from "../components/agent/SandboxBackends";
import { SettingsSectionNav } from "../components/agent/SettingsSections";
import { ReloadProfileDirButton } from "../components/agent/ReloadProfileDirButton";
import { SessionsTable } from "../components/agent/SessionsTable";
import { PlaybooksPanel } from "../components/agent/PlaybooksPanel";
import { SkillsPanel } from "../components/agent/SkillsPanel";
import { Empty } from "../components/Empty";
import { IdentityCard } from "../components/IdentityCard";
import { PageActions, PageFrame } from "../components/PageHeader";
import { UsersPage } from "./UsersPage";
import { Alert } from "../components/ui/alert";
import { useToast } from "../components/Toast";
import { useAgents } from "../hooks/useAgents";
import { PROVIDERS } from "../lib/agents";
import { agent, errorMessage, isAgentUnreachable } from "../lib/client";
import { useViewer } from "../lib/identity";
import { canManageInfra } from "../lib/rbac";

/**
 * Tab is one assistant screen. Chat, Sessions, Memory and Assistant are links in the app
 * sidebar, as are Playbooks, Skills, MCP, Channels and Settings.
 *
 * `route` exists only for Chat, whose own screen takes a chat id after the segment.
 *
 * Chat is first because that is the thing an operator opens this tab to do. Settings used to
 * lead, which put a credentials form in front of the conversation.
 */
type Tab = {
  path: string;
  title: string;
  element: ReactNode;
  route?: string;
};

const tabs: Tab[] = [
  { path: "chat", title: "Chat", element: <ChatPanel />, route: "chat/*" },
  { path: "sessions", title: "Sessions", element: <SessionsTable /> },
  { path: "memory", title: "Memory", element: <MemoryPanel /> },
  { path: "profile", title: "Assistant", element: <ProfileTab /> },
];

/** Screens that share this route tree but are reached from the app sidebar, not the tab bar. */
const sidebarScreens: { path: string; element: ReactNode }[] = [
  { path: "playbooks", element: <PlaybooksPanel /> },
  { path: "skills", element: <SkillsPanel /> },
  { path: "mcp", element: <McpPanel /> },
  { path: "channels", element: <ChannelsPanel /> },
  // Where an OAuth authorization server sends the browser back to. It is a route in the SPA
  // rather than an endpoint on podium-server: an OAuth redirect carries no bearer token, so
  // a server route would have to sit outside the identity middleware. See lib/mcp.ts.
  { path: "mcp/callback", element: <McpCallback /> },
  { path: "models", element: <Navigate to="/agent/settings/models" replace /> },
  { path: "settings/*", element: <SettingsTab /> },
];

/**
 * AgentPage is the assistant's screens. Chat, Sessions, Memory and Assistant live in the
 * app sidebar. Each is still a real route, so the back button and a deep link both work.
 *
 * With no conductor configured there is nothing to show. The route still exists — a browser
 * that follows an old bookmark gets a sentence rather than a blank page — and the nav item
 * that leads here is hidden.
 */
export function AgentPage() {
  const viewer = useViewer();
  const { pathname } = useLocation();

  const onSettings = pathname === "/agent/settings" || pathname.startsWith("/agent/settings/");
  if (viewer && !viewer.agentEnabled && !onSettings) {
    return (
      <PageFrame title="Assistant">
        <Empty
          icon={Settings2}
          title="The conductor is not configured on this control plane"
          hint="Set PODIUM_AGENT_URL and PODIUM_AGENT_TOKEN on podium-server and run podium-agent beside it. See docs/agent.md."
        />
      </PageFrame>
    );
  }

  // Chat, Sessions, Memory and Assistant each draw their own bar. The other
  // screens already do. One shared bar kept Chat's full-bleed pane in place
  // while the next screen mounted into it.
  //
  // Memory suspends until its first read classifies the screen. This boundary is
  // what keeps that wait from committing an empty Memory page.
  return (
    <div className="h-full min-h-0">
      <Suspense fallback={null}>
      <Routes>
        <Route index element={<Navigate to="/agent/chat" replace />} />
        {tabs.map((t) => (
          <Route
            key={t.path}
            path={t.route ?? t.path}
            element={
              <PageFrame title={t.title} bleed={t.path === "chat"}>
                {t.element}
              </PageFrame>
            }
          />
        ))}
        {sidebarScreens.map((s) => (
          <Route key={s.path} path={s.path} element={s.element} />
        ))}
        <Route path="*" element={<Navigate to="/agent/chat" replace />} />
      </Routes>
      </Suspense>
    </div>
  );
}

/**
 * SettingsTab is a side list of sections and the open one. The address is the
 * section, so back and a deep link both land on it.
 */
function SettingsTab() {
  const viewer = useViewer();
  const { pathname } = useLocation();
  const tab = pathname.replace(/^\/agent\/settings\/?/, "").split("/")[0] ?? "";

  // GitHub and Slack credentials are infra. Members keep Models; they do not see Connections.
  const manage = canManageInfra(viewer);
  const categories: { id: string; label: string; icon: LucideIcon }[] = [
    ...(viewer?.googleAuthEnabled ? [{ id: "account", label: "Account", icon: UserRound }] : []),
    { id: "users", label: "Users", icon: Users },
    { id: "backend", label: "Backend", icon: Server },
    ...(viewer?.agentEnabled && manage
      ? [{ id: "connections", label: "Connections", icon: Cable }]
      : []),
    ...(viewer?.agentEnabled ? [{ id: "models", label: "Models", icon: Cpu }] : []),
  ];

  const fallback = categories[0]?.id ?? "";
  const current = categories.some((c) => c.id === tab) ? tab : "";
  if (!current) {
    return fallback ? (
      <Navigate to={`/agent/settings/${fallback}`} replace />
    ) : (
      <PageFrame title="Settings">
        <p className="text-sm text-muted">Nothing to configure.</p>
      </PageFrame>
    );
  }

  return (
    <PageFrame title="Settings">
      <div className="flex flex-col gap-6 sm:flex-row sm:items-start sm:gap-8">
        <SettingsSectionNav sections={categories} current={current} />
        <div className="min-w-0 flex-1">
        {current === "account" ? (
          <section className="max-w-2xl space-y-4">
            <p className="text-sm leading-relaxed text-muted">
              Who is signed in on this browser. The local token stays a machine credential for
              the CLI and workers.
            </p>
            <IdentityCard />
          </section>
        ) : null}
        {current === "users" ? (
          <section className="max-w-2xl space-y-4">
            <p className="text-sm leading-relaxed text-muted">
              Who can sign in, and what they can do. Roles are enforced on the API after a
              Workspace claim; the local token and every node still do everything.
            </p>
            <UsersPage />
          </section>
        ) : null}
        {current === "backend" ? (
          <section className="space-y-4">
            <p className="max-w-2xl text-sm leading-relaxed text-muted">
              Where a session's workspace runs. Self-hosted is this control plane's own nodes.
              Modal and Daytona will run the same session once they are connected.
            </p>
            <SandboxBackends />
          </section>
        ) : null}
        {current === "connections" ? (
          <section>
            <ConnectionsPanel />
          </section>
        ) : null}
        {current === "models" ? (
          <section>
            <ModelsPanel />
          </section>
        ) : null}
        </div>
      </div>
    </PageFrame>
  );
}

/**
 * ModelsPanel is one row per provider the conductor can spend. Every provider gets a row
 * whether or not it is configured: a Grok playbook that cannot run is easier to understand
 * next to a row that says "Not set" than as a failure on the next turn.
 *
 * The picker in Chat is which model a turn uses. This panel is how those models get a
 * credential.
 */
function ModelsPanel() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["agent", "settings"],
    queryFn: () => agent.getSettings({}),
    enabled: viewer?.agentEnabled === true,
  });
  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "settings"] });

  const save = useMutation({
    mutationFn: (v: { provider: string; key: string }) => agent.setProviderKey(v),
    onSuccess: reload,
  });
  const clear = useMutation({
    mutationFn: (provider: string) => agent.clearProviderKey({ provider }),
    onSuccess: reload,
  });

  if (viewer?.agentEnabled && settings.isError && !isAgentUnreachable(settings.error)) {
    return <Empty title="Could not read the agent settings" hint={errorMessage(settings.error)} />;
  }

  const stored = settings.data?.providers ?? [];

  return (
    <>
      <p className="max-w-2xl text-sm leading-relaxed text-muted">
        Connect a provider. Chat picks the model; this is what that picker can spend. A turn
        is handed one credential (the backend it names) and never the others.
      </p>

      {isAgentUnreachable(settings.error) ? (
        <ConductorDown
          what="The stored credentials could not be read"
          onRetry={() => void settings.refetch()}
          retrying={settings.isFetching}
        />
      ) : null}

      {!manage ? (
        <Alert variant="info" title="Credentials are read-only for members">
          An admin or owner can set and rotate provider keys. You can see which ones are
          connected.
        </Alert>
      ) : null}

      <ul className="divide-y divide-border border-y border-border">
        {PROVIDERS.map((p) => (
          <li key={p.id}>
            <ProviderCard
              provider={p}
              settings={stored.find((s) => s.provider === p.id)}
              loading={settings.isPending}
              readOnly={!manage}
              onSave={(key) => save.mutateAsync({ provider: p.id, key })}
              onClear={async () => {
                await clear.mutateAsync(p.id);
              }}
              onStartOAuth={
                manage && p.subscription ? () => agent.startProviderOAuth({ provider: p.id }) : undefined
              }
              onPollOAuth={
                manage && p.subscription
                  ? (flowId) => agent.pollProviderOAuth({ provider: p.id, flowId })
                  : undefined
              }
              onSignedIn={() => void reload()}
            />
          </li>
        ))}
      </ul>
      <p className="max-w-3xl text-xs leading-relaxed text-muted">
        Encrypted at rest by podium-server as{" "}
        {PROVIDERS.map((p, i) => (
          <span key={p.id}>
            {i > 0 ? ", " : ""}
            <code className="font-mono text-fg">{p.secretName}</code>
          </span>
        ))}
        .
      </p>
    </>
  );
}

/**
 * ProfileTab owns the profile RPCs. The card is presentational, and it is remounted by key
 * whenever the stored profile changes so its fields re-seed from what the server actually
 * holds rather than from what was typed before the last save.
 */
function ProfileTab() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const qc = useQueryClient();
  const toast = useToast();
  const profile = useQuery({
    queryKey: ["agent", "profile"],
    queryFn: () => agent.getProfile({}),
  });
  const { agents } = useAgents();

  const save = useMutation({
    mutationFn: (fields: ProfileFields) => agent.updateProfile(fields),
    onSuccess: async () => {
      toast("Profile saved. It applies to the next turn.", "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "profile"] });
    },
    onError: (err) => toast(errorMessage(err)),
  });

  if (profile.isError && !isAgentUnreachable(profile.error)) {
    return <Empty title="Could not read the agent profile" hint={errorMessage(profile.error)} />;
  }

  const p = profile.data?.profile;

  return (
    <div className="space-y-5">
      <PageActions>
        <ReloadProfileDirButton />
      </PageActions>
      {isAgentUnreachable(profile.error) ? (
        <ConductorDown
          what="The profile could not be read"
          onRetry={() => void profile.refetch()}
          retrying={profile.isFetching}
        />
      ) : null}
      <ProfileCard
        key={p ? `${p.name}:${p.overridden.join(",")}:${p.updatedAt?.seconds ?? 0}` : "loading"}
        profile={p}
        agents={agents}
        loading={profile.isPending}
        saving={save.isPending}
        readOnly={!manage}
        onSave={(fields) => save.mutate(fields)}
      />
      <ProfileFileCard />
    </div>
  );
}
