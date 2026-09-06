import type { LucideIcon } from "lucide-react";
import { Bot, KeyRound, ListTodo, Server } from "lucide-react";
import { NavLink } from "react-router";
import { cn } from "@/lib/utils";
import { useViewer, viewerLabel } from "../lib/identity";

function navClass({ isActive }: { isActive: boolean }) {
  return cn(
    "flex items-center gap-2 rounded-md px-2.5 py-1.5 text-sm transition-colors",
    isActive ? "bg-raised text-fg" : "text-muted hover:bg-raised/70 hover:text-fg",
  );
}

function Item({
  to,
  end,
  icon: Icon,
  children,
}: {
  to: string;
  end?: boolean;
  icon: LucideIcon;
  children: string;
}) {
  return (
    <NavLink to={to} end={end} className={navClass}>
      <Icon className="size-4 shrink-0" />
      {children}
    </NavLink>
  );
}

export function Header() {
  const who = useViewer();
  const viewer = viewerLabel(who);
  return (
    <aside className="flex h-full w-56 shrink-0 flex-col border-r border-sidebar-border bg-sidebar">
      <div className="flex h-14 items-center gap-2 px-4">
        <span className="font-mono text-sm font-semibold tracking-tight">
          podium<span className="text-accent">.</span>
        </span>
      </div>
      <nav className="flex flex-1 flex-col gap-1 px-2">
        <p className="px-2.5 pt-1 pb-1.5 text-[11px] font-medium tracking-wider text-muted uppercase">
          Workspace
        </p>
        <Item to="/" end icon={ListTodo}>
          Tasks
        </Item>
        <Item to="/nodes" icon={Server}>
          Nodes
        </Item>
        <Item to="/secrets" icon={KeyRound}>
          Secrets
        </Item>
        {/* Only where there is a conductor to talk to. WhoAmI says so, so a control plane
            without one shows no dead end. */}
        {who?.agentEnabled ? (
          <>
            <p className="px-2.5 pt-4 pb-1.5 text-[11px] font-medium tracking-wider text-muted uppercase">
              Agent
            </p>
            <Item to="/agent" icon={Bot}>
              Agent
            </Item>
          </>
        ) : null}
      </nav>
      <div className="mt-auto space-y-1 border-t border-sidebar-border px-3 py-3 text-xs text-muted">
        {/* Whoever WhoAmI says is looking: a Tailscale login on a tailnet, "dev" on the dev
            transport, which has no per-user identity at all. */}
        <div className="truncate" title={viewer.title}>
          {viewer.text}
        </div>
        <div className="font-mono text-[11px] opacity-80">{__PODIUM_VERSION__}</div>
      </div>
    </aside>
  );
}
