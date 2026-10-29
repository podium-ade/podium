import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { TextMessagePartProvider } from "@assistant-ui/react";
import { Brain, ChevronRight, CircleHelp, MessageSquare, OctagonAlert, ScrollText } from "lucide-react";
import { isTranscript, type Entry } from "../../lib/entries";
import { absolute } from "../../lib/format";
import { cn } from "../../lib/utils";
import { ToolCallRow } from "../activity/ToolCallRow";
import { MarkdownText } from "../agent/chat/MarkdownText";
import { Chip } from "../Badge";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/collapsible";

const AT_BOTTOM_SLACK_PX = 32;

/**
 * Transcript is the story of a run: what the task said and what it did, in order. Tool
 * calls are one line each and open to their input and output; thoughts fold; an answer is
 * read as markdown. Every step can open the log explorer on itself, where the lines the
 * container printed while that step ran are marked.
 */
export function Transcript({
  entries,
  live,
  onViewInLogs,
}: {
  entries: readonly Entry[];
  /** live keeps the view on the newest step while the run is still going. */
  live: boolean;
  onViewInLogs: (key: string) => void;
}) {
  const steps = useMemo(() => entries.filter(isTranscript), [entries]);
  const ref = useRef<HTMLDivElement>(null);
  const [follow, setFollow] = useState(live);

  useEffect(() => {
    const el = ref.current;
    if (el && follow && live) el.scrollTop = el.scrollHeight;
  }, [steps.length, follow, live]);

  const onScroll = useCallback(() => {
    const el = ref.current;
    if (el) setFollow(el.scrollHeight - el.scrollTop - el.clientHeight <= AT_BOTTOM_SLACK_PX);
  }, []);

  if (steps.length === 0) {
    return (
      <div className="flex min-h-40 flex-col items-center justify-center gap-1 px-4 text-center">
        <p className="text-xs text-muted">Nothing said yet</p>
        <p className="text-2xs text-faint">
          Progress, tool calls and the answer appear here as the task reports them.
        </p>
      </div>
    );
  }

  return (
    <div
      ref={ref}
      onScroll={onScroll}
      data-testid="transcript"
      className="max-h-[40rem] overflow-y-auto px-5 py-4"
    >
      <ol className="space-y-2.5">
        {steps.map((e) => (
          <li key={e.key} data-testid="transcript-step" data-kind={e.kind} className="group/step min-w-0">
            <Step entry={e} aside={<Aside entry={e} onViewInLogs={onViewInLogs} />} />
          </li>
        ))}
      </ol>
    </div>
  );
}

/** Aside is a step's time, and the way from the step to the logs around it. */
function Aside({ entry, onViewInLogs }: { entry: Entry; onViewInLogs: (key: string) => void }) {
  return (
    <span className="flex shrink-0 items-center gap-2">
      <button
        type="button"
        onClick={() => onViewInLogs(entry.key)}
        className="inline-flex items-center gap-1 rounded px-1 text-2xs text-accent opacity-0 transition-opacity group-hover/step:opacity-100 hover:underline focus-visible:opacity-100"
      >
        <ScrollText className="size-3" />
        View in logs
      </button>
      <time className="tabular text-2xs text-faint" title={absolute(entry.ts)}>
        {entry.at ? new Date(entry.at).toLocaleTimeString() : ""}
      </time>
    </span>
  );
}

function Step({ entry, aside }: { entry: Entry; aside: ReactNode }) {
  const a = entry.activity;
  if (a?.kind === "tool") {
    const failed = a.status === "error";
    return (
      <ToolCallRow
        testId="transcript-tool"
        tool={a.tool}
        summary={entry.summary}
        input={a.input}
        output={failed ? a.error : a.output}
        isError={failed}
        aside={aside}
      />
    );
  }
  if (a?.kind === "reasoning") return <Reasoning text={a.text} aside={aside} />;

  const m = entry.message;
  if (!m) {
    // A lifecycle error: the node's word for why the run went wrong, in the story where it happened.
    return (
      <Line icon={<OctagonAlert className="size-3.5 text-err" />} aside={aside}>
        <span className="text-sm text-err">{entry.summary}</span>
      </Line>
    );
  }
  const attachments =
    m.attachments.length > 0 ? (
      <span className="mt-1.5 flex flex-wrap gap-1">
        {m.attachments.map((name) => (
          <Chip key={name}>{name}</Chip>
        ))}
      </span>
    ) : null;

  if (m.type === "final") {
    return (
      <div className="rounded-lg border border-border bg-panel px-3.5 py-3">
        <div className="mb-1.5 flex items-center gap-2">
          <span className="text-2xs font-medium tracking-wide text-ok uppercase">Answer</span>
          <span className="ml-auto">{aside}</span>
        </div>
        <TextMessagePartProvider text={m.text}>
          <MarkdownText className="text-sm leading-6" />
        </TextMessagePartProvider>
        {attachments}
      </div>
    );
  }
  if (m.type === "question") {
    return (
      <div className="rounded-lg border border-warn/50 bg-warn-bg px-3.5 py-2.5">
        <div className="flex items-center gap-2">
          <CircleHelp className="size-3.5 text-warn" />
          <span className="text-2xs font-medium tracking-wide text-warn uppercase">Asked</span>
          <span className="ml-auto">{aside}</span>
        </div>
        <p className="mt-1 text-sm whitespace-pre-wrap text-fg">{m.text}</p>
        {attachments}
      </div>
    );
  }
  return (
    <Line icon={<MessageSquare className="size-3.5 text-faint" />} aside={aside}>
      <span className="min-w-0 text-sm break-words whitespace-pre-wrap text-muted">
        {m.type && m.type !== "progress" ? <span className="mr-1.5 font-mono text-2xs text-faint">{m.type}</span> : null}
        {m.text}
      </span>
      {attachments}
    </Line>
  );
}

function Line({ icon, aside, children }: { icon: ReactNode; aside: ReactNode; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-start gap-2">
      <span className="mt-1 shrink-0">{icon}</span>
      <div className="min-w-0 flex-1">{children}</div>
      <span className="mt-0.5">{aside}</span>
    </div>
  );
}

/** Reasoning shows its first line, and the whole thought when opened. */
function Reasoning({ text, aside }: { text: string; aside: ReactNode }) {
  const first = text.split("\n", 1)[0];
  const more = text.length > first.length;
  return (
    <Collapsible className="min-w-0">
      <div className="flex min-w-0 items-center gap-2">
        <CollapsibleTrigger
          disabled={!more}
          className="group/trigger flex min-w-0 flex-1 items-center gap-2 text-left text-sm text-faint transition-colors enabled:hover:text-muted"
        >
          <Brain className="size-3.5 shrink-0" />
          <span className="min-w-0 truncate italic">{first}</span>
          {more ? (
            <ChevronRight className="size-3.5 shrink-0 transition-transform group-data-[state=open]/trigger:rotate-90" />
          ) : null}
        </CollapsibleTrigger>
        {aside}
      </div>
      <CollapsibleContent className="data-[state=closed]:animate-collapsible-up data-[state=open]:animate-collapsible-down overflow-hidden">
        <p className={cn("mt-1 ml-5.5 text-sm leading-6 whitespace-pre-wrap text-muted")}>{text}</p>
      </CollapsibleContent>
    </Collapsible>
  );
}
