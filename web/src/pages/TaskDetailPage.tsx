import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { ArtifactsPanel } from "../components/ArtifactsPanel";
import { Badge } from "../components/Badge";
import { LogViewer } from "../components/LogViewer";
import { Skeleton } from "../components/Skeleton";
import { TaskMessage } from "../components/TaskMessage";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { buttonVariants } from "../components/ui/button";
import { TaskStatus } from "../gen/podium/v1/common_pb";
import type { Task } from "../gen/podium/v1/task_pb";
import { useTaskEvents } from "../hooks/useTaskEvents";
import { errorMessage, tasks } from "../lib/client";
import {
  absolute,
  durationMs,
  eventKindLabel,
  isTerminal,
  queuedExplanation,
  taskDuration,
  taskOutcome,
  taskStatusLabel,
  taskStatusTone,
} from "../lib/format";
import { specToYaml } from "../lib/spec";

const POLL_MS = 5000;

/** Keyed on the task id so navigating between tasks starts a fresh log stream and a fresh
 *  accumulator instead of appending one task's output to another's. */
export function TaskDetailPage() {
  const { id = "" } = useParams();
  return <TaskDetail key={id} id={id} />;
}

function TaskDetail({ id }: { id: string }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [cancelling, setCancelling] = useState(false);
  const [confirming, setConfirming] = useState(false);

  const query = useQuery({
    queryKey: ["task", id],
    queryFn: () => tasks.getTask({ taskId: id }),
    refetchInterval: (q) => (q.state.data && isTerminal(q.state.data.task!.status) ? false : POLL_MS),
  });

  const reread = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["task", id] });
  }, [qc, id]);

  const { lines, timeline, phase, error } = useTaskEvents(id, reread);

  useEffect(() => {
    if (query.error) toast(`GetTask: ${errorMessage(query.error)}`);
  }, [query.error, toast]);

  // CancelTask returns the task unchanged: with no runner the command is PID 1, so a
  // default-disposition SIGTERM is discarded and the container dies at the node's 30s SIGKILL.
  // The real terminal status arrives with the node's `finished` event, so say "cancelling…" and
  // let it land rather than claiming the task is already cancelled.
  const cancel = useMutation({
    mutationFn: () => tasks.cancelTask({ taskId: id, reason: "cancelled from the web UI" }),
    onSuccess: () => {
      setCancelling(true);
      toast("Cancel requested — the node has up to 30s to stop the container.", "ok");
      void qc.invalidateQueries({ queryKey: ["task", id] });
    },
    onError: (err) => toast(`CancelTask: ${errorMessage(err)}`),
  });

  const task = query.data?.task;

  if (query.isPending) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-6 w-96" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-96 w-full" />
      </div>
    );
  }

  if (!task) {
    return (
      <p className="text-sm text-err">
        {errorMessage(query.error) || "task not found"} —{" "}
        <Link to="/" className="text-accent hover:underline">
          back to tasks
        </Link>
      </p>
    );
  }

  const terminal = isTerminal(task.status);
  const queued = queuedExplanation(task);
  const outcome = taskOutcome(task);

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/" className="text-xs text-muted hover:text-fg">
          ← Tasks
        </Link>
        <h1 className="font-mono text-sm font-semibold">{task.id}</h1>
        <Badge tone={taskStatusTone(task.status)}>{taskStatusLabel(task.status)}</Badge>
        {cancelling && !terminal ? <span className="text-xs text-warn">cancelling…</span> : null}
        <div className="ml-auto flex items-center gap-2">
          {terminal ? (
            // A terminal task has no outgoing edges in the state graph and there is no
            // "requeue" on the wire: re-run creates a new task from this one's spec.
            <Link
              to={`/submit?rerun=${task.id}`}
              className={buttonVariants({ variant: "outline", size: "sm" })}
            >
              Re-run
            </Link>
          ) : confirming ? (
            <span className="flex items-center gap-2 text-xs">
              Cancel this task?
              <button
                type="button"
                onClick={() => {
                  setConfirming(false);
                  cancel.mutate();
                }}
                className="rounded border border-err/60 px-2 py-1 text-err"
              >
                Yes, cancel
              </button>
              <button
                type="button"
                onClick={() => setConfirming(false)}
                className="rounded border border-border px-2 py-1"
              >
                Keep running
              </button>
            </span>
          ) : (
            <button
              type="button"
              disabled={cancel.isPending}
              onClick={() => setConfirming(true)}
              className="rounded border border-border px-2 py-1 text-xs hover:border-err hover:text-err disabled:opacity-40"
            >
              Cancel task
            </button>
          )}
        </div>
      </div>

      <Explain task={task} queued={queued} outcome={outcome} />

      <dl className="grid grid-cols-2 gap-x-6 gap-y-2 rounded-xl border border-border bg-card p-4 text-xs shadow-xs sm:grid-cols-4">
        <Field label="Image" value={task.spec?.image ?? "—"} mono />
        <Field label="Requester" value={task.requestedBy || "—"} />
        <Field label="Node" value={task.nodeId || "—"} mono />
        <Field
          label="Attempts"
          value={`${task.attempts} of ${task.spec?.maxAttempts || 1}${
            task.spec?.retryOnNodeLoss ? ", retries on node loss" : ""
          }`}
        />
        <Field label="Created" value={absolute(task.createdAt)} />
        <Field label="Started" value={absolute(task.startedAt)} />
        <Field label="Finished" value={absolute(task.finishedAt)} />
        <Field label="Duration" value={taskDuration(task.startedAt, task.finishedAt)} />
        <Field
          label="Exit code"
          value={task.exitCode === undefined ? "—" : String(task.exitCode)}
          mono
        />
        <Field label="CPU" value={task.usage ? `${task.usage.cpuSeconds.toFixed(2)}s` : "—"} />
        <Field
          label="Peak memory"
          value={task.usage?.peakMemoryMb ? `${task.usage.peakMemoryMb} MB` : "—"}
        />
        <Field
          label="Wall"
          value={task.usage?.wallMs ? durationMs(Number(task.usage.wallMs)) : "—"}
        />
      </dl>

      <details className="rounded-xl border border-border bg-card shadow-xs">
        <summary className="cursor-pointer px-3 py-2 text-xs font-medium">Spec</summary>
        <pre className="overflow-x-auto border-t border-border px-3 py-2 font-mono text-xs">
          {specToYaml(task.spec)}
        </pre>
      </details>

      <ArtifactsPanel taskId={task.id} refetch={!terminal || task.status === TaskStatus.SUCCEEDED} />

      <section className="rounded-xl border border-border bg-card shadow-xs">
        <h2 className="border-b border-border px-3 py-2 text-xs font-medium">Timeline</h2>
        {timeline.length === 0 ? (
          <p className="px-3 py-2 text-xs text-muted">no events yet</p>
        ) : (
          <ol className="divide-y divide-border">
            {timeline.map((e) => (
              <li key={String(e.seq)} className="flex flex-wrap gap-3 px-3 py-1.5 text-xs">
                <span className="w-40 shrink-0 text-muted">{absolute(e.ts)}</span>
                <span className="w-28 shrink-0 font-medium">{eventKindLabel(e.kind)}</span>
                {e.message ? <TaskMessage message={e.message} /> : null}
                {e.message ? null : <span className="text-muted">{e.detail}</span>}
              </li>
            ))}
          </ol>
        )}
      </section>

      {error && phase === "error" ? (
        <p className="text-xs text-err">log stream: {error} — reconnecting</p>
      ) : null}

      <LogViewer lines={lines} phase={phase} taskId={task.id} />
    </div>
  );
}

