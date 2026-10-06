import { useState } from "react";
import type { LucideIcon } from "lucide-react";
import {
  Brain,
  ChevronsUpDown,
  Coins,
  Container,
  Hash,
  History,
  KeyRound,
  ListTodo,
  LogOut,
  Menu,
  MessageSquare,
  Plug,
  Puzzle,
  Server,
  Settings2,
  Sparkles,
  UserRound,
  X,
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

function Item({
  to,
  end,
  icon: Icon,
  children,
  match,
  onNavigate,
}: {
  to: string;
  end?: boolean;
  icon: LucideIcon;
  children: string;
  match?: (pathname: string) => boolean;
  onNavigate?: () => void;
}) {
  const { pathname } = useLocation();
  const isActive = match ? match(pathname) : pathActive(pathname, to, end);
  return (
    <Link
      to={to}
      aria-current={isActive ? "page" : undefined}
      onClick={onNavigate}
      className={cn(
        "flex h-8 items-center gap-2.5 rounded-md px-2.5 text-sm transition-colors duration-150",
        "outline-none focus-visible:ring-2 focus-visible:ring-ring",
        isActive
          ? "bg-accent font-medium text-primary-foreground"
          : "text-muted hover:bg-sidebar-accent hover:text-sidebar-accent-foreground",
      )}
    >
      <Icon className="size-4 shrink-0" />
      {children}
    </Link>
  );
}

function SectionLabel({ children }: { children: string }) {
  return <p className="px-2.5 pt-3 pb-1 text-xs font-medium text-muted">{children}</p>;
}

function Wordmark() {
  return <span className="font-semibold tracking-tight text-fg">Podium</span>;
}

export function Header() {
  const who = useViewer();
  const { pathname } = useLocation();
  // The drawer stays open only for the path it was opened on, so a navigation closes it
  // without an effect writing state.
  const [openPath, setOpenPath] = useState<string | null>(null);
  const open = openPath === pathname;
  const agent = Boolean(who?.agentEnabled);
  // Users lives on Settings even when this control plane has no conductor and no Google
  // sign-in, so the link stays once WhoAmI has answered.
  const showSettings = Boolean(who);

  const close = () => setOpenPath(null);
  return (
    <>
      <div className="flex h-12 w-full shrink-0 items-center gap-2 border-b border-sidebar-border bg-sidebar px-3 md:hidden">
        <button
          type="button"
          aria-expanded={open}
          aria-controls="app-nav"
          onClick={() => setOpenPath(open ? null : pathname)}
          className="grid size-8 place-items-center rounded-md text-fg outline-none hover:bg-sidebar-accent focus-visible:ring-2 focus-visible:ring-ring"
        >
          {open ? <X className="size-4" /> : <Menu className="size-4" />}
          <span className="sr-only">{open ? "Close navigation" : "Open navigation"}</span>
        </button>
        <Wordmark />
      </div>
      {open ? (
        <button
          type="button"
          aria-label="Close navigation"
          className="fixed inset-x-0 bottom-0 top-12 z-30 bg-background/70 md:hidden"
          onClick={close}
        />
      ) : null}
      <aside
        id="app-nav"
        className={cn(
          "z-40 flex h-full w-60 shrink-0 flex-col border-r border-sidebar-border bg-sidebar",
          "max-md:fixed max-md:top-12 max-md:bottom-0 max-md:left-0 max-md:h-auto",
          open ? "max-md:flex" : "max-md:hidden",
        )}
      >
        <div className="flex h-14 items-center px-4">
          <Wordmark />
        </div>

        <nav aria-label="Primary" className="flex flex-1 flex-col gap-0.5 overflow-y-auto px-3 pb-2">
          {agent ? (
            <>
              <SectionLabel>Assistant</SectionLabel>
              <Item to="/agent/chat" icon={MessageSquare} onNavigate={close}>
                Chat
              </Item>
              <Item to="/agent/sessions" icon={History} onNavigate={close}>
                Sessions
              </Item>
              <Item to="/agent/memory" icon={Brain} onNavigate={close}>
                Memory
              </Item>
              <Item to="/agent/profile" icon={UserRound} onNavigate={close}>
                Assistant
              </Item>
            </>
          ) : null}

          <SectionLabel>Run</SectionLabel>
          <Item to="/tasks" end icon={ListTodo} onNavigate={close}>
            Tasks
          </Item>
          <Item to="/nodes" icon={Server} onNavigate={close}>
            Nodes
          </Item>

          <SectionLabel>Administer</SectionLabel>
          {agent ? (
            <>
              <Item to="/agent/playbooks" icon={Sparkles} onNavigate={close}>
                Playbooks
              </Item>
              <Item to="/agent/skills" icon={Puzzle} onNavigate={close}>
                Skills
              </Item>
              <Item to="/agent/mcp" icon={Plug} onNavigate={close}>
                MCP
              </Item>
              <Item to="/agent/channels" icon={Hash} onNavigate={close}>
                Channels
              </Item>
            </>
          ) : null}
          <Item to="/secrets" icon={KeyRound} onNavigate={close}>
            Secrets
          </Item>
          <Item to="/registries" icon={Container} onNavigate={close}>
            Registries
          </Item>
          {agent ? (
            <Item to="/usage" icon={Coins} onNavigate={close}>
              Usage
            </Item>
          ) : null}
          {showSettings ? (
            <Item to="/agent/settings" icon={Settings2} onNavigate={close}>
              Settings
            </Item>
          ) : null}
        </nav>

        <div className="mt-auto border-t border-sidebar-border p-2">
          <AccountMenu who={who} />
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
        <span className="block truncate text-2xs text-muted">{subtitle}</span>
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
          "outline-none transition-colors hover:bg-sidebar-accent",
          "focus-visible:ring-2 focus-visible:ring-ring",
          "data-[state=open]:bg-sidebar-accent",
        )}
        aria-label={label.text}
        title={label.title}
      >
        {identity}
        <ChevronsUpDown className="size-3.5 shrink-0 text-muted" />
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="start" className="w-56">
        <div className="flex items-center gap-2.5 px-2 py-2">
          <Avatar src={picture} label={label.text} size="md" />
          <div className="min-w-0">
            <div className="truncate text-sm font-medium text-fg">{label.text}</div>
            <div className="truncate text-2xs text-muted">{subtitle}</div>
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
