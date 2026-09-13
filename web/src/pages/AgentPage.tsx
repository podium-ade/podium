import { Fragment, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import {
  Brain,
  History,
  MessageSquare,
  Settings2,
  UserRound,
} from "lucide-react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChatPanel } from "../components/agent/ChatPanel";
import { McpCallback } from "../components/agent/McpCallback";
import { McpPanel } from "../components/agent/McpPanel";
import { ConductorDown } from "../components/agent/ConductorDown";
import { MemoryPanel } from "../components/agent/MemoryPanel";
import { ProfileCard, type ProfileFields } from "../components/agent/ProfileCard";
import { ProviderCard } from "../components/agent/ProviderCard";
import { ReloadProfileDirButton } from "../components/agent/ReloadProfileDirButton";
import { SessionsTable } from "../components/agent/SessionsTable";
import { PlaybooksPanel } from "../components/agent/PlaybooksPanel";
import { SkillsPanel } from "../components/agent/SkillsPanel";
import { Badge, Chip } from "../components/Badge";
import { Empty } from "../components/Empty";
import { PageHeader } from "../components/PageHeader";
import { useToast } from "../components/Toast";
import { Separator } from "../components/ui/separator";
import { useAgents } from "../hooks/useAgents";
import { PROVIDERS } from "../lib/agents";
import { agent, errorMessage, isAgentUnreachable } from "../lib/client";
import { useViewer } from "../lib/identity";
import { cn } from "../lib/utils";

/**
 * Tab is one sub-route of /agent that still lives in this page's own bar. The array below
 * is the extension point for those; Playbooks, Skills, MCP and Settings are in the app sidebar
 * instead — they are destinations of their own, not something you switch between while talking.
 *
 * `path` is the bare segment the NavLink builds `/agent/${path}` from — keep it that way,
 * because a relative NavLink does not go active inside the `/agent/*` splat route. `route`
 * is the pattern the nested Routes matches, and it exists only for Chat, whose own screen
 * takes a chat id after the segment.
 *
 * Chat is first because that is the thing an operator opens this tab to do. Settings used to
 * lead, which put a credentials form in front of the conversation.
 */
type Tab = {
  path: string;
  label: string;
  element: ReactNode;
  route?: string;
  icon: LucideIcon;
};

type Group = { label: string; tabs: Tab[] };

const groups: Group[] = [
  {
    label: "Talk",
    tabs: [
      { path: "chat", label: "Chat", element: <ChatPanel />, route: "chat/*", icon: MessageSquare },
      { path: "sessions", label: "Sessions", element: <SessionsTable />, icon: History },
      { path: "memory", label: "Memory", element: <MemoryPanel />, icon: Brain },
    ],
  },
  {
    label: "Configure",
    tabs: [{ path: "profile", label: "Assistant", element: <ProfileTab />, icon: UserRound }],
  },
];

const tabs: Tab[] = groups.flatMap((g) => g.tabs);

/** Screens that share this route tree but are reached from the app sidebar, not the tab bar. */
const sidebarScreens: { path: string; element: ReactNode }[] = [
  { path: "playbooks", element: <PlaybooksPanel /> },
  { path: "skills", element: <SkillsPanel /> },
  { path: "mcp", element: <McpPanel /> },
  // Where an OAuth authorization server sends the browser back to. It is a route in the SPA
  // rather than an endpoint on podium-server: an OAuth redirect carries no bearer token, so
  // a server route would have to sit outside the identity middleware. See lib/mcp.ts.
  { path: "mcp/callback", element: <McpCallback /> },
  { path: "settings", element: <SettingsTab /> },
];

/**
 * The tab bar is horizontal, and it is a row of links rather than `ui/tabs` on purpose: an
 * active tab here is a route, so the back button and a deep link both work, and only an
 * anchor gives that for free. The classes are `TabsTrigger`'s so it still reads as one
 * component family.
 *
 * It used to be a 192px rail, which put a second grey nav column immediately right of the
 * app's own and spent a quarter of the window before any content.
 */
function tabLink({ isActive }: { isActive: boolean }) {
  return cn(
    "inline-flex h-8 items-center justify-center gap-1.5 rounded-md px-2.5 text-xs font-medium whitespace-nowrap",
    "transition-colors duration-150 ease-out outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
    "[&_svg]:size-3.5 [&_svg]:shrink-0",
    isActive ? "bg-raised text-fg shadow-xs" : "text-muted hover:text-fg",
  );
}

/**
 * AgentPage is the bot's home in the UI: a tab shell whose active tab is a real route, so
 * the back button and a deep link both work.
 *
 * With no conductor configured there is nothing to show. The route still exists — a browser
 * that follows an old bookmark gets a sentence rather than a blank page — and the nav item
 * that leads here is hidden.
 */
export function AgentPage() {
  const viewer = useViewer();
  const { pathname } = useLocation();
  const chat = pathname.startsWith("/agent/chat");

  if (viewer && !viewer.agentEnabled) {
    return (
      <div className="mx-auto w-full max-w-3xl px-6 py-16 lg:px-8">
        <Empty
          icon={Settings2}
          title="The conductor is not configured on this control plane"
          hint="Set PODIUM_AGENT_URL and PODIUM_AGENT_TOKEN on podium-server and run podium-agent beside it. See docs/agent.md."
        />
      </div>
    );
  }

  const routed = (
    <Routes>
      <Route index element={<Navigate to="/agent/chat" replace />} />
      {tabs.map((t) => (
        <Route key={t.path} path={t.route ?? t.path} element={t.element} />
      ))}
      {sidebarScreens.map((s) => (
        <Route key={s.path} path={s.path} element={s.element} />
      ))}
      <Route path="*" element={<Navigate to="/agent/chat" replace />} />
    </Routes>
  );

  const showSubnav =
    pathname === "/agent" ||
    pathname === "/agent/" ||
    groups.some((g) =>
      g.tabs.some((t) => {
        const prefix = `/agent/${t.path}`;
        return pathname === prefix || pathname.startsWith(`${prefix}/`);
      }),
    );

  return (
    <div className="flex h-full min-h-0 flex-col">
      {showSubnav ? (
        <div className="shrink-0 border-b border-border bg-panel/50">
          <nav
            aria-label="Agent"
            className="mx-auto flex w-full max-w-7xl flex-wrap items-center gap-x-3 gap-y-2 px-6 py-2.5 lg:px-8"
          >
            {groups.map((g, i) => (
              <Fragment key={g.label}>
                {i > 0 ? <Separator orientation="vertical" className="mx-1 hidden h-5 sm:block" /> : null}
                <div className="flex items-center gap-2">
                  <span className="text-2xs font-medium tracking-wider text-faint uppercase">
                    {g.label}
                  </span>
                  <div className="inline-flex h-9 items-center gap-0.5 rounded-lg border border-border bg-panel p-0.5">
                    {g.tabs.map((t) => (
                      <NavLink key={t.path} to={`/agent/${t.path}`} className={tabLink}>
                        <t.icon />
                        {t.label}
                      </NavLink>
                    ))}
                  </div>
                </div>
              </Fragment>
            ))}
          </nav>
        </div>
      ) : null}

      {/* Chat is a full-height pane and owns its own scrolling; every other tab is a page. */}
      {chat ? (
        <div className="min-h-0 flex-1 overflow-hidden">{routed}</div>
      ) : (
        // relative for the same reason as the shell's main — see App.tsx.
        <div className="relative min-h-0 flex-1 overflow-y-auto px-6 py-7 lg:px-8">
          <div key={pathname} className="mx-auto w-full max-w-7xl animate-in fade-in-0 duration-200">
            {routed}
          </div>
        </div>
      )}
    </div>
  );
}

/**
 * SettingsTab owns the RPCs and hands each card the functions it needs.
 *
 * Every provider gets a card whether or not it is configured, because the card is also
 * where an operator finds out that it is not: a Grok playbook that cannot run is easier to
 * understand next to a card that says "Not set" than as a failure on the next turn.
 */
function SettingsTab() {
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["agent", "settings"],
    queryFn: () => agent.getSettings({}),
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

  // A conductor that is down is not a broken page: the cards still render, and their status
  // strips are where the operator is told. Any other failure to read the settings is.
  if (settings.isError && !isAgentUnreachable(settings.error)) {
    return <Empty title="Could not read the agent settings" hint={errorMessage(settings.error)} />;
  }

  const stored = settings.data?.providers ?? [];
  const connected = PROVIDERS.filter(
    (p) => stored.find((s) => s.provider === p.id)?.keySet,
  ).length;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Settings"
        description="Credentials for the models this conductor can run. Each one is a Podium secret; the UI never sees more than the last four characters."
        // With nothing read back, "0 of 2 connected" would be a claim rather than a count.
        meta={
          settings.data === undefined ? null : (
            <>
              <Badge tone={connected > 0 ? "ok" : "idle"}>
                {connected} of {PROVIDERS.length} connected
              </Badge>
              {connected < PROVIDERS.length ? (
                <Chip>{PROVIDERS.length - connected} still to set up</Chip>
              ) : null}
            </>
          )
        }
      />
      {isAgentUnreachable(settings.error) ? (
        <ConductorDown
          what="The stored credentials could not be read"
          onRetry={() => void settings.refetch()}
          retrying={settings.isFetching}
        />
      ) : null}

      <div className="grid items-start gap-4 xl:grid-cols-2">
        {PROVIDERS.map((p) => (
          <ProviderCard
            key={p.id}
            provider={p}
            settings={stored.find((s) => s.provider === p.id)}
            loading={settings.isPending}
            onSave={(key) => save.mutateAsync({ provider: p.id, key })}
            onClear={async () => {
              await clear.mutateAsync(p.id);
            }}
            onStartOAuth={
              p.subscription ? () => agent.startProviderOAuth({ provider: p.id }) : undefined
            }
            onPollOAuth={
              p.subscription
                ? (flowId) => agent.pollProviderOAuth({ provider: p.id, flowId })
                : undefined
            }
            onSignedIn={() => void reload()}
          />
        ))}
      </div>

      <p className="max-w-3xl text-xs leading-relaxed text-muted">
        Each credential is stored as a Podium secret —{" "}
        {PROVIDERS.map((p, i) => (
          <span key={p.id}>
            {i > 0 ? ", " : ""}
            <code className="font-mono text-fg">{p.secretName}</code>
          </span>
        ))}{" "}
        — and a turn is handed only the one its backend spends. There is no way to read a
        stored secret back: the last four characters above were kept at save time, and that is
        all any part of the UI ever sees of a key.
      </p>
    </div>
  );
}

/**
 * ProfileTab owns the profile RPCs. The card is presentational, and it is remounted by key
 * whenever the stored profile changes so its fields re-seed from what the server actually
 * holds rather than from what was typed before the last save.
 */
function ProfileTab() {
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
      <PageHeader
        title="Assistant"
        description="Who answers a conversation, and which model it answers on. Every editable field here overrides profile.yaml on the conductor's host."
        actions={<ReloadProfileDirButton />}
      />
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
        onSave={(fields) => save.mutate(fields)}
      />
    </div>
  );
}
