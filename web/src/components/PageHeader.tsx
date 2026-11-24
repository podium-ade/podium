import { createContext, useContext, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { ChevronLeft, Menu, X } from "lucide-react";
import { Link } from "react-router";
import { cn } from "@/lib/utils";
import { useViewer } from "../lib/identity";
import { pageCategory } from "../lib/navigation";
import { NotificationBell } from "./NotificationBell";
import { useShell } from "./shell";
import { ThemeToggle } from "./ThemeToggle";

const PageActionsContext = createContext<HTMLDivElement | null>(null);

const iconButton =
  "grid size-11 shrink-0 place-items-center rounded-md border border-border bg-panel text-fg hover:bg-raised";

/**
 * PageActions renders into the top bar's action slot. A panel that owns the button's state
 * can sit below the bar and still place its action there, without a second header.
 */
export function PageActions({ children }: { children: ReactNode }) {
  const slot = useContext(PageActionsContext);
  if (!slot) return null;
  return createPortal(children, slot);
}

/**
 * PageFrame is the product top bar: the page title, its group, its actions, the theme
 * switch, and the notifications bell, on one 64px row. The body scrolls underneath,
 * except a bleed page (the chat), which keeps the height that remains.
 *
 * A narrow pane (a phone, or a tablet beside the sidebar) has no room for all of that on one
 * row: the title would be squeezed to nothing and the actions pushed off the edge. There the
 * bar keeps the title and the bell on top and drops the state and the actions to a second
 * row, and the theme switch lives in the navigation drawer instead.
 */
export function PageFrame({
  title,
  actions,
  meta,
  lead,
  back,
  bleed,
  bodyClassName,
  children,
}: {
  title: ReactNode;
  actions?: ReactNode;
  /** One line of state, such as a status badge. Not a description or a count row. */
  meta?: ReactNode;
  /**
   * One or two plain sentences under the top bar saying what this page is for. The top bar
   * already names the page, so a page draws no heading of its own above this.
   */
  lead?: ReactNode;
  back?: { to: string; label: string };
  /** The body is the whole remaining pane, with no gutter. Used by the chat. */
  bleed?: boolean;
  bodyClassName?: string;
  children: ReactNode;
}) {
  const [slot, setSlot] = useState<HTMLDivElement | null>(null);
  const who = useViewer();
  const { navOpen, setNavOpen } = useShell();
  const category = pageCategory(
    typeof window.location?.pathname === "string" ? window.location.pathname : "",
  );
  // Whether the second row has anything in it. It holds the actions holder and the slot
  // holder whatever happens, so it is filled once it holds a third element: something a
  // page rendered, here or through PageActions.
  const tools = useRef<HTMLDivElement>(null);
  const [toolsFilled, setToolsFilled] = useState(meta != null || actions != null);
  useLayoutEffect(() => {
    const el = tools.current;
    if (!el) return;
    const sync = () => setToolsFilled(el.querySelectorAll("*").length > 2);
    sync();
    const mo = new MutationObserver(sync);
    mo.observe(el, { childList: true, subtree: true });
    return () => mo.disconnect();
  }, []);
  return (
    <PageActionsContext.Provider value={slot}>
      <div className="@container flex h-full min-h-0 flex-col bg-background">
        <header
          className={cn(
            "flex min-h-16 shrink-0 flex-wrap items-center gap-x-3 gap-y-2 border-b border-border bg-panel px-4 py-2.5",
            "@2xl:h-16 @2xl:flex-nowrap @2xl:px-5 @2xl:py-0",
          )}
        >
          <button
            type="button"
            className={cn(iconButton, "md:hidden")}
            aria-expanded={navOpen}
            aria-controls="app-nav"
            aria-label={navOpen ? "Close application navigation" : "Open application navigation"}
            onClick={() => setNavOpen(!navOpen)}
          >
            {navOpen ? <X className="size-[18px] stroke-[1.8]" /> : <Menu className="size-[18px] stroke-[1.8]" />}
          </button>
          {back ? (
            <Link
              to={back.to}
              className="-mx-1 inline-flex min-h-11 shrink-0 items-center gap-1 rounded-md px-1 text-sm font-semibold text-soft hover:text-fg"
            >
              <ChevronLeft className="size-4" />
              {/* A narrow bar keeps the chevron and gives the title the room; the name is
                  still the link's accessible name. */}
              <span className="sr-only @xl:not-sr-only">{back.label}</span>
            </Link>
          ) : null}
          {/* On a narrow bar the title takes what the first row leaves (a zero basis, so a long
              one truncates instead of wrapping the row); on a wide one it is its own width. */}
          <h1 className="min-w-0 flex-1 truncate text-lg font-bold tracking-tight text-fg @2xl:flex-initial">
            {title}
          </h1>
          {category ? (
            <span className="hidden shrink-0 border-l border-border pl-3 text-sm text-muted @2xl:inline">
              {category}
            </span>
          ) : null}
          {/* The second row on a narrow bar. On a wide one it dissolves (contents) and its
              pieces sit in the one row, the state beside the title and the actions at the end. */}
          <div
            ref={tools}
            className={cn(
              toolsFilled ? "flex" : "hidden",
              "order-last w-full min-w-0 items-center justify-end gap-2 @2xl:order-none @2xl:contents",
            )}
          >
            {meta ? (
              <div className="mr-auto flex min-w-0 items-center gap-2 overflow-hidden @2xl:mr-0 @2xl:shrink-0 @2xl:overflow-visible">
                {meta}
              </div>
            ) : null}
            <div className="flex min-w-0 items-center gap-2 overflow-x-auto @2xl:ml-auto @2xl:shrink-0 @2xl:overflow-visible">
              {actions}
              <div ref={setSlot} className="flex items-center gap-2 empty:hidden" />
            </div>
          </div>
          <div className="ml-auto flex shrink-0 items-center gap-2 @2xl:ml-0">
            <ThemeToggle className="hidden md:grid" />
            {who?.agentEnabled ? <NotificationBell /> : null}
          </div>
        </header>
        <div
          className={cn(
            "relative min-h-0 flex-1",
            bleed ? "flex h-full flex-col overflow-hidden" : "overflow-y-auto",
          )}
        >
          {bleed ? (
            children
          ) : (
            <div className={cn("w-full px-4 py-6 sm:px-6", bodyClassName ?? "space-y-5")}>
              {lead ? <p className="text-soft">{lead}</p> : null}
              {children}
            </div>
          )}
        </div>
      </div>
    </PageActionsContext.Provider>
  );
}
