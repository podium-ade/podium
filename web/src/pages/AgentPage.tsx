import type { ReactNode } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChatPanel } from "../components/agent/ChatPanel";
import { MemoryPanel } from "../components/agent/MemoryPanel";
import { ProfileCard, type ProfileFields } from "../components/agent/ProfileCard";
import { ProviderKeyCard } from "../components/agent/ProviderKeyCard";
import { SessionsTable } from "../components/agent/SessionsTable";
import { SkillsPanel } from "../components/agent/SkillsPanel";
import { Empty } from "../components/Empty";
import { useToast } from "../components/Toast";
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

/** SettingsTab owns the RPCs and hands the card two functions. */
function SettingsTab() {
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["agent", "settings"],
    queryFn: () => agent.getSettings({}),
  });

  const save = useMutation({
    mutationFn: (key: string) => agent.setProviderKey({ provider: "anthropic", key }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["agent", "settings"] }),
  });
  const clear = useMutation({
    mutationFn: () => agent.clearProviderKey({ provider: "anthropic" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["agent", "settings"] }),
  });

  // A conductor that is down is not a broken page: the card still renders, and its status
  // strip is where the operator is told. Any other failure to read the settings is.
  if (settings.isError && !isAgentUnreachable(settings.error)) {
    return <Empty title="Could not read the agent settings" hint={errorMessage(settings.error)} />;
  }

  return (
    <div className="space-y-4">
      {isAgentUnreachable(settings.error) ? (
        <p className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          podium-agent is not reachable. Check its /readyz on PODIUM_AGENT_LISTEN.
        </p>
      ) : null}
      <ProviderKeyCard
        settings={settings.data?.provider}
        loading={settings.isPending}
        onSave={async (key) => {
          const res = await save.mutateAsync(key);
          return res;
        }}
        onClear={async () => {
          await clear.mutateAsync();
        }}
      />
      <p className="text-xs text-muted">
        The key is stored as the Podium secret{" "}
        <code className="font-mono text-fg">podium.agent.anthropic_api_key</code>. There is no
        way to read a stored secret back — the last four characters above were kept at save
        time, and that is all any part of the UI ever sees of it.
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
        loading={profile.isPending}
        saving={save.isPending}
        onSave={(fields) => save.mutate(fields)}
      />
    </div>
  );
}
