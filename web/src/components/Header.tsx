import type { LucideIcon } from "lucide-react";
import { Bot, Coins, KeyRound, ListTodo, Plug, Puzzle, Server, Settings2, Sparkles } from "lucide-react";
import { Link, useLocation } from "react-router";
import { cn } from "@/lib/utils";
import { useViewer, viewerLabel } from "../lib/identity";
import { Tooltip } from "./ui/tooltip";

function pathActive(pathname: string, to: string, end?: boolean) {
  if (end) return pathname === to;
  return pathname === to || pathname.startsWith(`${to}/`);
}

/**
 * Agent in the sidebar is the talk screens (chat, sessions, memory, profile). Playbooks,
 * Skills, MCP and Settings are siblings, not children, so a prefix match on /agent would
 * light Agent on every one of them.
 */
const agentSiblings = ["/agent/playbooks", "/agent/skills", "/agent/mcp", "/agent/settings"];

function agentActive(pathname: string) {
  if (pathname !== "/agent" && !pathname.startsWith("/agent/")) return false;
  return !agentSiblings.some((p) => pathname.startsWith(p));
}

function Item({
  to,
  end,
  icon: Icon,
  children,
  match,
  className,
}: {
  to: string;
  end?: boolean;
  icon: LucideIcon;
  children: string;
  match?: (pathname: string) => boolean;
  className?: string;
}) {
  const { pathname } = useLocation();
  const isActive = match ? match(pathname) : pathActive(pathname, to, end);
  return (
    <Link
      to={to}
      aria-current={isActive ? "page" : undefined}
      className={cn(
        "group relative flex h-8 items-center gap-2.5 rounded-md pr-2.5 pl-3 text-sm transition-colors duration-150",
        // The active rail is the only chrome that moves, so the eye can find the current
        // screen without reading the labels.
        "before:absolute before:top-1.5 before:bottom-1.5 before:-left-2 before:w-0.5 before:rounded-full before:bg-accent",
        "before:origin-center before:scale-y-0 before:transition-transform before:duration-200",
        isActive
          ? "bg-raised font-medium text-fg before:scale-y-100"
          : "text-muted hover:bg-raised/55 hover:text-fg",
        className,
      )}
    >
      <Icon className={cn("size-4 shrink-0 transition-colors", isActive && "text-accent")} />
      {children}
    </Link>
  );
}

function SectionLabel({ children }: { children: string }) {
  return (
    <p className="px-3 pt-1 pb-1.5 text-2xs font-medium tracking-[0.08em] text-faint uppercase">
      {children}
    </p>
  );
}

export function Header() {
  const who = useViewer();
  const viewer = viewerLabel(who);
  return (
    <aside className="flex h-full w-56 shrink-0 flex-col border-r border-sidebar-border bg-sidebar">
      <div className="flex h-14 items-center gap-2.5 px-4">
        <span
          aria-hidden
          className="grid size-6 place-items-center rounded-md bg-accent/15 text-accent ring-1 ring-accent/25"
        >
          <svg viewBox="0 0 16 16" className="size-3.5" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round">
            <path d="M3 12.5V9m5 3.5V4m5 8.5V6.5" />
          </svg>
        </span>
        <span className="font-mono text-sm font-semibold tracking-tight">
          podium<span className="text-accent">.</span>
        </span>
      </div>

      <nav className="flex flex-1 flex-col gap-0.5 overflow-y-auto px-4 pb-2">
        {/* Only where there is a conductor to talk to. WhoAmI says so, so a control plane
            without one shows no dead end. */}
        {who?.agentEnabled ? (
          <>
            <SectionLabel>Agent</SectionLabel>
            <Item to="/agent" icon={Bot} match={agentActive}>
              Agent
            </Item>
            <Item to="/agent/playbooks" icon={Sparkles}>
              Playbooks
            </Item>
            <Item to="/agent/skills" icon={Puzzle}>
              Skills
            </Item>
            <Item to="/agent/mcp" icon={Plug}>
              MCP
            </Item>
            <div className="pt-4" />
          </>
        ) : null}
        <SectionLabel>Workspace</SectionLabel>
        <Item to="/" end icon={ListTodo}>
          Tasks
        </Item>
        {/* Cost is recorded per agent turn, so without a conductor this screen is a table of
            dashes. Same gate as the Agent section. */}
        {who?.agentEnabled ? (
          <Item to="/usage" icon={Coins}>
            Usage
          </Item>
        ) : null}
        <Item to="/nodes" icon={Server}>
          Nodes
        </Item>
        <Item to="/secrets" icon={KeyRound}>
          Secrets
        </Item>
      </nav>

      <div className="mt-auto border-t border-sidebar-border px-4 py-3">
        {/* Whoever WhoAmI says is looking: a Tailscale login on a tailnet, "dev" on the dev
            transport, which has no per-user identity at all. Settings sits under the picture
            because it is about this login, not about the agent screens above. */}
        <Tooltip label={viewer.title} side="right">
          <div className="flex min-w-0 items-center gap-2">
            <span
              aria-hidden
              className="grid size-6 shrink-0 place-items-center rounded-full bg-raised text-2xs font-semibold text-muted uppercase"
            >
              {viewer.text.slice(0, 1)}
            </span>
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm text-fg">{viewer.text}</div>
              <div className="truncate font-mono text-2xs text-faint">{__PODIUM_VERSION__}</div>
            </div>
          </div>
        </Tooltip>
        {who?.agentEnabled ? (
          <nav aria-label="Profile" className="pt-1.5">
            <Item to="/agent/settings" icon={Settings2} className="text-xs">
              Settings
            </Item>
          </nav>
        ) : null}
      </div>
    </aside>
  );
}
