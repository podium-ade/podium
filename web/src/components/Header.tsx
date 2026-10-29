import {
  useEffect,
  useEffectEvent,
  useRef,
  useState,
  type KeyboardEvent as KeyboardEvent_,
  type ReactNode,
} from "react";
import { flushSync } from "react-dom";
import { ChevronDown, LogOut, UserRound } from "lucide-react";
import { Link, useLocation, useNavigate } from "react-router";
import { cn } from "@/lib/utils";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { useViewer, viewerLabel, type Viewer } from "../lib/identity";
import { Avatar } from "./Avatar";
import {
  groupOf,
  navGroupsFor,
  pageActive,
  pageMatches,
  type NavGroup,
  type NavPage,
} from "../lib/navigation";
import { CollapseIcon, NavIcon, SearchIcon } from "./NavIcon";
import { useShell } from "./shell";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "./ui/dropdown-menu";

const GROUPS_KEY = "podium-relay-nav-groups";

function readGroups(): Record<string, boolean> {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(GROUPS_KEY) ?? "{}");
    return parsed && typeof parsed === "object" ? (parsed as Record<string, boolean>) : {};
  } catch {
    return {};
  }
}

function writeGroups(groups: Record<string, boolean>) {
  try {
    localStorage.setItem(GROUPS_KEY, JSON.stringify(groups));
  } catch {
    // The folder still opens or closes for this visit.
  }
}

/** The label with the first hit of the search underlined, as the design's `.nav-hit`. */
function Highlight({ text, query }: { text: string; query: string }) {
  const at = query ? text.toLowerCase().indexOf(query) : -1;
  if (at < 0) return text;
  return (
    <>
      {text.slice(0, at)}
      <mark className="bg-transparent font-bold text-inherit underline decoration-2 underline-offset-[3px]">
        {text.slice(at, at + query.length)}
      </mark>
      {text.slice(at + query.length)}
    </>
  );
}

function Item({
  page,
  query,
  onNavigate,
  collapsed,
}: {
  page: NavPage;
  query: string;
  onNavigate: () => void;
  collapsed: boolean;
}) {
  const { pathname } = useLocation();
  const isActive = pageActive(page, pathname);
  return (
    <Link
      to={page.to}
      data-nav-item=""
      aria-current={isActive ? "page" : undefined}
      aria-label={page.label}
      title={page.label}
      onClick={onNavigate}
      className={cn(
        "flex min-h-11 items-center gap-3 rounded-md border border-transparent px-3 text-left text-base font-normal text-soft",
        "hover:border-border hover:bg-raised hover:text-fg",
        collapsed && "md:justify-center md:px-0",
        isActive && "border-line-strong bg-raised font-[720] text-fg hover:border-line-strong hover:bg-raised hover:text-fg",
      )}
    >
      <NavIcon name={page.icon} />
      <span className={cn("truncate", collapsed && "md:hidden")}>
        <Highlight text={page.label} query={query} />
      </span>
    </Link>
  );
}

/**
 * Folder is one sidebar group. Its open state is the operator's and persists; a search
 * opens every group that has a hit. A collapsed sidebar drops the headings and shows every
 * icon, with a rule between groups.
 */
function Folder({
  group,
  open,
  onToggle,
  collapsed,
  children,
}: {
  group: NavGroup;
  open: boolean;
  onToggle: () => void;
  collapsed: boolean;
  children: ReactNode;
}) {
  const id = `nav-group-${group.id}`;
  // A collapsed sidebar shows every icon, but the drawer on a narrow window is never
  // collapsed, so a closed group stays closed there.
  const shown = open || collapsed;
  // The group slides open by its grid row. It clips while it moves, then lets go once open
  // so a focus ring on its first or last link is not cut off.
  const [settled, setSettled] = useState(shown);
  if (!shown && settled) setSettled(false);
  return (
    <div
      data-nav-group={group.id}
      className={cn(
        "mt-1 first:mt-0",
        collapsed && "md:mt-2 md:border-t md:border-border md:pt-2 md:first:mt-0 md:first:border-t-0 md:first:pt-0",
      )}
    >
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={onToggle}
        className={cn(
          "flex min-h-11 w-full items-center justify-between gap-2 rounded-md border border-transparent px-3 text-left text-xs font-bold tracking-[0.08em] text-muted uppercase md:min-h-10",
          "hover:border-border hover:bg-raised hover:text-fg",
          collapsed && "md:hidden",
        )}
      >
        <span>{group.label}</span>
        <ChevronDown
          aria-hidden
          className={cn(
            "size-4 shrink-0 stroke-[1.8] transition-transform duration-150 ease-out motion-reduce:transition-none",
            !open && "-rotate-90",
          )}
        />
      </button>
      <div
        id={id}
        role="group"
        aria-label={group.label}
        // Visibility rides the transition, so a closing group stays painted until it has
        // folded away and its links leave the tab order once it has.
        style={shown ? undefined : { visibility: "hidden" }}
        onTransitionEnd={(e) => {
          if (e.target === e.currentTarget && e.propertyName === "grid-template-rows" && shown) setSettled(true);
        }}
        className={cn(
          "grid transition-[grid-template-rows,visibility] duration-200 ease-out",
          shown ? "grid-rows-[1fr]" : "grid-rows-[0fr]",
          collapsed && !open && "max-md:hidden",
        )}
      >
        <div className={cn("flex min-h-0 flex-col gap-0.5", !settled && "overflow-hidden")}>{children}</div>
      </div>
    </div>
  );
}

