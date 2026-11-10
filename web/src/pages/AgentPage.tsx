import { Suspense, useState, type ReactNode } from "react";
import { Settings2 } from "lucide-react";
import { Navigate, Route, Routes, useLocation, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChannelsPanel } from "../components/agent/ChannelsPanel";
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
import { RoutinesPanel } from "../components/agent/RoutinesPanel";
import { SessionsTable } from "../components/agent/SessionsTable";
import { PlaybookPage, PlaybooksPanel } from "../components/agent/PlaybooksPanel";
import { SkillPage, SkillsPanel } from "../components/agent/SkillsPanel";
import { Empty } from "../components/Empty";
import { IdentityCard } from "../components/IdentityCard";
import { PageFrame } from "../components/PageHeader";
import {
  Table,
  TableBody,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import type { ProviderSettings } from "../gen/podium/agent/v1/agent_pb";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { CLAUDE_SUBSCRIPTION, PROVIDERS } from "../lib/agents";
import { agent, errorMessage, isAgentUnreachable } from "../lib/client";
import { useViewer, type Viewer } from "../lib/identity";
import { cn } from "../lib/utils";
import { canManageInfra } from "../lib/rbac";

/**
 * Tab is one assistant screen. Chat, Sessions, Memory and Assistants are links in the app
 * sidebar, as are Playbooks, Skills, MCP, Channels and each former settings section.
 *
 * `route` exists only for Chat, whose own screen takes a chat id after the segment.
 *
 * Chat is first because that is the thing an operator opens this tab to do. Settings used to
 * lead, which put a credentials form in front of the conversation.
 */
type Tab = {
  path: string;
  title: string;
  lead?: string;
  element: ReactNode;
  route?: string;
};

const tabs: Tab[] = [
  { path: "chat", title: "Chat", element: <ChatPanel />, route: "chat/*" },
  {
    path: "sessions",
    title: "Sessions",
    lead: "Every conversation your assistants have taken part in, wherever it started. Open one to see each turn.",
    element: <SessionsTable />,
  },
  {
    path: "memory",
    title: "Memory",
    lead: "What your assistants remember between conversations, and where each memory came from. Forget anything that shouldn't be kept.",
    element: <MemoryPanel />,
  },
];

/** Screens that share this route tree but are reached from the app sidebar, not the tab bar. */
const sidebarScreens: { path: string; element: ReactNode }[] = [
  { path: "playbooks", element: <PlaybooksPanel /> },
  { path: "playbooks/new", element: <PlaybookPage /> },
  { path: "playbooks/:name", element: <PlaybookPage /> },
  { path: "skills", element: <SkillsPanel /> },
  { path: "skills/new", element: <SkillPage /> },
  { path: "skills/:name", element: <SkillPage /> },
  { path: "mcp", element: <McpPanel /> },
  { path: "channels", element: <ChannelsPanel /> },
  { path: "routines", element: <RoutinesPanel /> },
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
              <PageFrame title={t.title} lead={t.lead} bleed={t.path === "chat"}>
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

/** An old /agent/profile bookmark lands on Podium's page, on the same section when it still exists. */
function ProfileRedirect() {
  const rest = useParams()["*"] ?? "";
  const section = rest.split("/").filter(Boolean)[0] ?? "";
  if (isAssistantSection(section)) {
    return <Navigate to={section === "identity" ? "/agent/assistants/podium" : `/agent/assistants/podium/${section}`} replace />;
  }
  return <Navigate to="/agent/assistants/podium" replace />;
}

/**
 * SETTINGS_SECTIONS are the pages that used to be Settings' tabs and are still the
 * viewer's own: their account and the models their chats spend. The organization's —
 * Members, Connections, Backend and the routines policy — moved to Organization settings
 * (pages/OrgSettingsPage); MOVED sends an old /agent/settings link there.
 */
const SETTINGS_SECTIONS: {
  id: string;
  title: string;
  lead: string;
  show: (v: Viewer | undefined) => boolean;
}[] = [
  {
    id: "account",
    title: "Your account",
    lead: "How you are signed in, and the GitHub account your assistants use on your behalf.",
    show: (v) => Boolean(v?.googleAuthEnabled),
  },
  {
    id: "models",
    title: "Models",
    lead: "The AI services your assistants run on. Add a key or sign in, and that service's models appear in Chat.",
    show: (v) => Boolean(v?.agentEnabled),
  },
];

/** Where each section that moved to Organization settings lives now. */
const MOVED: Record<string, string> = {
  users: "/org/settings/members",
  "runs-on": "/org/settings/backend",
  connections: "/org/settings/connections",
  routines: "/org/settings/routines",
};

/**
 * SettingsTab draws one former settings section as a page of its own. A bare
 * /agent/settings, or a section this viewer does not get, lands on the first one they do.
 */
function SettingsTab() {
  const viewer = useViewer();
  const { pathname } = useLocation();
  const tab = pathname.replace(/^\/agent\/settings\/?/, "").split("/")[0] ?? "";

  const moved = MOVED[tab];
  if (moved) return <Navigate to={moved} replace />;

  const sections = SETTINGS_SECTIONS.filter((s) => s.show(viewer));
  const section = sections.find((s) => s.id === tab);
  if (!section) {
    const fallback = sections[0];
    return fallback ? (
      <Navigate to={`/agent/settings/${fallback.id}`} replace />
    ) : (
      <Navigate to="/org/settings" replace />
    );
  }

  const current = section.id;

  return (
    <PageFrame title={section.title} lead={section.lead} bodyClassName="space-y-5 p-6">
      <section
        className="min-w-0 overflow-hidden rounded-lg border border-border bg-panel"
        aria-label={section.title}
      >
        <div className={current === "models" ? undefined : "grid gap-4 p-4"}>
          {current === "account" ? (
            <>
              <IdentityCard />
              {viewer?.agentEnabled ? <GitHubAccountCard /> : null}
            </>
          ) : null}
          {current === "models" ? <ModelsPanel /> : null}
        </div>
      </section>
    </PageFrame>
  );
}

/**
 * ModelsPanel is one row per provider the conductor can spend, on two lists: Yours, the
 * credentials a person saved for their own chats, and Global, the API keys an admin or owner
 * set for everyone. A chat spends its person's own credential when there is one and the
 * global key when there is not. Every provider gets a row on each list whether or not it is
 * configured: a Grok playbook that cannot run is easier to understand next to a row that says
 * "Not set" than as a failure on the next turn.
 *
 * The layout is the Secrets and MCP screens' own: Yours and Global tabs with a count, and a
 * scope bar that says what the list is for. A caller that is not a signed-in person — the
 * local token — has no list of its own and sees Global alone.
 *
 * The picker in Chat is which model a turn uses. This panel is how those models get a
 * credential.
 */
export function ModelsPanel({ globalOnly = false }: { globalOnly?: boolean } = {}) {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  // Organization settings shows the global keys alone: a person's own are theirs, not the
  // organization's, and stay on Compute › Models.
  const personal = !globalOnly && viewer?.kind === IdentityKind.USER;
  const [tab, setTab] = useState<"yours" | "global">(personal ? "yours" : "global");
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["agent", "settings"],
    queryFn: () => agent.getSettings({}),
    enabled: viewer?.agentEnabled === true,
  });
  const reload = () => {
    void qc.invalidateQueries({ queryKey: ["agent", "settings"] });
    void qc.invalidateQueries({ queryKey: ["agent", "agents"] });
  };

  const save = useMutation({
    mutationFn: (v: { provider: string; key: string; global: boolean }) => agent.setProviderKey(v),
    onSuccess: reload,
  });
  const clear = useMutation({
    mutationFn: (v: { provider: string; global: boolean; claudeSubscription?: boolean }) =>
      agent.clearProviderKey(v),
    onSuccess: reload,
  });

  if (viewer?.agentEnabled && settings.isError && !isAgentUnreachable(settings.error)) {
    return <Empty title="Could not read the agent settings" hint={errorMessage(settings.error)} />;
  }

  const claudeSub = settings.data?.claudeSubscription;
  const globalRows = settings.data?.providers ?? [];
  const yourRows = settings.data?.personal ?? [];
  const connected = (rows: ProviderSettings[]) => rows.filter((r) => r.keySet).length;
  const globalConnected = connected(globalRows) + (claudeSub?.keySet ? 1 : 0);

  const renderTable = (scope: "yours" | "global") => {
    const mine = scope === "yours";
    const readOnly = mine ? !personal : !manage;
    return (
      <Table containerClassName="rounded-none border-0 bg-transparent" className="min-w-[640px]">
        <caption className="sr-only">
          {mine ? "Your model credentials" : "Global model credentials"}
        </caption>
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
          {PROVIDERS.map((p) => {
            const globalRow = globalRows.find((s) => s.provider === p.id);
            // Anyone may sign in their own subscription; an admin may sign one in for everyone.
            // A global Claude sign-in is not the Anthropic row's: it has a row of its own below,
            // beside the global key.
            const subscription =
              p.subscription !== undefined && !readOnly && (mine || !p.subscription.personalOnly);
            // With no Anthropic credential of their own, a person's chats run on the global
            // Claude subscription when there is one, and on the global key otherwise.
            const onClaudeSub = mine && p.id === "anthropic" && Boolean(claudeSub?.keySet);
            const row = (
              <ProviderCard
                key={`${scope}-${p.id}`}
                provider={p}
                scope={mine ? "personal" : "global"}
                settings={(mine ? yourRows : globalRows).find((s) => s.provider === p.id)}
                fallbackSet={mine && (Boolean(globalRow?.keySet) || onClaudeSub)}
                fallbackLabel={onClaudeSub ? "Using the global subscription" : undefined}
                loading={settings.isPending}
                readOnly={readOnly}
                onSave={(key) => save.mutateAsync({ provider: p.id, key, global: !mine })}
                onClear={async () => {
                  await clear.mutateAsync({ provider: p.id, global: !mine });
                }}
                onStartOAuth={
                  subscription
                    ? () => agent.startProviderOAuth({ provider: p.id, global: !mine })
                    : undefined
                }
                onPollOAuth={
                  subscription
                    ? (flowId, code) => agent.pollProviderOAuth({ provider: p.id, flowId, code })
                    : undefined
                }
                onSignedIn={reload}
              />
            );
            if (mine || p.id !== "anthropic" || !claudeSub) return row;
            return [
              row,
              <ProviderCard
                key={`${scope}-${CLAUDE_SUBSCRIPTION.id}`}
                provider={CLAUDE_SUBSCRIPTION}
                scope="global"
                subscriptionOnly
                settings={claudeSub}
                loading={settings.isPending}
                readOnly={readOnly}
                clearNote="Chats with the assistant go back to the global API key. People who signed in their own keep using it."
                onSave={() =>
                  Promise.reject(new Error("a Claude subscription is signed in to, not keyed"))
                }
                onClear={async () => {
                  await clear.mutateAsync({
                    provider: "anthropic",
                    global: true,
                    claudeSubscription: true,
                  });
                }}
                onStartOAuth={
                  readOnly
                    ? undefined
                    : () => agent.startProviderOAuth({ provider: "anthropic", global: true })
                }
                onPollOAuth={
                  readOnly
                    ? undefined
                    : (flowId, code) =>
                        agent.pollProviderOAuth({ provider: "anthropic", flowId, code })
                }
                onSignedIn={reload}
              />,
            ];
          })}
        </TableBody>
      </Table>
    );
  };

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

      {personal ? (
        <Tabs value={tab} onValueChange={(value) => setTab(value as "yours" | "global")}>
          <TabsList className="px-4" aria-label="Model credential scope">
            <TabsTrigger
              value="yours"
              className="gap-2 py-2.5 text-base leading-[1.55]"
              aria-label={`Yours, ${connected(yourRows)} connected`}
            >
              Yours
              <TabCount count={connected(yourRows)} />
            </TabsTrigger>
            <TabsTrigger
              value="global"
              className="gap-2 py-2.5 text-base leading-[1.55]"
              aria-label={`Global, ${globalConnected} connected`}
            >
              Global
              <TabCount count={globalConnected} />
            </TabsTrigger>
          </TabsList>
          <ModelsScopeBar personal={tab === "yours"} manage={manage} />
          <TabsContent value="yours" data-testid="models-yours" className="pt-0">
            {renderTable("yours")}
          </TabsContent>
          <TabsContent value="global" data-testid="models-global" className="pt-0">
            {renderTable("global")}
          </TabsContent>
        </Tabs>
      ) : (
        <>
          <ModelsScopeBar personal={false} manage={manage} />
          {renderTable("global")}
        </>
      )}
    </>
  );
}

function ModelsScopeBar({ personal, manage }: { personal: boolean; manage: boolean }) {
  return (
    <div className="border-b border-border bg-raised px-6 py-4">
      <p className="m-0 text-soft">
        {personal
          ? "Your own keys and subscriptions. Chats you start use them before the global key."
          : manage
            ? "Keys and subscriptions for the workspace. Any chat without a credential of its own uses these."
            : "Keys and subscriptions for the workspace, used by any chat without a credential of its own. Only an admin or owner can change them."}
      </p>
    </div>
  );
}

function TabCount({ count }: { count: number }) {
  return (
    <span
      aria-hidden
      className="inline-grid h-5 min-w-[22px] place-items-center rounded-sm border border-line-strong bg-panel px-1.5 text-xs font-bold text-fg tabular-nums"
    >
      {count}
    </span>
  );
}

const settingsHead =
  "px-4 py-3 text-xs font-[750] leading-[normal] tracking-[0.08em] text-muted uppercase";


