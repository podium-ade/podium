import type { LucideIcon } from "lucide-react";
import {
  Bot,
  ChevronsUpDown,
  Coins,
  Container,
  KeyRound,
  ListTodo,
  LogOut,
  Plug,
  Puzzle,
  Server,
  Settings2,
  Sparkles,
} from "lucide-react";
import { Link, useLocation } from "react-router";
import { cn } from "@/lib/utils";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { useViewer, viewerLabel, type Viewer } from "../lib/identity";
import { Avatar } from "./Avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";

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
  const { pathname } = useLocation();
  const settingsOn = pathActive(pathname, "/agent/settings");
  const showSettings = Boolean(who?.agentEnabled || who?.googleAuthEnabled);
  return (
    <aside className="flex h-full w-56 shrink-0 flex-col border-r border-sidebar-border bg-sidebar">
      <div className="flex h-14 items-center gap-2 px-3">
        <span
          aria-hidden
          className="grid size-6 shrink-0 place-items-center rounded-md bg-accent/15 text-accent ring-1 ring-accent/25"
        >
          <svg viewBox="0 0 16 16" className="size-3.5" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round">
            <path d="M3 12.5V9m5 3.5V4m5 8.5V6.5" />
          </svg>
        </span>
        <span className="min-w-0 flex-1 font-mono text-sm font-semibold tracking-tight">
          podium<span className="text-accent">.</span>
        </span>
        {showSettings ? (
          <Link
            to="/agent/settings"
            aria-label="Settings"
            aria-current={settingsOn ? "page" : undefined}
            className={cn(
              "grid size-8 shrink-0 place-items-center rounded-md transition-colors",
              settingsOn
                ? "bg-raised text-fg"
                : "text-muted hover:bg-raised/70 hover:text-fg",
            )}
          >
            <Settings2 className={cn("size-4", settingsOn && "text-accent")} />
          </Link>
        ) : null}
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
        <Item to="/registries" icon={Container}>
          Registries
        </Item>
      </nav>

      <div className="mt-auto border-t border-sidebar-border p-2">
        <AccountMenu who={who} />
      </div>
    </aside>
  );
}

function accountSubtitle(who: Viewer | undefined, label: string) {
  if (!who) return __PODIUM_VERSION__;
  if (who.kind === IdentityKind.USER) return who.login || label;
  return __PODIUM_VERSION__;
}

/**
 * AccountMenu is the sidebar footer: avatar, name, and Sign out for a Google session.
 * Settings lives next to the wordmark — it is an app destination, not an account action.
 */
function AccountMenu({ who }: { who: Viewer | undefined }) {
  const label = viewerLabel(who);
  const subtitle = accountSubtitle(who, label.text);
  const canSignOut = Boolean(who?.googleAuthEnabled && who.kind === IdentityKind.USER);
  const picture = who?.pictureUrl ? "/auth/picture" : "";

  const identity = (
    <>
      <Avatar src={picture} label={label.text} />
      <span className="min-w-0 flex-1 text-left">
        <span className="block truncate text-sm font-medium text-fg">{label.text}</span>
        <span className="block truncate text-2xs text-faint">{subtitle}</span>
      </span>
    </>
  );

  if (!canSignOut) {
    return (
      <div className="flex w-full items-center gap-2.5 rounded-lg px-2 py-1.5" title={label.title}>
        {identity}
      </div>
    );
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className={cn(
          "flex w-full items-center gap-2.5 rounded-lg px-2 py-1.5 text-left",
          "outline-none transition-colors hover:bg-raised/70",
          "focus-visible:ring-2 focus-visible:ring-ring/50",
          "data-[state=open]:bg-raised",
        )}
        aria-label={label.text}
        title={label.title}
      >
        {identity}
        <ChevronsUpDown className="size-3.5 shrink-0 text-faint" />
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="start" className="w-56">
        <div className="flex items-center gap-2.5 px-2 py-2">
          <Avatar src={picture} label={label.text} size="md" />
          <div className="min-w-0">
            <div className="truncate text-sm font-medium text-fg">{label.text}</div>
            <div className="truncate text-2xs text-faint">{subtitle}</div>
          </div>
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <a href="/auth/logout">
            <LogOut />
            Sign out
          </a>
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
