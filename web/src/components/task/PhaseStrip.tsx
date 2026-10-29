import { useEffect, useMemo, useState } from "react";
import { ArrowRight } from "lucide-react";
import type { Task } from "../../gen/podium/v1/task_pb";
import type { Entry } from "../../lib/entries";
import { durationMs, isTerminal } from "../../lib/format";
import { phasesOf } from "../../lib/phases";
import { cn } from "../../lib/utils";
import { Chip } from "../Badge";

function useNow(ticking: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!ticking) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [ticking]);
  return now;
}

/**
 * PhaseStrip is where a run spent its time: waiting for a node, provisioning, pulling the
 * image, running — one bar, each stage as wide as it took, and how it ended. The node's
 * lifecycle events are only worth this much of the page.
 */
export function PhaseStrip({ task, entries }: { task: Task; entries: readonly Entry[] }) {
  const terminal = isTerminal(task.status);
  const now = useNow(!terminal);
  const phases = useMemo(() => phasesOf(task, entries, now), [task, entries, now]);
  const extras = useMemo(() => entries.filter((e) => e.kind === "sandbox" || e.kind === "preview"), [entries]);
  if (phases.length === 0) return null;

  const total = Math.max(1, phases.reduce((n, p) => n + (p.to - p.from), 0));
  const exit = task.exitCode;

  return (
    <div data-testid="phase-strip" className="space-y-2 rounded-xl border border-border bg-panel px-4 py-3">
      <div aria-hidden className="flex h-1.5 gap-0.5 overflow-hidden rounded-full">
        {phases.map((p) => (
          <span
            key={p.name}
            style={{ flexGrow: Math.max(p.to - p.from, total * 0.015) }}
            className={cn("rounded-full", p.fill, p.open && "animate-pulse")}
          />
        ))}
        {exit !== undefined ? (
          <span className={cn("w-1.5 shrink-0 rounded-full", exit === 0 ? "bg-ok" : "bg-err")} />
        ) : null}
      </div>
      <ol className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
        {phases.map((p, i) => (
          <li key={p.name} className="flex items-center gap-2">
            {i > 0 ? <ArrowRight aria-hidden className="size-3 text-faint" /> : null}
            <span className={cn("size-2 rounded-[2px]", p.fill)} aria-hidden />
            <span className={p.open ? "text-fg" : "text-muted"}>{p.name}</span>
            <span className="tabular text-fg">{durationMs(p.to - p.from)}</span>
          </li>
        ))}
        {exit !== undefined ? (
          <li className="flex items-center gap-2">
            <ArrowRight aria-hidden className="size-3 text-faint" />
            <span className={cn("tabular font-medium", exit === 0 ? "text-ok" : "text-err")}>exit {exit}</span>
          </li>
        ) : null}
        {extras.length > 0 ? (
          <li className="ml-auto flex flex-wrap gap-1">
            {extras.map((e) => (
              <Chip key={e.key} className="font-mono">
                {e.summary}
              </Chip>
            ))}
          </li>
        ) : null}
      </ol>
    </div>
  );
}
