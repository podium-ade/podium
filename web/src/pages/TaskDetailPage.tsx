import { useCallback, useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { Badge } from "../components/Badge";
import { LogViewer } from "../components/LogViewer";
import { Skeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { useTaskEvents } from "../hooks/useTaskEvents";
import { errorMessage, tasks } from "../lib/client";
import {
  absolute,
  durationMs,
  eventKindLabel,
  isTerminal,
  specToYaml,
  taskDuration,
  taskStatusLabel,
  taskStatusTone,
} from "../lib/format";

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

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/" className="text-xs text-muted hover:text-fg">
          ← Tasks
        </Link>
        <h1 className="font-mono text-sm font-semibold">{task.id}</h1>
        <Badge tone={taskStatusTone(task.status)}>{taskStatusLabel(task.status)}</Badge>
        {cancelling && !terminal ? <span className="text-xs text-warn">cancelling…</span> : null}
        <div className="ml-auto">
          {terminal ? null : confirming ? (
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

      <dl className="grid grid-cols-2 gap-x-6 gap-y-1 rounded border border-border bg-panel p-3 text-xs sm:grid-cols-4">
        <Field label="Image" value={task.spec?.image ?? "—"} mono />
        <Field label="Requester" value={task.requestedBy || "—"} />
        <Field label="Node" value={task.nodeId || "—"} mono />
        <Field label="Attempts" value={String(task.attempts)} />
        <Field label="Created" value={absolute(task.createdAt)} />
        <Field label="Started" value={absolute(task.startedAt)} />
        <Field label="Finished" value={absolute(task.finishedAt)} />
        <Field
          label="Duration"
          value={taskDuration(task.startedAt, task.finishedAt)}
        />
        <Field label="Exit code" value={task.exitCode === undefined ? "—" : String(task.exitCode)} mono />
        <Field
          label="CPU"
          value={task.usage ? `${task.usage.cpuSeconds.toFixed(2)}s` : "—"}
        />
        <Field
          label="Peak memory"
          value={task.usage ? `${task.usage.peakMemoryMb} MB` : "—"}
        />
        <Field
          label="Wall"
          value={task.usage ? durationMs(Number(task.usage.wallMs)) : "—"}
        />
      </dl>

      <details className="rounded border border-border bg-panel">
        <summary className="cursor-pointer px-3 py-2 text-xs font-medium">Spec</summary>
        <pre className="overflow-x-auto border-t border-border px-3 py-2 font-mono text-xs">
          {specToYaml(task.spec)}
        </pre>
      </details>

      <section className="rounded border border-border bg-panel">
        <h2 className="border-b border-border px-3 py-2 text-xs font-medium">Timeline</h2>
        {timeline.length === 0 ? (
          <p className="px-3 py-2 text-xs text-muted">no events yet</p>
        ) : (
          <ol className="divide-y divide-border">
            {timeline.map((e) => (
              <li key={String(e.seq)} className="flex gap-3 px-3 py-1.5 text-xs">
                <span className="w-40 shrink-0 text-muted">{absolute(e.ts)}</span>
                <span className="w-28 shrink-0 font-medium">{eventKindLabel(e.kind)}</span>
                <span className="text-muted">{e.detail}</span>
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

function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <dt className="text-muted">{label}</dt>
      <dd className={mono ? "font-mono break-all" : ""}>{value}</dd>
    </div>
  );
}
