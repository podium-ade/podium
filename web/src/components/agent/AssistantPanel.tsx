import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Plus } from "lucide-react";
import { Link } from "react-router";
import { cn } from "@/lib/utils";
import { PodiumMark } from "../PodiumMark";
import { Label } from "../ui/label";

/**
 * The Assistants screen is drawn in the same parts as MCP, Secrets and Models: one bordered
 * panel on the page, a header naming what is open, a tab strip, a raised bar that says what
 * the open tab is for, field rows, and a bar along the bottom that holds Save.
 *
 * The parts live here so the built-in assistant and a personal one cannot drift apart.
 */

export type AssistantTab = {
  id: string;
  label: string;
  icon: LucideIcon;
  /** A dot when this tab has edits that are not saved yet. */
  unsaved?: boolean;
};

/** The panel itself. Everything an assistant is sits inside one surface. */
export function AssistantPanel({ children, label }: { children: ReactNode; label: string }) {
  return (
    <section
      aria-label={label}
      // clip, not hidden: hidden would make the panel a scroll container and stop the
      // save bar sticking to the bottom of the page.
      className="min-w-0 overflow-clip rounded-lg border border-border bg-panel"
    >
      {children}
    </section>
  );
}

const TONES = [
  "bg-run-bg text-run",
  "bg-ok-bg text-ok",
  "bg-lost-bg text-lost",
  "bg-warn-bg text-warn",
] as const;

/** The same name always lands on the same tone, so a voice is recognisable before it is read. */
function toneFor(seed: string): string {
  let h = 0;
  for (const ch of seed) h = (h * 31 + (ch.codePointAt(0) ?? 0)) >>> 0;
  return TONES[h % TONES.length];
}

function initials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return "";
  if (words.length === 1) return [...words[0]].slice(0, 2).join("").toUpperCase();
  return ([...words[0]][0] + [...words[words.length - 1]][0]).toUpperCase();
}

const TILE = {
  row: "size-10 text-sm",
  detail: "size-14 text-lg",
} as const;

/**
 * AssistantTile is the square an assistant is drawn with: the product mark for Podium, the
 * initials of a personal voice on a tone of its own, and a plus for one not saved yet. The
 * name is always written beside it, so the tile has no label.
 */
export function AssistantTile({
  kind,
  name = "",
  seed = "",
  size = "row",
}: {
  kind: "podium" | "personal" | "new";
  name?: string;
  seed?: string;
  size?: keyof typeof TILE;
}) {
  if (kind === "podium") {
    return <PodiumMark className={cn("rounded-sm", size === "row" ? "size-10" : "size-14")} />;
  }
  const letters = initials(name);
  return (
    <span
      aria-hidden
      className={cn(
        "grid shrink-0 place-items-center rounded-sm border font-bold tracking-tight select-none",
        TILE[size],
        kind === "new" || !letters
          ? "border-dashed border-line-strong bg-raised text-soft"
          : cn("border-transparent", toneFor(seed || name)),
      )}
    >
      {kind === "new" || !letters ? <Plus className={size === "row" ? "size-4" : "size-5"} /> : letters}
    </span>
  );
}

