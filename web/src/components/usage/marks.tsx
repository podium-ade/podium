import { Cpu, Repeat, Sparkles } from "lucide-react";
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

/**
 * The usage screen's marks: where the spend came from, who it was for, what model ran it and
 * what paid for it. Each is a mark beside its name in text, never the mark alone, so the
 * screen reads the same to someone who cannot tell the marks apart.
 */

/** SourceCell is an origin's mark and name. Housekeeping is the conductor's own spend. */
export function SourceCell({ origin, className }: { origin: string; className?: string }) {
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-2 text-xs text-soft", className)}>
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
      <span className={cn("inline-flex min-w-0 items-center gap-2 text-xs text-fg", className)} title={startedBy}>
        <Avatar name={startedBy} login={startedBy} />
        <span className="truncate">{label}</span>
      </span>
    );
  }
  const routine = routineOf(startedBy) !== "";
  return (
    <span
      className={cn("inline-flex min-w-0 items-center gap-2 text-xs text-muted", className)}
      title={routine ? `A system routine: ${label}` : NOT_A_PERSON[startedBy]?.hint}
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
      <span className="text-xs text-faint" title="Ran before the conductor recorded what it ran on">
        Unrecorded
      </span>
    );
  }
  const mark = backendMark(provider);
  return (
    <span className="inline-flex min-w-0 items-center gap-2" title={[providerLabel(provider), agent].filter(Boolean).join(" · ")}>
      {mark ? <BackendMark id={mark} className="size-3.5 text-soft" /> : null}
      <span className="min-w-0">
        <span className="block truncate font-mono text-xs text-fg">{model || "-"}</span>
        {effort ? <span className="block text-2xs text-faint">{effort} effort</span> : null}
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
}: {
  authKind: string;
  owner: string;
  account?: string;
  provider?: string;
}) {
  if (authKind === "") {
    return (
      <span className="text-xs text-faint" title="Ran before the conductor recorded which credential paid">
        Unrecorded
      </span>
    );
  }
  const sub = authKind === "subscription";
  return (
    <span className="min-w-0" title={[provider ? providerLabel(provider) : "", account].filter(Boolean).join(" · ")}>
      <span
        className={cn(
          "inline-flex items-center rounded-sm px-1.5 py-px text-2xs font-semibold",
          sub ? "bg-accent/10 text-accent" : "bg-raised text-soft",
        )}
      >
        {authLabel(authKind)}
      </span>
      <span className="mt-0.5 block truncate text-2xs text-faint">
        {ownerLabel(owner)}
        {account ? ` · ${account}` : ""}
      </span>
    </span>
  );
}
