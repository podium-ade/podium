import { createContext, useContext, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { ChevronLeft, Menu, Moon, Sun, X } from "lucide-react";
import { Link } from "react-router";
import { cn } from "@/lib/utils";
import { useViewer } from "../lib/identity";
import { pageCategory } from "../lib/navigation";
import { readTheme, syncTheme, toggleTheme, watchSystemTheme, type Theme } from "../lib/theme";
import { NotificationBell } from "./NotificationBell";
import { useShell } from "./shell";

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

function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(() =>
    typeof document === "undefined" ? "light" : readTheme(),
  );
  useEffect(() => {
    syncTheme();
    return watchSystemTheme(setTheme);
  }, []);
  const dark = theme === "dark";
  return (
    <button
      type="button"
      aria-pressed={dark}
      aria-label={dark ? "Switch to light theme" : "Switch to dark theme"}
      onClick={() => setTheme(toggleTheme())}
      className={iconButton}
    >
      {dark ? <Sun className="size-[18px] stroke-[1.8]" /> : <Moon className="size-[18px] stroke-[1.8]" />}
    </button>
  );
}

/**
 * PageFrame is the product top bar: the page title, its group, its actions, the theme
 * switch, and the notifications bell, on one 64px row. The body scrolls underneath,
 * except a bleed page (the chat), which keeps the height that remains.
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
  return (
    <PageActionsContext.Provider value={slot}>
      <div className="flex h-full min-h-0 flex-col bg-background">
        <header className="flex h-16 shrink-0 items-center gap-3 border-b border-border bg-panel px-4 sm:px-5">
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
              className="inline-flex shrink-0 items-center gap-1 rounded-md text-sm font-semibold text-soft hover:text-fg"
            >
              <ChevronLeft className="size-4" />
              {back.label}
            </Link>
          ) : null}
          <h1 className="min-w-0 truncate text-lg font-bold tracking-tight text-fg">{title}</h1>
          {category ? (
            <span className="hidden shrink-0 border-l border-border pl-3 text-sm text-muted sm:inline">
              {category}
            </span>
          ) : null}
          {meta ? <div className="flex shrink-0 items-center gap-2">{meta}</div> : null}
          <div className="ml-auto flex shrink-0 items-center gap-2">
            {actions}
            <div ref={setSlot} className="flex items-center gap-2 empty:hidden" />
            <ThemeToggle />
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
              {lead ? <p className="max-w-[68ch] text-soft">{lead}</p> : null}
              {children}
            </div>
          )}
        </div>
      </div>
    </PageActionsContext.Provider>
  );
}
