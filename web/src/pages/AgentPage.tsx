import type { ReactNode } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChatPanel } from "../components/agent/ChatPanel";
import { MemoryPanel } from "../components/agent/MemoryPanel";
import { ProfileCard, type ProfileFields } from "../components/agent/ProfileCard";
import { ProviderCard } from "../components/agent/ProviderCard";
import { SessionsTable } from "../components/agent/SessionsTable";
import { SkillsPanel } from "../components/agent/SkillsPanel";
import { Empty } from "../components/Empty";
import { useToast } from "../components/Toast";
import { useAgents } from "../hooks/useAgents";
import { PROVIDERS } from "../lib/agents";
import { agent, errorMessage, isAgentUnreachable } from "../lib/client";
import { useViewer } from "../lib/identity";

/**
 * Tab is one sub-route of /agent. The array below is the whole extension point: a new screen
 * is a line here and nothing else in this file.
 *
 * `path` is the bare segment the NavLink builds `/agent/${path}` from — keep it that way,
 * because a relative NavLink does not go active inside the `/agent/*` splat route. `route`
 * is the pattern the nested Routes matches, and it exists only for Chat, whose own screen
 * takes a chat id after the segment.
 */
type Tab = { path: string; label: string; element: ReactNode; route?: string };

const tabs: Tab[] = [
  { path: "settings", label: "Settings", element: <SettingsTab /> },
  { path: "profile", label: "Profile", element: <ProfileTab /> },
  { path: "skills", label: "Skills", element: <SkillsPanel /> },
  { path: "sessions", label: "Sessions", element: <SessionsTable /> },
  { path: "memory", label: "Memory", element: <MemoryPanel /> },
  { path: "chat", label: "Chat", element: <ChatPanel />, route: "chat/*" },
];

const tabLink = ({ isActive }: { isActive: boolean }) =>
  `rounded px-3 py-1.5 text-sm ${
    isActive ? "bg-raised text-fg" : "text-muted hover:text-fg"
  } focus-visible:ring-1 focus-visible:ring-accent`;

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
  if (viewer && !viewer.agentEnabled) {
    return (
      <Empty
        title="The conductor is not configured on this control plane"
        hint="Set PODIUM_AGENT_URL and PODIUM_AGENT_TOKEN on podium-server and run podium-agent beside it. See docs/agent.md."
      />
    );
  }

  return (
    <div className="space-y-4">
      <nav className="flex gap-1 border-b border-border pb-2">
        {tabs.map((t) => (
          <NavLink key={t.path} to={`/agent/${t.path}`} className={tabLink}>
            {t.label}
          </NavLink>
        ))}
      </nav>
      <Routes>
        <Route index element={<Navigate to="/agent/settings" replace />} />
        {tabs.map((t) => (
          <Route key={t.path} path={t.route ?? t.path} element={t.element} />
        ))}
        <Route path="*" element={<Navigate to="/agent/settings" replace />} />
      </Routes>
    </div>
  );
}

/**
 * SettingsTab owns the RPCs and hands each card the functions it needs.
 *
 * Every provider gets a card whether or not it is configured, because the card is also
 * where an operator finds out that it is not: a Grok skill that cannot run is easier to
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

  return (
    <div className="space-y-4">
      {isAgentUnreachable(settings.error) ? (
        <p className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          podium-agent is not reachable. Check its /readyz on PODIUM_AGENT_LISTEN.
        </p>
      ) : null}

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

      <p className="max-w-3xl text-xs text-muted">
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
  const skills = (profile.data?.skills ?? []).filter((s) => !s.shadowed).map((s) => s.name);

  return (
    <div className="space-y-4">
      {isAgentUnreachable(profile.error) ? (
        <p className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          podium-agent is not reachable. Check its /readyz on PODIUM_AGENT_LISTEN.
        </p>
      ) : null}
      <ProfileCard
        key={p ? `${p.name}:${p.overridden.join(",")}:${p.updatedAt?.seconds ?? 0}` : "loading"}
        profile={p}
        skills={skills}
        agents={agents}
        loading={profile.isPending}
        saving={save.isPending}
        onSave={(fields) => save.mutate(fields)}
      />
    </div>
  );
}
