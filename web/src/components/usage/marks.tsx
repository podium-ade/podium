import { Cpu, Repeat, Sparkles } from "lucide-react";
import { useRef, useState, type ReactNode } from "react";
import {
  authLabel,
  backendMark,
  isPerson,
  markFor,
  NOT_A_PERSON,
  originLabel,
  ownerLabel,
  personLabel,
  providerLabel,
  routineOf,
} from "../../lib/usage";
import { cn } from "../../lib/utils";
import { BackendMark } from "../agent/BackendMark";
import { Avatar } from "../agent/chat/Origin";
import { OriginMark } from "../agent/OriginMark";
import { TooltipContent, TooltipProvider, TooltipRoot, TooltipTrigger } from "../ui/tooltip";

/**
 * The usage screen's marks: where the spend came from, who it was for, what model ran it and
 * what paid for it. Each is a mark beside its name in text, never the mark alone, so the
 * screen reads the same to someone who cannot tell the marks apart.
 */

/**
 * Clip holds a table cell's content to its column, so a long name ellipsizes there instead of
 * pushing into the next one. Hovering it shows the app's tooltip, but only when it has
 * something to add:
 *
 * - a mark's `data-hint`, which explains it (a routine, an unrecorded bucket, the provider
 *   behind a model), always;
 * - otherwise the full text, only when some of it is actually cut off: `data-full` where a
 *   mark names it (a person's whole login), or the cell's own text, line by line.
 *
 * Whether anything is cut off is measured as the pointer arrives, not guessed from lengths.
 */
export function Clip({ className, children }: { className?: string; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<string | null>(null);
  return (
    <TooltipProvider>
      <TooltipRoot open={tip !== null} onOpenChange={(open) => setTip(open ? tipFor(ref.current) : null)}>
        <TooltipTrigger asChild>
          <div ref={ref} className={cn("min-w-0", className)}>
            {children}
          </div>
        </TooltipTrigger>
        <TooltipContent className="whitespace-pre-line">{tip}</TooltipContent>
      </TooltipRoot>
    </TooltipProvider>
  );
}

/** tipFor is what hovering a Clip should say, or null for nothing. */
function tipFor(el: HTMLElement | null): string | null {
  if (!el) return null;
  const hint = el.querySelector<HTMLElement>("[data-hint]")?.dataset.hint;
  if (hint) return hint;
  const cut = [el, ...el.querySelectorAll<HTMLElement>("*")].some((e) => e.scrollWidth - e.clientWidth > 1);
  if (!cut) return null;
  const full = el.querySelector<HTMLElement>("[data-full]")?.dataset.full;
  return full || el.innerText.trim() || null;
}

/** SourceCell is an origin's mark and name. Housekeeping is the conductor's own spend. */
export function SourceCell({ origin, className }: { origin: string; className?: string }) {
  return (
    <span
      className={cn("inline-flex max-w-full min-w-0 items-center gap-2 text-xs text-soft", className)}
    >
      {origin === "housekeeping" ? (
        <span aria-hidden className="grid size-[22px] shrink-0 place-items-center rounded-sm bg-fg text-raised">
          <Sparkles className="size-[13px]" strokeWidth={2} />
        </span>
      ) : (
        <OriginMark origin={markFor(origin)} />
      )}
      <span className="truncate">{originLabel(origin)}</span>
    </span>
  );
}

/**
 * PersonCell is whose turn it was: a person's avatar and login, a routine by name, or the
 * stand-ins for nobody and the GitHub App, set in a quieter tone so they do not read as people.
 */
export function PersonCell({
  startedBy,
  routines,
  className,
}: {
  startedBy: string;
  routines?: Map<string, string>;
  className?: string;
}) {
  const label = personLabel(startedBy, routines);
  if (isPerson(startedBy)) {
    return (
      <span className={cn("inline-flex max-w-full min-w-0 items-center gap-2 text-xs text-fg", className)} data-full={startedBy}>
        <Avatar name={startedBy} login={startedBy} />
        <span className="truncate">{label}</span>
      </span>
    );
  }
  const routine = routineOf(startedBy) !== "";
  return (
    <span
      className={cn("inline-flex max-w-full min-w-0 items-center gap-2 text-xs text-muted", className)}
      data-hint={routine ? `A system routine: ${label}` : NOT_A_PERSON[startedBy]?.hint}
    >
      <span
        aria-hidden
        className="inline-grid size-6 shrink-0 place-items-center rounded-full border border-dashed border-line-strong text-faint"
      >
        {routine ? <Repeat className="size-3" /> : <Cpu className="size-3" />}
      </span>
      <span className="truncate">{label}</span>
    </span>
  );
}

/** ModelCell is the provider's mark, the model and, when set, the effort it ran at. */
export function ModelCell({
  provider,
  model,
  effort,
  agent,
}: {
  provider: string;
  model: string;
  effort?: string;
  agent?: string;
}) {
  if (provider === "" && model === "") {
    return (
      <span className="text-xs text-faint" data-hint="Ran before the conductor recorded what it ran on">
        Unrecorded
      </span>
    );
  }
  const mark = backendMark(provider);
  return (
    <span
      className="inline-flex max-w-full min-w-0 items-center gap-2"
      data-hint={[model, providerLabel(provider), agent].filter(Boolean).join(" · ")}
    >
      {mark ? <BackendMark id={mark} className="size-3.5 text-soft" /> : null}
      <span className="min-w-0">
        <span className="block truncate font-mono text-xs text-fg">{model || "-"}</span>
        {effort ? <span className="block truncate text-2xs text-faint">{effort} effort</span> : null}
      </span>
    </span>
  );
}

/**
 * PaidWith is what paid: a subscription or an API key, whose, and the account the provider
 * named. A subscription is marked in the accent so it is told from billed spend at a glance.
 */
export function PaidWith({
  authKind,
  owner,
  account,
  provider,
  compact,
}: {
  authKind: string;
  owner: string;
  account?: string;
  provider?: string;
  /** compact is the kind of credential alone, on one line; whose it was is in the hover. */
  compact?: boolean;
}) {
  if (authKind === "") {
    return (
      <span className="text-xs text-faint" data-hint="Ran before the conductor recorded which credential paid">
        Unrecorded
      </span>
    );
  }
  const sub = authKind === "subscription";
  return (
    <span
      className="block max-w-full min-w-0"
      data-hint={[authLabel(authKind), ownerLabel(owner), account, provider ? providerLabel(provider) : ""]
        .filter(Boolean)
        .join(" · ")}
    >
      <span
        className={cn(
          "inline-block max-w-full truncate rounded-sm px-1.5 py-px align-middle text-2xs font-semibold",
          sub ? "bg-accent/10 text-accent" : "bg-raised text-soft",
        )}
      >
        {authLabel(authKind)}
      </span>
      {compact ? null : (
        <span className="mt-0.5 block truncate text-2xs text-faint">
          {ownerLabel(owner)}
          {account ? ` · ${account}` : ""}
        </span>
      )}
    </span>
  );
}
