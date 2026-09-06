import { useCallback, useEffect, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { Ban, ChevronRight, FileCode, Hourglass, RotateCw } from "lucide-react";
import { ArtifactsPanel } from "../components/ArtifactsPanel";
import { Badge, Chip } from "../components/Badge";
import { Empty } from "../components/Empty";
import { LogViewer } from "../components/LogViewer";
import { PageHeader } from "../components/PageHeader";
import { Skeleton } from "../components/Skeleton";
import { CopyValue, PathText } from "../components/task/CopyValue";
import { TaskTimeline } from "../components/task/TaskTimeline";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "../components/ui/collapsible";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/ui/dialog";
import { TaskStatus } from "../gen/podium/v1/common_pb";
import type { Task } from "../gen/podium/v1/task_pb";
import { useTaskEvents } from "../hooks/useTaskEvents";
import { errorMessage, tasks } from "../lib/client";
import {
  absolute,
  durationMs,
  isTerminal,
  queuedExplanation,
  taskDuration,
  taskOutcome,
  taskStatusLabel,
  taskStatusTone,
  toDate,
} from "../lib/format";
import { specToYaml } from "../lib/spec";
import { cn } from "../lib/utils";

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
  const [specOpen, setSpecOpen] = useState(false);

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
    onSettled: () => setConfirming(false),
  });

  const task = query.data?.task;

  if (query.isPending) return <DetailSkeleton />;

  if (!task) {
    return (
      <div className="space-y-5">
        <PageHeader back={{ to: "/", label: "Tasks" }} title="Task not found" />
        <Alert variant="destructive" title="Could not load this task">
          {errorMessage(query.error) || `The control plane has no task with the ID ${id}.`} Check
          the ID, or pick the task from the list.
        </Alert>
        <Link to="/" className={buttonVariants({ variant: "outline", size: "sm" })}>
          Back to tasks
        </Link>
      </div>
    );
  }

  const terminal = isTerminal(task.status);
  const image = task.spec?.image ?? "";
  const command = task.spec?.command.join(" ") ?? "";
  // A task that has never been placed has no output, no events and no files — three empty
  // cards say that three times and answer nothing.
  const notStarted =
    !task.startedAt && !terminal && timeline.length === 0 && lines.length === 0;

  return (
    <div className="space-y-5">
      <PageHeader
        back={{ to: "/", label: "Tasks" }}
        title={
          <CopyValue value={task.id} label="task ID" className="text-lg tracking-tight" />
        }
        description={
          command ? (
            <span className="font-mono text-xs break-words">{command}</span>
          ) : (
            "Runs the image's own entrypoint."
          )
        }
        meta={
          <>
            <Badge tone={taskStatusTone(task.status)}>{taskStatusLabel(task.status)}</Badge>
            {cancelling && !terminal ? <Badge tone="warn">cancelling…</Badge> : null}
          </>
        }
        actions={
          terminal ? (
            // A terminal task has no outgoing edges in the state graph and there is no
            // "requeue" on the wire: re-run creates a new task from this one's spec.
            <Link
              to={`/submit?rerun=${task.id}`}
              className={buttonVariants({ variant: "outline", size: "sm" })}
            >
              <RotateCw />
              Re-run
            </Link>
          ) : (
            <Button
              variant="danger"
              size="sm"
              disabled={cancel.isPending || cancelling}
              onClick={() => setConfirming(true)}
            >
              <Ban />
              {cancelling ? "Cancelling…" : "Cancel task"}
            </Button>
          )
        }
      />

      <Explain task={task} />

      <Card>
        <div className="grid divide-y divide-hairline xl:grid-cols-[1.5fr_1fr_1fr] xl:divide-x xl:divide-y-0">
          <FactGroup title="Task">
            <Fact label="Image">
              {image ? (
                <CopyValue value={image} label="image">
                  <PathText text={image} />
                </CopyValue>
              ) : (
                <Unknown>no image</Unknown>
              )}
            </Fact>
            <Fact label="Command">
              {command ? (
                <span className="font-mono break-words">{command}</span>
              ) : (
                <Unknown>the image entrypoint</Unknown>
              )}
            </Fact>
            <Fact label="Requester">{task.requestedBy || <Unknown>unknown</Unknown>}</Fact>
          </FactGroup>

          <FactGroup title="Placement">
            <Fact label="Node">
              {task.nodeId ? (
                <CopyValue value={task.nodeId} label="node ID" />
              ) : (
                <Unknown>not placed yet</Unknown>
              )}
            </Fact>
            <Fact label="Attempts">
              <span className="tabular">
                {task.attempts} of {task.spec?.maxAttempts || 1}
              </span>
              {task.spec?.retryOnNodeLoss ? (
                <span className="text-faint"> · retries on node loss</span>
              ) : null}
            </Fact>
            <Fact label="Labels">
              {task.spec && task.spec.labels.length > 0 ? (
                <span className="flex flex-wrap gap-1">
                  {task.spec.labels.map((l) => (
                    <Chip key={l} className="font-mono">
                      {l}
                    </Chip>
                  ))}
                </span>
              ) : (
                <Unknown>any node</Unknown>
              )}
            </Fact>
            {task.spec?.resources ? (
              <Fact label="Requested">
                <span className="tabular">
                  {task.spec.resources.cpu} CPU · {String(task.spec.resources.memoryMb)} MB
                </span>
              </Fact>
            ) : null}
          </FactGroup>

          <FactGroup title="Timing">
            <TimeFact label="Created" ts={task.createdAt} />
            <TimeFact label="Started" ts={task.startedAt} since={task.createdAt} />
            <TimeFact label="Finished" ts={task.finishedAt} since={task.startedAt} />
            <Fact label={terminal ? "Duration" : task.startedAt ? "Elapsed" : "Waiting"}>
              <span className="tabular">
                {task.startedAt ? taskDuration(task.startedAt, task.finishedAt) : waiting(task)}
              </span>
            </Fact>
          </FactGroup>
        </div>
        <Usage task={task} />
      </Card>

      {notStarted ? (
        <Empty
          icon={Hourglass}
          title="Nothing to show yet"
          hint="This task has not started, so it has no output, no events and no files. They appear here the moment a node picks it up."
          action={
            <Link to="/nodes" className={buttonVariants({ variant: "outline", size: "sm" })}>
              Check the nodes
            </Link>
          }
        />
      ) : (
        <>
          {/* Log lines are long; the output gets the full width and the two lists share the
              row below it. */}
          <LogViewer lines={lines} phase={phase} error={error} taskId={task.id} />

          <div className="grid items-start gap-5 lg:grid-cols-2">
            <Card className="flex min-w-0 flex-col overflow-hidden">
              <CardHeader className="pb-3">
                <div>
                  <CardTitle>Timeline</CardTitle>
                  <CardDescription>What the node reported, in order.</CardDescription>
                </div>
              </CardHeader>
              <div className="max-h-[34rem] overflow-y-auto border-t border-hairline px-5 py-4">
                {timeline.length === 0 ? (
                  <p className="text-xs text-muted">
                    No events yet. The node reports one as each stage of the run begins.
                  </p>
                ) : (
                  <TaskTimeline entries={timeline} />
                )}
              </div>
            </Card>

            <ArtifactsPanel
              taskId={task.id}
              refetch={!terminal || task.status === TaskStatus.SUCCEEDED}
            />
          </div>
        </>
      )}

      <Collapsible open={specOpen} onOpenChange={setSpecOpen}>
        <Card className="overflow-hidden">
          <CollapsibleTrigger className="flex w-full items-center gap-2.5 px-5 py-3.5 text-left transition-colors outline-none hover:bg-raised/40 focus-visible:ring-2 focus-visible:ring-ring/50">
            <FileCode className="size-4 shrink-0 text-faint" />
            <span className="text-sm font-semibold tracking-tight text-fg">Spec</span>
            <span className="hidden text-xs text-muted sm:inline">
              The definition this run was created from
            </span>
            <ChevronRight
              aria-hidden
              className={cn(
                "ml-auto size-4 shrink-0 text-faint transition-transform duration-150",
                specOpen && "rotate-90",
              )}
            />
          </CollapsibleTrigger>
          <CollapsibleContent>
            <pre className="overflow-x-auto border-t border-hairline bg-bg px-5 py-4 font-mono text-xs leading-relaxed text-muted">
              {specToYaml(task.spec)}
            </pre>
          </CollapsibleContent>
        </Card>
      </Collapsible>

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Cancel this task?</DialogTitle>
            <DialogDescription>
              <span className="font-mono text-fg">{task.id}</span> is{" "}
              {taskStatusLabel(task.status)}
              {task.nodeId ? ` on ${task.nodeId}` : ""}. Cancelling stops it and it cannot be
              resumed — only re-run.
            </DialogDescription>
          </DialogHeader>
          <p className="text-xs leading-relaxed text-muted">
            The node sends SIGTERM and then SIGKILL 30 seconds later, so the container may take
            up to half a minute to disappear. Output already streamed is kept.
          </p>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline" size="sm">
                Keep running
              </Button>
            </DialogClose>
            <Button
              variant="destructive"
              size="sm"
              disabled={cancel.isPending}
              onClick={() => cancel.mutate()}
            >
              {cancel.isPending ? "Cancelling…" : "Cancel task"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

/**
 * Explain is the banner that says what the system is doing, in words, when the status badge
 * alone does not: why a queued task is still queued, and why a terminal one ended as it did.
 * `lost` gets its own colour and its own sentence — it is not a failure and must not read as one.
 */
function Explain({ task }: { task: Task }) {
  const queued = queuedExplanation(task);
  if (queued) {
    return (
      <Alert data-testid="queued-reason" variant="info" title="Not scheduled yet">
        {queued}
      </Alert>
    );
  }

  const outcome = taskOutcome(task);
  if (outcome === "") return null;

  const variant =
    task.status === TaskStatus.LOST
      ? "lost"
      : task.status === TaskStatus.FAILED
        ? "destructive"
        : "warn";
  // The exit code belongs in the headline of a failure, not four fields down a metadata grid.
  const heading =
    task.status === TaskStatus.LOST
      ? "Lost, not failed"
      : task.status === TaskStatus.FAILED
        ? task.exitCode === undefined
          ? "Failed"
          : `Failed — exit ${task.exitCode}`
        : task.status === TaskStatus.CANCELLED
          ? "Cancelled"
          : undefined;

  return (
    <Alert data-testid="task-outcome" variant={variant} title={heading}>
      {outcome}
    </Alert>
  );
}

/** Usage is the receipt: what the run actually cost, once the node has reported it. */
function Usage({ task }: { task: Task }) {
  const usage = task.usage;
  const limit = task.spec?.resources?.memoryMb;
  const items: { label: string; value: string; sub?: string; tone?: string }[] = [];

  if (task.exitCode !== undefined) {
    items.push({
      label: "Exit code",
      value: String(task.exitCode),
      tone: task.exitCode === 0 ? "text-ok" : "text-err",
    });
  }
  if (usage) {
    items.push({ label: "CPU", value: `${usage.cpuSeconds.toFixed(2)}s` });
    if (usage.peakMemoryMb) {
      items.push({
        label: "Peak memory",
        value: `${usage.peakMemoryMb} MB`,
        sub: limit ? `of ${limit} MB` : undefined,
        tone: limit && usage.peakMemoryMb >= limit ? "text-err" : undefined,
      });
    }
    if (usage.wallMs) items.push({ label: "Wall", value: durationMs(Number(usage.wallMs)) });
  }
  if (items.length === 0) return null;

  return (
    <div className="flex flex-wrap gap-x-10 gap-y-3 border-t border-hairline px-4 py-3">
      {items.map((item) => (
        <div key={item.label}>
          <dt className="text-2xs text-faint">{item.label}</dt>
          <dd className={cn("tabular text-sm font-medium", item.tone ?? "text-fg")}>
            {item.value}
            {item.sub ? <span className="ml-1.5 text-2xs text-faint">{item.sub}</span> : null}
          </dd>
        </div>
      ))}
    </div>
  );
}

function FactGroup({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="min-w-0 space-y-3 p-4">
      <h3 className="text-2xs font-medium tracking-wide text-faint uppercase">{title}</h3>
      <dl className="space-y-2">{children}</dl>
    </section>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[5.25rem_minmax(0,1fr)] gap-x-3">
      <dt className="text-2xs text-faint">{label}</dt>
      <dd className="min-w-0 text-xs text-fg">{children}</dd>
    </div>
  );
}

/** A field that has no value yet says why, in words, rather than showing an em-dash. */
function Unknown({ children }: { children: ReactNode }) {
  return <span className="text-faint italic">{children}</span>;
}

function TimeFact({ label, ts, since }: { label: string; ts?: Timestamp; since?: Timestamp }) {
  const at = toDate(ts);
  if (!at) return null;
  const from = toDate(since);
  const delta = from ? at.getTime() - from.getTime() : 0;
  return (
    <Fact label={label}>
      <span className="tabular" title={absolute(ts)}>
        {at.toLocaleTimeString()}
      </span>
      {delta >= 1000 ? (
        <span className="tabular ml-2 text-faint">+{durationMs(delta)}</span>
      ) : null}
    </Fact>
  );
}

function waiting(task: Task): string {
  const created = toDate(task.createdAt);
  return created ? durationMs(Date.now() - created.getTime()) : "—";
}

function DetailSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading task" className="space-y-5">
      <div className="space-y-3">
        <Skeleton className="h-3 w-14" />
        <Skeleton className="h-6 w-80" />
        <Skeleton className="h-4 w-64" />
        <Skeleton className="h-5 w-20" />
      </div>
      <Card>
        <div className="grid gap-6 p-4 md:grid-cols-3">
          {[0, 1, 2].map((c) => (
            <div key={c} className="space-y-3">
              <Skeleton className="h-2.5 w-16" />
              {[0, 1, 2].map((r) => (
                <Skeleton key={r} className="h-3.5" style={{ width: `${88 - r * 18}%` }} />
              ))}
            </div>
          ))}
        </div>
      </Card>
      <Skeleton className="h-[30rem] rounded-xl" />
      <div className="grid gap-5 lg:grid-cols-2">
        <Skeleton className="h-64 rounded-xl" />
        <Skeleton className="h-64 rounded-xl" />
      </div>
    </div>
  );
}
