import type { Task } from "../gen/podium/v1/task_pb";
import type { Entry } from "./entries";
import { isTerminal, toDate } from "./format";

export interface Phase {
  name: string;
  from: number;
  to: number;
  /** open is the phase the run is in now. */
  open: boolean;
  fill: string;
}

/** phasesOf reads the run's stages off the node's lifecycle events and the task's own times. */
export function phasesOf(task: Task, entries: readonly Entry[], now: number): Phase[] {
  const first = (kind: string) => entries.find((e) => e.kind === kind && e.at > 0)?.at;
  const created = toDate(task.createdAt)?.getTime();
  const provisioning = first("provisioning");
  const pulling = first("pulling");
  const started = first("started") ?? toDate(task.startedAt)?.getTime();
  const ended = first("exited") ?? first("finished") ?? toDate(task.finishedAt)?.getTime();
  const terminal = isTerminal(task.status);

  const marks: { name: string; at?: number; fill: string }[] = [
    { name: "Queued", at: created, fill: "bg-idle/35" },
    { name: "Provisioning", at: provisioning, fill: "bg-idle/60" },
    { name: "Pulling", at: pulling, fill: "bg-idle/85" },
    { name: "Running", at: started, fill: "bg-run" },
  ];
  const known = marks.filter((m): m is { name: string; at: number; fill: string } => m.at !== undefined);
  const end = ended ?? (terminal ? (known.at(-1)?.at ?? now) : now);
  return known.map((m, i) => {
    const next = known[i + 1]?.at;
    return {
      name: m.name,
      from: m.at,
      to: Math.max(m.at, next ?? end),
      open: next === undefined && ended === undefined && !terminal,
      fill: m.fill,
    };
  });
}
