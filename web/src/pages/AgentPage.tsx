import type { ReactNode } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MemoryPanel } from "../components/agent/MemoryPanel";
import { ProviderKeyCard } from "../components/agent/ProviderKeyCard";
import { SessionsTable } from "../components/agent/SessionsTable";
import { Empty } from "../components/Empty";
import { agent, errorMessage, isAgentUnreachable } from "../lib/client";
import { useViewer } from "../lib/identity";

/**
 * Tab is one sub-route of /agent. The array below is the whole extension point: step 21 adds
 * a line (`/agent/chat`) and nothing else here changes.
 */
type Tab = { path: string; label: string; element: ReactNode };

const tabs: Tab[] = [
  { path: "settings", label: "Settings", element: <SettingsTab /> },
  { path: "sessions", label: "Sessions", element: <SessionsTable /> },
  { path: "memory", label: "Memory", element: <MemoryPanel /> },
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
          <Route key={t.path} path={t.path} element={t.element} />
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