const shortcutLabel =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || "") ? "⌘K" : "Ctrl K";

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
  const navigate = useNavigate();
  const { navOpen, setNavOpen, collapsed, toggleCollapsed } = useShell();
  const [query, setQueryValue] = useState("");
  // Groups folded by hand during the current search. A search opens every group with a hit;
  // folding one of those is for this search only, so the remembered folds stay as they were.
  const [searchFolded, setSearchFolded] = useState<ReadonlySet<string>>(() => new Set());
  const setQuery = (value: string) => {
    setQueryValue(value);
    setSearchFolded(new Set());
  };
  const [groupsOpen, setGroupsOpen] = useState(readGroups);
  const inputRef = useRef<HTMLInputElement>(null);
  const navRef = useRef<HTMLElement>(null);

  // A navigation closes the drawer and opens the group that holds the new page. Adjusting
  // during render keeps both in the same pass as the route change, so neither lingers.
  const [seenPath, setSeenPath] = useState<string | null>(null);
  if (seenPath !== pathname) {
    setSeenPath(pathname);
    if (seenPath !== null && navOpen) setNavOpen(false);
    const group = groupOf(pathname);
    if (group && groupsOpen[group] === false) {
      const next = { ...groupsOpen, [group]: true };
      setGroupsOpen(next);
      writeGroups(next);
    }
  }

  const q = query.trim().toLowerCase();
  const groups = navGroupsFor(who)
    .map((g) => ({ ...g, pages: g.pages.filter((p) => pageMatches(p, q)) }))
    .filter((g) => g.pages.length > 0);
  const firstHit = groups[0]?.pages[0];

  const toggleGroup = (id: string) => {
    if (q) {
      const next = new Set(searchFolded);
      if (!next.delete(id)) next.add(id);
      setSearchFolded(next);
      return;
    }
    const next = { ...groupsOpen, [id]: groupsOpen[id] === false };
    setGroupsOpen(next);
    writeGroups(next);
  };

  const close = () => setNavOpen(false);
  const navigated = () => {
    setQuery("");
    close();
  };

  // Ctrl K (⌘K on a Mac) searches the pages from anywhere: it widens a collapsed sidebar,
  // or opens the drawer on a narrow window, then puts the caret in the field.
  const openSearch = () => {
    const narrow = window.matchMedia?.("(max-width: 767px)").matches ?? false;
    // The field has to be on screen before it can take focus, and focus has to move now:
    // a frame later, the first letters typed after the shortcut land in whatever had it.
    flushSync(() => {
      if (!narrow && collapsed) toggleCollapsed();
      if (narrow && !navOpen) setNavOpen(true);
    });
    inputRef.current?.focus();
    inputRef.current?.select();
  };
  const onShortcut = useEffectEvent(openSearch);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "k") {
        event.preventDefault();
        onShortcut();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  const visibleItems = () =>
    Array.from(navRef.current?.querySelectorAll<HTMLElement>("[data-nav-item]") ?? []).filter(
      (el) => !el.closest("[hidden]") && (typeof el.checkVisibility !== "function" || el.checkVisibility()),
    );

  const onSearchKey = (event: KeyboardEvent_<HTMLInputElement>) => {
    if (event.key === "Escape" && query) {
      event.preventDefault();
      event.stopPropagation();
      setQuery("");
    } else if (event.key === "ArrowDown") {
      const first = visibleItems()[0];
      if (first) {
        event.preventDefault();
        first.focus();
      }
    } else if (event.key === "Enter" && q && firstHit) {
      event.preventDefault();
      void navigate(firstHit.to);
      navigated();
    }
  };

  const onListKey = (event: KeyboardEvent_<HTMLElement>) => {
    const items = visibleItems();
    const at = items.indexOf(document.activeElement as HTMLElement);
    if (at < 0) return;
    if (event.key === "Escape" && query) {
      event.preventDefault();
      event.stopPropagation();
      setQuery("");
      inputRef.current?.focus();
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    event.preventDefault();
    if (event.key === "ArrowUp" && at === 0) {
      inputRef.current?.focus();
      return;
    }
    items[Math.min(items.length - 1, Math.max(0, at + (event.key === "ArrowDown" ? 1 : -1)))]?.focus();
  };

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

        {who ? (
          <div role="search" className="px-2 pt-3">
            <div className={cn("relative", collapsed && "md:hidden")}>
              <SearchIcon className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted" />
              <label className="sr-only" htmlFor="nav-search">
                Search pages
              </label>
              <input
                ref={inputRef}
                id="nav-search"
                type="search"
                placeholder="Search"
                autoComplete="off"
                spellCheck={false}
                aria-controls="app-nav-list"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={onSearchKey}
                className={cn(
                  "peer block min-h-11 w-full rounded-md border border-border bg-background pr-16 pl-10 text-sm text-fg placeholder:text-muted",
                  "hover:border-line-strong focus-visible:border-line-strong focus-visible:outline-3 focus-visible:outline-offset-2 focus-visible:outline-ring",
                  "[&::-webkit-search-cancel-button]:appearance-none",
                )}
              />
              <kbd
                aria-hidden
                className="pointer-events-none absolute top-1/2 right-2.5 hidden -translate-y-1/2 rounded-sm md:block border border-border bg-raised px-1.5 font-mono text-xs leading-5 font-semibold whitespace-nowrap text-muted peer-[:not(:placeholder-shown)]:hidden"
              >
                {shortcutLabel}
              </kbd>
            </div>
            <button
              type="button"
              aria-label="Search pages"
              title="Search pages"
              onClick={openSearch}
              className={cn(
                "hidden min-h-11 w-full place-items-center rounded-md border border-transparent text-soft hover:border-border hover:bg-raised hover:text-fg",
                collapsed && "md:grid",
              )}
            >
              <SearchIcon />
            </button>
          </div>
        ) : null}

        <nav
          ref={navRef}
          id="app-nav-list"
          aria-label="Primary"
          onKeyDown={onListKey}
          className="flex min-h-0 flex-1 flex-col overflow-y-auto px-2 py-3"
        >
          {groups.map((group) => (
            <Folder
              key={group.id}
              group={group}
              open={q ? !searchFolded.has(group.id) : groupsOpen[group.id] !== false}
              onToggle={() => toggleGroup(group.id)}
              collapsed={collapsed}
            >
              {group.pages.map((page) => (
                <Item key={page.id} page={page} query={q} onNavigate={navigated} collapsed={collapsed} />
              ))}
            </Folder>
          ))}
          {q && groups.length === 0 ? (
            <div role="status" className="mx-3 my-2 grid justify-items-start gap-2 text-sm text-soft">
              <p className="[overflow-wrap:anywhere]">No pages match “{query.trim()}”.</p>
              <button
                type="button"
                onClick={() => {
                  setQuery("");
                  inputRef.current?.focus();
                }}
                className="min-h-11 rounded-md border border-line-strong bg-background px-3 font-semibold text-fg hover:bg-raised"
              >
                Clear search
              </button>
            </div>
          ) : null}
        </nav>

        <div className="hidden border-t border-border p-2 md:block">
          <button
            type="button"
            aria-pressed={collapsed}
            aria-controls="app-nav"
            aria-label={collapseLabel}
            title={collapseLabel}
            onClick={() => {
              setQuery("");
              toggleCollapsed();
            }}
            className={cn(
              "flex min-h-11 w-full items-center gap-3 rounded-md border border-transparent px-3 text-base font-normal text-soft",
              "hover:border-border hover:bg-raised hover:text-fg",
              collapsed && "justify-center px-0",
            )}
          >
            <CollapseIcon className={cn(collapsed && "rotate-180")} />
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
 * AccountMenu is the sidebar footer: avatar, name, and for a Google session the way to
 * your account and Sign out. Your account is personal, so it is here and not a sidebar page.
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
          <Link to="/agent/settings/account">
            <UserRound />
            Your account
          </Link>
        </DropdownMenuItem>
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
