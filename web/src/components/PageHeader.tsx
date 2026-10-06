import { createContext, useContext, useState } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { ChevronLeft } from "lucide-react";
import { Link } from "react-router";
import { cn } from "@/lib/utils";

const PageActionsContext = createContext<HTMLDivElement | null>(null);

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
 * PageFrame is a fixed 3rem toolbar: the page title, its current state, and its actions,
 * on one row. The side rail is the map. The body scrolls underneath, except a bleed page
 * (the chat), which keeps the height that remains.
 */
export function PageFrame({
  title,
  actions,
  meta,
  back,
  bleed,
  bodyClassName,
  children,
}: {
  title: ReactNode;
  actions?: ReactNode;
  /** One line of state, such as a status badge. Not a description or a count row. */
  meta?: ReactNode;
  back?: { to: string; label: string };
  /** The body is the whole remaining pane, with no gutter. Used by the chat. */
  bleed?: boolean;
  bodyClassName?: string;
  children: ReactNode;
}) {
  const [slot, setSlot] = useState<HTMLDivElement | null>(null);
  return (
    <PageActionsContext.Provider value={slot}>
      <div className="flex h-full min-h-0 flex-col">
        <header className="flex h-12 shrink-0 items-center gap-3 border-b border-border bg-background px-4">
          {back ? (
            <Link
              to={back.to}
              className="inline-flex shrink-0 items-center gap-1 rounded-md text-xs text-muted transition-colors hover:text-fg focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
            >
              <ChevronLeft className="size-3.5" />
              {back.label}
            </Link>
          ) : null}
          <h1 className="min-w-0 truncate text-sm font-semibold tracking-tight text-fg">{title}</h1>
          {meta ? <div className="flex shrink-0 items-center gap-2">{meta}</div> : null}
          <div className="ml-auto flex shrink-0 items-center gap-2">
            {actions}
            <div ref={setSlot} className="flex items-center gap-2 empty:hidden" />
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
            <div
              className={cn(
                "mx-auto w-full max-w-7xl animate-in px-6 py-6 fade-in-0 duration-200 lg:px-8",
                bodyClassName ?? "space-y-5",
              )}
            >
              {children}
            </div>
          )}
        </div>
      </div>
    </PageActionsContext.Provider>
  );
}
