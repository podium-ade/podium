import { Suspense, type ReactNode } from "react";
import { Settings2 } from "lucide-react";
import { Link, Navigate, Route, Routes, useLocation, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChannelsPanel } from "../components/agent/ChannelsPanel";
import { ConnectionsPanel } from "../components/agent/ConnectionsPanel";
import { ChatPanel } from "../components/agent/ChatPanel";
import { GitHubAccountCard } from "../components/agent/GitHubAccountCard";
import { GitHubCallback } from "../components/agent/GitHubCallback";
import { McpCallback } from "../components/agent/McpCallback";
import { McpPanel } from "../components/agent/McpPanel";
import { ConductorDown } from "../components/agent/ConductorDown";
import { MemoryPanel } from "../components/agent/MemoryPanel";
import { Assistants } from "../components/agent/Assistants";
import { isAssistantSection } from "../lib/assistantProfile";
import { ProviderCard } from "../components/agent/ProviderCard";
import { RunsOnSettings } from "../components/agent/RunsOnSettings";
import { SessionsTable } from "../components/agent/SessionsTable";
import { PlaybooksPanel } from "../components/agent/PlaybooksPanel";
import { SkillsPanel } from "../components/agent/SkillsPanel";
import { Empty } from "../components/Empty";
import { IdentityCard } from "../components/IdentityCard";
import { PageFrame } from "../components/PageHeader";
import { UsersPage } from "./UsersPage";
import { Alert } from "../components/ui/alert";
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import { PROVIDERS } from "../lib/agents";
import { agent, errorMessage, isAgentUnreachable } from "../lib/client";
import { useViewer } from "../lib/identity";
import { cn } from "../lib/utils";
import { canManageInfra } from "../lib/rbac";

/**
 * Tab is one assistant screen. Chat, Sessions, Memory and Assistants are links in the app
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
  { path: "github/callback", element: <GitHubCallback /> },
  { path: "models", element: <Navigate to="/agent/settings/models" replace /> },
  { path: "settings/*", element: <SettingsTab /> },
];

/**
 * AgentPage is the assistant's screens. Chat, Sessions, Memory and Assistants live in the
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

  // Chat, Sessions, Memory and Assistants each draw their own bar. The other
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
        <Route path="assistants/*" element={<Assistants />} />
        <Route path="profile/*" element={<ProfileRedirect />} />
        {sidebarScreens.map((s) => (
          <Route key={s.path} path={s.path} element={s.element} />
        ))}
        <Route path="*" element={<Navigate to="/agent/chat" replace />} />
      </Routes>
      </Suspense>
    </div>
  );
}

/** An old /agent/profile bookmark lands on Podium, on the same section when it still exists. */
function ProfileRedirect() {
  const rest = useParams()["*"] ?? "";
  const section = rest.split("/").filter(Boolean)[0] ?? "";
  if (isAssistantSection(section)) {
    return <Navigate to={`/agent/assistants/podium/${section}`} replace />;
  }
  return <Navigate to="/agent/assistants" replace />;
}

const SETTINGS_LEAD: Record<string, string> = {
  account:
    "Who is signed in on this browser. The local token stays a machine credential for the CLI and workers.",
  users:
    "Who can sign in, and what they can do. Roles are enforced on the API after a Workspace claim. The local token and every node still do everything.",
  "runs-on":
    "Where a session's turns run when their playbook does not say: this fleet's own nodes, or a sandbox provider set up on Sandboxes.",
  connections:
    "The outside apps the assistant works with. GitHub lets it read your code and open pull requests. Slack lets people message it. Changes take effect the next time the conductor restarts.",
  models:
    "The AI services the assistant runs on. Add a key or sign in, and that service's models show up in Chat. Each chat only gets the key it needs.",
};

/**
 * SettingsTab is the settings frame. Each section keeps its own address, so back and a
 * deep link still land on it. The names are the design's underline tabs.
 */
function SettingsTab() {
  const viewer = useViewer();
  const { pathname } = useLocation();
  const tab = pathname.replace(/^\/agent\/settings\/?/, "").split("/")[0] ?? "";

  // GitHub and Slack credentials are infra. Members keep Models; they do not see Connections.
  const manage = canManageInfra(viewer);
  const categories: { id: string; label: string }[] = [
    ...(viewer?.googleAuthEnabled ? [{ id: "account", label: "Account" }] : []),
    { id: "users", label: "Users" },
    ...(viewer?.agentEnabled ? [{ id: "runs-on", label: "Runs on" }] : []),
    ...(viewer?.agentEnabled && manage ? [{ id: "connections", label: "Connections" }] : []),
    ...(viewer?.agentEnabled ? [{ id: "models", label: "Models" }] : []),
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

  const flush = current === "users" || current === "connections" || current === "models";

  return (
    <PageFrame title="Settings" bodyClassName="p-6">
      <section
        className="min-w-0 overflow-hidden rounded-lg border border-border bg-panel"
        aria-label="Settings"
      >
        <nav
          aria-label="Settings sections"
          className="flex w-full items-center gap-2 overflow-x-auto border-b border-border px-4"
        >
          {categories.map((section) => {
            const open = section.id === current;
            return (
              <Link
                key={section.id}
                to={`/agent/settings/${section.id}`}
                aria-current={open ? "page" : undefined}
                className={cn(
                  "inline-flex min-h-11 shrink-0 items-center border-b-[3px] px-3.5 py-2.5 text-base leading-[1.55] font-semibold whitespace-nowrap",
                  open
                    ? "border-accent text-fg"
                    : "border-transparent text-soft hover:bg-raised hover:text-fg",
                )}
              >
                {section.label}
              </Link>
            );
          })}
        </nav>
        <div className="border-b border-border p-4">
          <p className="max-w-[68ch] text-soft">{SETTINGS_LEAD[current]}</p>
        </div>
        <div className={flush ? undefined : "grid gap-4 p-4"}>
          {current === "account" ? (
            <>
              <IdentityCard />
              {viewer?.agentEnabled ? <GitHubAccountCard /> : null}
            </>
          ) : null}
          {current === "users" ? <UsersPage /> : null}
          {current === "runs-on" ? <RunsOnSettings /> : null}
          {current === "connections" ? <ConnectionsPanel /> : null}
          {current === "models" ? <ModelsPanel /> : null}
        </div>
      </section>
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
      {isAgentUnreachable(settings.error) ? (
        <div className="border-b border-border p-4">
          <ConductorDown
            what="The stored credentials could not be read"
            onRetry={() => void settings.refetch()}
            retrying={settings.isFetching}
          />
        </div>
      ) : null}

      {!manage ? (
        <div className="border-b border-border p-4">
          <Alert variant="info" title="Credentials are read-only for members">
            An admin or owner can set and rotate provider keys. You can see which ones are
            connected.
          </Alert>
        </div>
      ) : null}

      <Table containerClassName="rounded-none border-0 bg-transparent" className="min-w-[640px]">
        <caption className="sr-only">Model providers</caption>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className={cn(settingsHead, "w-[40%]")}>Model</TableHead>
            <TableHead className={cn(settingsHead, "w-[20%]")}>Status</TableHead>
            <TableHead className={settingsHead}>Credential</TableHead>
            <TableHead className={settingsHead}>
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {PROVIDERS.map((p) => (
            <ProviderCard
              key={p.id}
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
          ))}
        </TableBody>
      </Table>
    </>
  );
}

const settingsHead =
  "px-4 py-3 text-xs font-[750] leading-[normal] tracking-[0.08em] text-muted uppercase";


