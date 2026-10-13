import { useState } from "react";
import type { LucideIcon } from "lucide-react";
import {
  Brain,
  ChevronDown,
  Coins,
  Container,
  Hash,
  History,
  KeyRound,
  ListTodo,
  LogOut,
  MessageSquare,
  PanelLeftClose,
  Plug,
  Puzzle,
  Server,
  Settings2,
  Sparkles,
  UserRound,
} from "lucide-react";
import { Link, useLocation } from "react-router";
import { cn } from "@/lib/utils";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { useViewer, viewerLabel, type Viewer } from "../lib/identity";
import { Avatar } from "./Avatar";
import { useShell } from "./shell";
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

function Item({
  to,
  end,
  icon: Icon,
  children,
  match,
  onNavigate,
  collapsed,
}: {
  to: string;
  end?: boolean;
  icon: LucideIcon;
  children: string;
  match?: (pathname: string) => boolean;
  onNavigate?: () => void;
  collapsed: boolean;
}) {
  const { pathname } = useLocation();
  const isActive = match ? match(pathname) : pathActive(pathname, to, end);
  return (
    <Link
      to={to}
      aria-current={isActive ? "page" : undefined}
      aria-label={children}
      title={children}
      onClick={onNavigate}
      className={cn(
        "flex min-h-11 items-center gap-3 rounded-md border border-transparent px-3 text-left text-base font-normal text-soft",
        "hover:border-border hover:bg-raised hover:text-fg",
        collapsed && "md:justify-center md:px-0",
        isActive && "border-line-strong bg-raised font-[720] text-fg hover:border-line-strong hover:bg-raised hover:text-fg",
      )}
    >
      <Icon className="size-[18px] shrink-0 stroke-[1.8]" />
      <span className={cn("truncate", collapsed && "md:hidden")}>{children}</span>
    </Link>
  );
}

function SectionLabel({ children, collapsed }: { children: string; collapsed: boolean }) {
  return (
    <p
      className={cn(
        "px-3 pt-6 pb-2 text-xs font-bold tracking-[0.1em] text-muted uppercase",
        collapsed && "md:hidden",
      )}
    >
      {children}
    </p>
  );
}

function Wordmark({ collapsed }: { collapsed: boolean }) {
  return (
    <div
      className={cn(
        "flex h-16 shrink-0 items-center border-b border-border bg-panel px-4 text-[1.35rem] tracking-[-0.04em]",
        collapsed && "md:justify-center md:px-0",
      )}
    >
      <span className="grid size-9 shrink-0 place-items-center rounded-sm bg-accent font-mono text-[1.15rem] font-[800] text-primary-foreground">
        P
      </span>
      <span className={cn("ml-3 text-[1.35rem] font-[760] text-fg", collapsed && "md:hidden")}>
        podium<span className="text-accent">.</span>
      </span>
    </div>
  );
}