/** The top of the panel: who is open, what it runs on, and what can be done to it. */
export function PanelHeader({
  tile,
  title,
  chips,
  subtitle,
  actions,
}: {
  tile: ReactNode;
  title: string;
  chips?: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="flex flex-col gap-4 px-6 pt-6 pb-5 sm:flex-row sm:items-center">
      <div className="flex min-w-0 flex-1 items-center gap-4">
        {tile}
        <div className="min-w-0 space-y-1.5">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
            <h2 className="min-w-0 truncate text-xl font-bold tracking-tight text-fg" title={title}>
              {title}
            </h2>
            {chips}
          </div>
          {subtitle ? <div className="flex min-w-0 items-center gap-2 text-sm text-muted">{subtitle}</div> : null}
        </div>
      </div>
      {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
    </header>
  );
}

/**
 * SectionTabs is the strip of an assistant's sections. They look like every other tab
 * strip in the product, but each is a link, so back and a deep link land on the section.
 */
export function SectionTabs({
  tabs,
  current,
  href,
  label = "Assistant",
}: {
  tabs: AssistantTab[];
  current: string;
  href: (id: string) => string;
  label?: string;
}) {
  return (
    <nav aria-label={label} className="border-b border-border px-4">
      <ul className="-mb-px flex items-center gap-1 overflow-x-auto [scrollbar-width:none]">
        {tabs.map((tab) => {
          const open = tab.id === current;
          return (
            <li key={tab.id} className="shrink-0">
              <Link
                to={href(tab.id)}
                aria-current={open ? "page" : undefined}
                className={cn(
                  "inline-flex min-h-12 items-center gap-2 border-b-[3px] px-3.5 text-[15px] font-semibold whitespace-nowrap",
                  "transition-colors duration-150 ease-out outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset",
                  open
                    ? "border-accent text-fg"
                    : "border-transparent text-soft hover:bg-raised hover:text-fg",
                )}
              >
                <tab.icon aria-hidden className="size-4 shrink-0 stroke-[1.8]" />
                {tab.label}
                {tab.unsaved ? (
                  <>
                    <span aria-hidden className="size-1.5 rounded-full bg-accent" />
                    <span className="sr-only">, unsaved</span>
                  </>
                ) : null}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

/** The raised bar under the tabs: one or two sentences on what the open section is for. */
export function ScopeBar({ children, aside }: { children: ReactNode; aside?: ReactNode }) {
  return (
    <div className="grid grid-cols-1 items-center gap-3 border-b border-border bg-raised px-6 py-4 sm:grid-cols-[minmax(0,1fr)_auto]">
      <p className="m-0 max-w-[72ch] text-soft">{children}</p>
      {aside ? <div className="justify-self-start sm:justify-self-end">{aside}</div> : null}
    </div>
  );
}

/** The field rows of the open section. */
export function PanelBody({ children, hidden }: { children: ReactNode; hidden?: boolean }) {
  return (
    <div hidden={hidden} className="divide-y divide-border px-6">
      {children}
    </div>
  );
}

/** One field: its name and what it is for on the left, the control on the right. */
export function Row({
  id,
  label,
  hint,
  children,
}: {
  id?: string;
  label: string;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-x-10 gap-y-3 py-6 md:grid-cols-[15rem_minmax(0,1fr)]">
      <div className="space-y-1">
        {id ? (
          <Label htmlFor={id} className="text-base font-bold text-fg">
            {label}
          </Label>
        ) : (
          <p className="text-base font-bold text-fg">{label}</p>
        )}
        {hint ? <p className="text-sm leading-relaxed text-muted">{hint}</p> : null}
      </div>
      <div className="min-w-0 space-y-2.5">{children}</div>
    </div>
  );
}

export function Issue({ children }: { children: ReactNode }) {
  return <p className="text-sm font-semibold text-err">{children}</p>;
}

/**
 * SaveBar is the bottom of the panel. It says what is waiting to be saved, so an edit made
 * on another tab is not forgotten, and holds Save. It stays in view while the panel scrolls.
 */
export function SaveBar({
  status,
  dirty,
  children,
}: {
  status: string;
  dirty: boolean;
  children: ReactNode;
}) {
  return (
    <div className="sticky bottom-0 z-10 flex items-center gap-4 border-t border-border bg-raised px-4 py-3.5 sm:px-6">
      <p
        aria-live="polite"
        className={cn("m-0 flex min-w-0 flex-1 items-center gap-2 text-sm", dirty ? "font-semibold text-fg" : "text-muted")}
      >
        {dirty ? <span aria-hidden className="size-2 shrink-0 rounded-full bg-accent" /> : null}
        {status}
      </p>
      <div className="flex shrink-0 flex-wrap items-center gap-2">{children}</div>
    </div>
  );
}
