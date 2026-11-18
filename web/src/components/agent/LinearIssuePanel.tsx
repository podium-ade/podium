import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { ChevronDown, ExternalLink, GitPullRequest } from "lucide-react";
import type {
  ChatPullRequest,
  GetLinearIssueConversationResponse,
  LinearConversationRoutineRun,
  LinearConversationTask,
} from "../../gen/podium/agent/v1/agent_pb";
import { agent } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { pullLinks } from "../../lib/linearIssues";
import { cn } from "../../lib/utils";
import { Badge, type Tone } from "../Badge";
import { Button } from "../ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/collapsible";
import { StateDot } from "./chat/LinearIssueChip";

/** How often the panel asks again while the conversation is open and nothing happened in it. */
const POLL_MS = 30_000;

const TASK_TONE: Record<string, Tone> = {
  running: "run",
  succeeded: "ok",
  failed: "err",
  timeout: "err",
  lost: "lost",
  cancelled: "idle",
};

const RUN_TONE: Record<string, Tone> = {
  running: "run",
  done: "ok",
  idle: "idle",
  failed: "err",
  skipped_overlap: "idle",
  skipped_paused: "idle",
};

/**
 * LinearIssuePanel is the head of a Linear issue's conversation: the issue as Linear has it now —
 * identifier, title, state, team, assignee and labels — and, opened, its pull requests, the tasks
 * the conversation started and the routine runs that happened in it.
 *
 * It asks again whenever something is said in the conversation (refreshKey), when the window
 * comes back into focus, and every half minute: Linear's own changes reach Podium as webhooks,
 * which do not move the chat stream.
 *
 * A conversation that is no issue's — a project's, or a Linear agent session's from before issues
 * were one conversation — draws nothing.
 */
export function LinearIssuePanel({
  chatId,
  pullRequests,
  refreshKey,
}: {
  chatId: string;
  pullRequests: ChatPullRequest[];
  refreshKey: number;
}) {
  const panel = useQuery({
    queryKey: ["agent", "linear-panel", chatId],
    queryFn: async (): Promise<GetLinearIssueConversationResponse | null> => {
      try {
        return await agent.getLinearIssueConversation({ chatId });
      } catch (err) {
        if (err instanceof ConnectError && err.code === Code.NotFound) return null;
        throw err;
      }
    },
    refetchInterval: POLL_MS,
    refetchOnWindowFocus: true,
    retry: false,
  });
  const { refetch } = panel;
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    void refetch();
  }, [refreshKey, refetch]);

  const [open, setOpen] = useState(false);
  const cardRef = useRef<HTMLElement>(null);
  // The details lie over the transcript, as a routine's do: a click elsewhere, or Escape, puts
  // them away.
  useEffect(() => {
    if (!open) return;
    const away = (e: PointerEvent) => {
      const t = e.target as Node | null;
      if (t && !cardRef.current?.contains(t)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("pointerdown", away);
      document.removeEventListener("keydown", esc);
    };
  }, [open]);
  const [detailsHeight, setDetailsHeight] = useState(256);
  useLayoutEffect(() => {
    if (!open || !cardRef.current) return;
    const below = window.innerHeight - cardRef.current.getBoundingClientRect().bottom - 12;
    setDetailsHeight(Math.max(140, Math.min(256, Math.floor(below))));
  }, [open]);

  const data = panel.data;
  const issue = data?.issue;
  if (!data || !issue) return null;
  const prs = pullLinks(issue.links, pullRequests);
  const running = data.tasks.filter((t) => t.status === "running").length;
  const team = issue.teamName || issue.teamKey;
  const summary = [
    prs.length > 0 ? `${prs.length} ${prs.length === 1 ? "pull request" : "pull requests"}` : "",
    data.tasks.length > 0
      ? `${data.tasks.length} ${data.tasks.length === 1 ? "task" : "tasks"}${running > 0 ? `, ${running} running` : ""}`
      : "",
    data.routineRuns.length > 0
      ? `${data.routineRuns.length} routine ${data.routineRuns.length === 1 ? "run" : "runs"}`
      : "",
  ].filter((s) => s !== "");

  return (
    <Collapsible open={open} onOpenChange={setOpen} asChild>
      <section
        ref={cardRef}
        data-testid="linear-issue-panel"
        aria-label={`Linear issue ${issue.identifier}`}
        className="relative z-20 mx-4 mt-3 rounded-lg border border-border bg-panel text-sm transition-[border-radius] data-[state=open]:rounded-b-none sm:mx-6"
      >
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5">
          <div className="min-w-[min(100%,16rem)] flex-1 space-y-0.5">
            <p className="flex min-w-0 items-center gap-2 text-fg">
              <StateDot stateType={issue.stateType} />
              <span className="shrink-0 font-mono text-xs text-muted">{issue.identifier}</span>
              <span className="min-w-0 truncate font-semibold" title={issue.title}>
                {issue.title || "Untitled"}
              </span>
            </p>
            <p data-testid="linear-issue-facts" className="flex flex-wrap items-center gap-x-1.5 pl-3.5 text-xs text-muted">
              {issue.stateName ? <span>{issue.stateName}</span> : null}
              {team ? <span>· {team}</span> : null}
              {issue.cached ? null : <span>· {issue.assignee ? issue.assignee : "Unassigned"}</span>}
              {issue.updatedAt ? (
                <span title={absolute(issue.updatedAt)}>· updated {relative(issue.updatedAt)}</span>
              ) : null}
              {issue.labels.map((l) => (
                <span key={l} className="rounded-sm bg-raised px-1.5 text-2xs text-soft">
                  {l}
                </span>
              ))}
            </p>
          </div>
          <span className="flex items-center gap-1">
            {summary.length > 0 ? (
              <span className="hidden text-xs text-faint tabular-nums sm:inline">{summary.join(" · ")}</span>
            ) : null}
            {issue.url ? (
              <Button asChild size="xs" variant="outline">
                <a href={issue.url} target="_blank" rel="noreferrer noopener">
                  <ExternalLink aria-hidden />
                  Open in Linear
                </a>
              </Button>
            ) : null}
            <CollapsibleTrigger asChild>
              <Button size="icon-xs" variant="ghost" aria-label={open ? "Hide details" : "Show details"}>
                <ChevronDown aria-hidden className={cn("transition-transform duration-200", open && "rotate-180")} />
              </Button>
            </CollapsibleTrigger>
          </span>
        </div>

        <CollapsibleContent
          data-testid="linear-issue-details"
          className={cn(
            "absolute -inset-x-px top-full overflow-hidden rounded-b-lg border border-t-0 border-border bg-panel shadow-lg",
            "data-[state=open]:animate-collapsible-down data-[state=closed]:animate-collapsible-up",
          )}
        >
          <div
            className="grid content-start gap-3 overflow-y-auto border-t border-border px-3 py-3 text-xs"
            style={{ maxHeight: detailsHeight }}
          >
            <Section title="Pull requests" empty="None linked in Linear or opened here.">
              {prs.map((pr) => (
                <li key={pr.url} className="flex items-center gap-2">
                  <GitPullRequest aria-hidden className="size-3.5 shrink-0 text-faint" />
                  <a
                    href={pr.url}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="font-mono text-fg hover:text-accent"
                  >
                    {pr.label}
                  </a>
                  <span className="text-faint">{pr.from === "linear" ? "linked in Linear" : "from this conversation"}</span>
                </li>
              ))}
            </Section>
            <Section title="Tasks started here" empty="None yet.">
              {data.tasks.map((t, i) => (
                <TaskRow key={t.taskId || `pending-${i}`} t={t} />
              ))}
            </Section>
            <Section title="Routine runs here" empty="None.">
              {data.routineRuns.map((r) => (
                <RunRow key={r.id} r={r} />
              ))}
            </Section>
          </div>
        </CollapsibleContent>
      </section>
    </Collapsible>
  );
}