export function Header() {
  const who = useViewer();
  const { pathname } = useLocation();
  const { navOpen, setNavOpen, collapsed, toggleCollapsed } = useShell();
  // A navigation closes the drawer. Adjusting during render keeps the close in the same
  // pass as the route change, so the drawer does not linger for a frame.
  const [seenPath, setSeenPath] = useState(pathname);
  if (seenPath !== pathname) {
    setSeenPath(pathname);
    if (navOpen) setNavOpen(false);
  }
  const agent = Boolean(who?.agentEnabled);
  // Users lives on Settings even when this control plane has no conductor and no Google
  // sign-in, so the link stays once WhoAmI has answered.
  const showSettings = Boolean(who);

  const close = () => setNavOpen(false);
  const collapseLabel = collapsed ? "Expand sidebar" : "Collapse sidebar";
  return (
    <>
      {navOpen ? (
        <button
          type="button"
          aria-label="Close navigation"
          className="fixed inset-x-0 top-16 bottom-0 z-30 bg-[#071018]/66 md:hidden"
          onClick={close}
        />
      ) : null}
      <aside
        id="app-nav"
        aria-label="Primary application navigation"
        className={cn(
          "z-40 flex h-full w-[232px] shrink-0 flex-col border-r border-border bg-panel text-fg",
          collapsed && "md:w-[72px]",
          "max-md:fixed max-md:top-16 max-md:bottom-0 max-md:left-0 max-md:h-auto max-md:w-[min(280px,84vw)] max-md:shadow-[10px_0_28px_#0002]",
          navOpen ? "max-md:flex" : "max-md:hidden",
        )}
      >
        <Wordmark collapsed={collapsed} />

        <nav aria-label="Primary" className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-2 py-3">
          {agent ? (
            <>
              <SectionLabel collapsed={collapsed}>Assistant</SectionLabel>
              <Item to="/agent/chat" icon={MessageSquare} onNavigate={close} collapsed={collapsed}>
                Chat
              </Item>
              <Item to="/agent/sessions" icon={History} onNavigate={close} collapsed={collapsed}>
                Sessions
              </Item>
              <Item to="/agent/memory" icon={Brain} onNavigate={close} collapsed={collapsed}>
                Memory
              </Item>
              <Item to="/agent/assistants" icon={UserRound} onNavigate={close} collapsed={collapsed}>
                Assistants
              </Item>
            </>
          ) : null}

          <SectionLabel collapsed={collapsed}>Run</SectionLabel>
          <Item to="/tasks" end icon={ListTodo} onNavigate={close} collapsed={collapsed}>
            Tasks
          </Item>
          <Item to="/nodes" icon={Server} onNavigate={close} collapsed={collapsed}>
            Nodes
          </Item>

          <SectionLabel collapsed={collapsed}>Administer</SectionLabel>
          {agent ? (
            <>
              <Item to="/agent/playbooks" icon={Sparkles} onNavigate={close} collapsed={collapsed}>
                Playbooks
              </Item>
              <Item to="/agent/skills" icon={Puzzle} onNavigate={close} collapsed={collapsed}>
                Skills
              </Item>
              <Item to="/agent/mcp" icon={Plug} onNavigate={close} collapsed={collapsed}>
                MCP
              </Item>
              <Item to="/agent/channels" icon={Hash} onNavigate={close} collapsed={collapsed}>
                Channels
              </Item>
            </>
          ) : null}
          <Item to="/secrets" icon={KeyRound} onNavigate={close} collapsed={collapsed}>
            Secrets
          </Item>
          <Item to="/registries" icon={Container} onNavigate={close} collapsed={collapsed}>
            Registries
          </Item>
          {agent ? (
            <Item to="/usage" icon={Coins} onNavigate={close} collapsed={collapsed}>
              Usage
            </Item>
          ) : null}
          {showSettings ? (
            <Item to="/agent/settings" icon={Settings2} onNavigate={close} collapsed={collapsed}>
              Settings
            </Item>
          ) : null}
        </nav>

        <div className="hidden border-t border-border p-2 md:block">
          <button
            type="button"
            aria-pressed={collapsed}
            aria-controls="app-nav"
            aria-label={collapseLabel}
            title={collapseLabel}
            onClick={toggleCollapsed}
            className={cn(
              "flex min-h-11 w-full items-center gap-3 rounded-md border border-transparent px-3 text-base font-normal text-soft",
              "hover:border-border hover:bg-raised hover:text-fg",
              collapsed && "justify-center px-0",
            )}
          >
            <PanelLeftClose className={cn("size-[18px] shrink-0 stroke-[1.8]", collapsed && "rotate-180")} />
            <span className={cn(collapsed && "hidden")}>{collapseLabel}</span>
          </button>
        </div>

        <div className="mt-auto border-t border-border p-2">
          <AccountMenu who={who} collapsed={collapsed} />
        </div>
      </aside>
    </>
  );
}

function accountSubtitle(who: Viewer | undefined, label: string) {
  if (!who) return __PODIUM_VERSION__;
  if (who.kind === IdentityKind.USER) return who.login || label;
  return __PODIUM_VERSION__;
}

/**
 * AccountMenu is the sidebar footer: avatar, name, and Sign out for a Google session.
 */
function AccountMenu({ who, collapsed }: { who: Viewer | undefined; collapsed: boolean }) {
  const label = viewerLabel(who);
  const subtitle = accountSubtitle(who, label.text);
  const canSignOut = Boolean(who?.googleAuthEnabled && who.kind === IdentityKind.USER);
  const picture = who?.pictureUrl ? "/auth/picture" : "";

  const identity = (
    <>
      <Avatar
        src={picture}
        label={label.text}
        className="border-line-strong bg-raised text-fg"
      />
      <span className={cn("min-w-0 flex-1 text-left", collapsed && "md:hidden")}>
        <span className="block truncate text-base font-bold text-fg">{label.text}</span>
        <span className="block truncate text-sm font-normal text-muted">{subtitle}</span>
      </span>
    </>
  );

  if (!canSignOut) {
    return (
      <div
        className={cn("flex w-full items-center gap-3 rounded-md px-2 py-2", collapsed && "md:justify-center md:px-0")}
        title={label.title}
      >
        {identity}
      </div>
    );
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className={cn(
          "flex min-h-[52px] w-full items-center gap-3 rounded-md border border-transparent px-2 py-2 text-left",
          "hover:border-border hover:bg-raised",
          "data-[state=open]:border-border data-[state=open]:bg-raised",
          collapsed && "md:justify-center md:px-0",
        )}
        aria-label={label.text}
        title={label.title}
      >
        {identity}
        <ChevronDown className={cn("size-4 shrink-0 text-muted", collapsed && "md:hidden")} />
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="start" className="w-[min(300px,calc(100vw-2rem))]">
        <div className="px-3 py-2">
          <div className="text-base font-semibold text-fg">Account</div>
          <div className="mt-1 truncate text-sm text-soft">{label.text}</div>
          <div className="truncate text-xs text-muted">{subtitle}</div>
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
