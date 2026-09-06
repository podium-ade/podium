import type { LucideIcon } from "lucide-react";
import {
  Circle,
  CircleCheck,
  ContainerIcon,
  LogOut,
  MessageSquare,
  OctagonAlert,
  Package,
  Paperclip,
  Play,
  ListChecks,
  Terminal,
} from "lucide-react";
import { TaskEventKind } from "../../gen/podium/v1/node_pb";
import type { TimelineEntry } from "../../hooks/useTaskEvents";
import { absolute, durationMs, eventKindLabel, toDate } from "../../lib/format";
import { cn } from "../../lib/utils";
import { TaskMessage } from "../TaskMessage";

const IDLE = "border-border bg-raised text-faint";
const RUN = "border-run/40 bg-run/12 text-run";
const OK = "border-ok/40 bg-ok/12 text-ok";
const ERR = "border-err/40 bg-err/12 text-err";
const ACCENT = "border-accent/40 bg-accent/12 text-accent";

const EVENT_STYLE: Record<TaskEventKind, { icon: LucideIcon; tone: string }> = {
  [TaskEventKind.UNSPECIFIED]: { icon: Circle, tone: IDLE },
  [TaskEventKind.PROVISIONING]: { icon: ContainerIcon, tone: IDLE },
  [TaskEventKind.PULLING]: { icon: Package, tone: IDLE },
  [TaskEventKind.STARTED]: { icon: Play, tone: RUN },
  [TaskEventKind.LOG]: { icon: Terminal, tone: IDLE },
  [TaskEventKind.STEP]: { icon: ListChecks, tone: IDLE },
  [TaskEventKind.ARTIFACT]: { icon: Paperclip, tone: IDLE },
  [TaskEventKind.EXITED]: { icon: LogOut, tone: OK },
  [TaskEventKind.FINISHED]: { icon: CircleCheck, tone: OK },
  [TaskEventKind.ERROR]: { icon: OctagonAlert, tone: ERR },
  [TaskEventKind.MESSAGE]: { icon: MessageSquare, tone: ACCENT },
};

/** An exit that is not 0 is the moment the task went wrong, so it is not coloured like a success. */
function styleOf(entry: TimelineEntry) {
  const style = EVENT_STYLE[entry.kind] ?? EVENT_STYLE[TaskEventKind.UNSPECIFIED];
  const exited = entry.kind === TaskEventKind.EXITED || entry.kind === TaskEventKind.FINISHED;
  if (exited && !/^exit 0\b/.test(entry.detail)) return { ...style, tone: ERR };
  return style;
}

function clock(entry: TimelineEntry): string {
  return toDate(entry.ts)?.toLocaleTimeString() ?? "—";
}

/** The wait between two events, which is usually where a slow task actually spent its time. */
function gap(entry: TimelineEntry, previous?: TimelineEntry): string {
  const from = previous && toDate(previous.ts);
  const to = toDate(entry.ts);
  if (!from || !to) return "";
  const ms = to.getTime() - from.getTime();
  return ms >= 1000 ? `+${durationMs(ms)}` : "";
}

/**
 * TaskTimeline is the story of one task: a rail of what the node reported, in order.
 *
 * A `message` event is the task talking rather than the node reporting, so its payload is
 * rendered as a block nested under the entry instead of squeezed into the one-line detail.
 */
export function TaskTimeline({ entries }: { entries: readonly TimelineEntry[] }) {
  return (
    <ol className="relative">
      {entries.map((entry, i) => {
        const { icon: Icon, tone } = styleOf(entry);
        const delta = gap(entry, entries[i - 1]);
        return (
          <li key={String(entry.seq)} className="relative flex gap-3 pb-4 last:pb-0">
            {i < entries.length - 1 ? (
              <span
                aria-hidden
                className="absolute top-6 bottom-0 left-[0.6875rem] w-px bg-border"
              />
            ) : null}
            <span
              aria-hidden
              className={cn(
                "relative z-10 mt-px grid size-5.5 shrink-0 place-items-center rounded-full border",
                tone,
              )}
            >
              <Icon className="size-3" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-baseline gap-x-2">
                <span className="text-xs font-medium text-fg">{eventKindLabel(entry.kind)}</span>
                <time className="tabular text-2xs text-faint" title={absolute(entry.ts)}>
                  {clock(entry)}
                </time>
                {delta ? <span className="tabular text-2xs text-faint">{delta}</span> : null}
              </div>
              {entry.message ? (
                <div className="mt-1.5">
                  <TaskMessage message={entry.message} />
                </div>
              ) : entry.detail ? (
                <p className="mt-0.5 text-xs break-words text-muted">{entry.detail}</p>
              ) : null}
            </div>
          </li>
        );
      })}
    </ol>
  );
}