/**
 * Explain is the banner that says what the system is doing, in words, when the status badge
 * alone does not: why a queued task is still queued, and why a terminal one ended as it did.
 * `lost` gets its own colour and its own sentence — it is not a failure and must not read as one.
 */
function Explain({
  task,
  queued,
  outcome,
}: {
  task: Task;
  queued?: string;
  outcome: string;
}) {
  if (queued) {
    return (
      <Alert data-testid="queued-reason" variant="info">
        <b>Not scheduled yet.</b> {queued}
      </Alert>
    );
  }
  if (outcome === "") return null;
  const variant =
    task.status === TaskStatus.LOST
      ? "lost"
      : task.status === TaskStatus.FAILED
        ? "destructive"
        : "warn";
  const heading =
    task.status === TaskStatus.LOST
      ? "Lost, not failed."
      : task.status === TaskStatus.FAILED
        ? "Failed."
        : task.status === TaskStatus.CANCELLED
          ? "Cancelled."
          : "";
  return (
    <Alert data-testid="task-outcome" variant={variant}>
      {heading ? <b>{heading} </b> : null}
      {outcome}
    </Alert>
  );
}

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-muted">{label}</dt>
      <dd className={mono ? "font-mono break-all" : ""}>{value}</dd>
    </div>
  );
}