function Section({ title, empty, children }: { title: string; empty: string; children: React.ReactNode[] }) {
  return (
    <div>
      <h3 className="mb-1 font-semibold tracking-[0.08em] text-muted uppercase">{title}</h3>
      {children.length > 0 ? <ul className="space-y-1">{children}</ul> : <p className="text-muted">{empty}</p>}
    </div>
  );
}

function TaskRow({ t }: { t: LinearConversationTask }) {
  const tone = TASK_TONE[t.status] ?? "idle";
  return (
    <li className="flex min-w-0 items-center gap-2">
      <Badge tone={tone}>{t.status}</Badge>
      {t.taskId ? (
        <Link to={`/tasks/${t.taskId}`} className="min-w-0 truncate text-fg hover:text-accent">
          {t.playbook || "task"}
        </Link>
      ) : (
        <span className="min-w-0 truncate text-fg">{t.playbook || "task"}</span>
      )}
      <span className="shrink-0 text-faint" title={absolute(t.createdAt)}>
        {relative(t.createdAt)}
      </span>
    </li>
  );
}

function RunRow({ r }: { r: LinearConversationRoutineRun }) {
  const tone = RUN_TONE[r.status] ?? "idle";
  return (
    <li className="flex min-w-0 items-center gap-2">
      <Badge tone={tone}>{r.status.replace("skipped_", "skipped, ")}</Badge>
      <Link to={`/agent/routines/${r.routineId}`} className="shrink-0 text-fg hover:text-accent">
        {r.routineName}
      </Link>
      {r.summary ? <span className="min-w-0 truncate text-muted">{r.summary}</span> : null}
      <span className="ml-auto shrink-0 text-faint" title={absolute(r.createdAt)}>
        {relative(r.createdAt)}
      </span>
    </li>
  );
}
