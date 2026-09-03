import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import type { Tone } from "../components/Badge";
import { NodeStatus, TaskStatus } from "../gen/podium/v1/common_pb";
import { TaskEventKind } from "../gen/podium/v1/node_pb";
import type { Node } from "../gen/podium/v1/admin_pb";
import type { Task } from "../gen/podium/v1/task_pb";

export function toDate(ts?: Timestamp): Date | undefined {
  return ts ? timestampDate(ts) : undefined;
}

export function absolute(ts?: Timestamp): string {
  const d = toDate(ts);
  return d ? d.toLocaleString() : "—";
}

export function relative(ts: Timestamp | undefined, now = Date.now()): string {
  const d = toDate(ts);
  if (!d) return "never";
  const secs = Math.round((now - d.getTime()) / 1000);
  if (secs < 0) return "just now";
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

export function durationMs(ms: number): string {
  if (ms < 1000) return `${Math.max(ms, 0)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${Math.floor(s % 60)}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** taskDuration is finished-started, or now-started while it runs. */
export function taskDuration(
  started: Timestamp | undefined,
  finished: Timestamp | undefined,
  now = Date.now(),
): string {
  const from = toDate(started);
  if (!from) return "—";
  const to = toDate(finished)?.getTime() ?? now;
  return durationMs(to - from.getTime());
}

const TASK_STATUS_LABEL: Record<TaskStatus, string> = {
  [TaskStatus.UNSPECIFIED]: "unknown",
  [TaskStatus.QUEUED]: "queued",
  [TaskStatus.SCHEDULED]: "scheduled",
  [TaskStatus.PROVISIONING]: "provisioning",
  [TaskStatus.RUNNING]: "running",
  [TaskStatus.SUCCEEDED]: "succeeded",
  [TaskStatus.FAILED]: "failed",
  [TaskStatus.CANCELLED]: "cancelled",
  [TaskStatus.LOST]: "lost",
};

export function taskStatusLabel(s: TaskStatus): string {
  return TASK_STATUS_LABEL[s] ?? "unknown";
}

/** Colour is the only presentation logic the UI owns; everything else comes from the API. */
export function taskStatusTone(s: TaskStatus): Tone {
  switch (s) {
    case TaskStatus.SUCCEEDED:
      return "ok";
    case TaskStatus.FAILED:
      return "err";
    // lost is deliberately not err. Nothing about the task went wrong: the machine running it
    // went away. Colouring it like a failure tells the operator to go and read logs that do not
    // exist, and hides the one thing that is actually true — the node is gone.
    case TaskStatus.LOST:
      return "lost";
    case TaskStatus.RUNNING:
    case TaskStatus.PROVISIONING:
      return "run";
    case TaskStatus.CANCELLED:
      return "warn";
    default:
      return "idle";
  }
}

export function isTerminal(s: TaskStatus): boolean {
  return (
    s === TaskStatus.SUCCEEDED ||
    s === TaskStatus.FAILED ||
    s === TaskStatus.CANCELLED ||
    s === TaskStatus.LOST
  );
}

export const TASK_STATUS_FILTERS: TaskStatus[] = [
  TaskStatus.QUEUED,
  TaskStatus.SCHEDULED,
  TaskStatus.PROVISIONING,
  TaskStatus.RUNNING,
  TaskStatus.SUCCEEDED,
  TaskStatus.FAILED,
  TaskStatus.CANCELLED,
  TaskStatus.LOST,
];

const NODE_STATUS_LABEL: Record<NodeStatus, string> = {
  [NodeStatus.UNSPECIFIED]: "unknown",
  [NodeStatus.ONLINE]: "online",
  [NodeStatus.UNREACHABLE]: "unreachable",
  [NodeStatus.OFFLINE]: "offline",
  [NodeStatus.DRAINING]: "draining",
};

export function nodeStatusLabel(s: NodeStatus): string {
  return NODE_STATUS_LABEL[s] ?? "unknown";
}

export function nodeStatusTone(s: NodeStatus): "ok" | "err" | "warn" | "idle" {
  switch (s) {
    case NodeStatus.ONLINE:
      return "ok";
    case NodeStatus.UNREACHABLE:
      return "warn";
    case NodeStatus.OFFLINE:
      return "err";
    default:
      return "idle";
  }
}

/** messageTone: an answer reads as done, a progress note as still working. */
export function messageTone(type: string): Tone {
  if (type === "final") return "ok";
  if (type === "progress") return "run";
  return "idle";
}

const EVENT_KIND_LABEL: Record<TaskEventKind, string> = {
  [TaskEventKind.UNSPECIFIED]: "unknown",
  [TaskEventKind.PROVISIONING]: "provisioning",
  [TaskEventKind.PULLING]: "pulling",
  [TaskEventKind.STARTED]: "started",
  [TaskEventKind.LOG]: "log",
  [TaskEventKind.STEP]: "step",
  [TaskEventKind.ARTIFACT]: "artifact",
  [TaskEventKind.EXITED]: "exited",
  [TaskEventKind.FINISHED]: "finished",
  [TaskEventKind.ERROR]: "error",
  [TaskEventKind.MESSAGE]: "message",
};

export function eventKindLabel(k: TaskEventKind): string {
  return EVENT_KIND_LABEL[k] ?? "unknown";
}

/** humanBytes matches the CLI's rendering, so a size reads the same in both. */
export function humanBytes(n: bigint | number): string {
  const v = Number(n);
  const unit = 1024;
  if (v < unit) return `${v} B`;
  let div = unit;
  let exp = 0;
  for (let x = Math.floor(v / unit); x >= unit; x = Math.floor(x / unit)) {
    div *= unit;
    exp++;
  }
  return `${(v / div).toFixed(1)} ${"KMGTPE"[exp]}B`;
}

/**
 * nodeStateLabel is the node's status *and* the operator's standing drain instruction, which
 * are separate things: a drained node that has gone offline is `offline (draining)`, and an
 * operator who sees only one of the two cannot tell why nothing is being scheduled on it.
 */
export function nodeStateLabel(n: Node): string {
  const status = nodeStatusLabel(n.status);
  if (n.draining && n.status !== NodeStatus.DRAINING) return `${status} (draining)`;
  return status;
}

export function nodeStateTone(n: Node): Tone {
  if (n.draining || n.status === NodeStatus.DRAINING) return "warn";
  return nodeStatusTone(n.status);
}

/**
 * taskOutcome is the sentence under a terminal task's badge: what happened, in words, when the
 * status alone does not say it. `lost` and `failed` are given different words on purpose.
 */
export function taskOutcome(task: Task): string {
  const reason = task.failureReason;
  switch (task.status) {
    case TaskStatus.LOST:
      return task.nodeId
        ? `The node running this task (${task.nodeId}) went away before it finished. ` +
            "Nothing about the task itself failed" +
            (reason ? ` — ${reason}.` : ".") +
            " Re-running it is your call; set retry_on_node_loss to have Podium do it."
        : "The node running this task went away before it finished. Nothing about the task itself failed.";
    case TaskStatus.FAILED:
      if (reason === "oom") {
        return "Killed for running out of memory. Raise resources.memory_mb, or make the task use less.";
      }
      if (reason === "timeout") return "Stopped for exceeding the task's timeout.";
      if (reason) return reason;
      if (task.exitCode !== undefined) return `The command exited ${task.exitCode}.`;
      return "The task failed without an exit code.";
    case TaskStatus.CANCELLED:
      return reason || "Cancelled by an operator.";
    case TaskStatus.SUCCEEDED:
      return "";
    default:
      return reason;
  }
}

/**
 * queuedExplanation answers "why is this still queued?" from the two fields the scheduler
 * writes when it looks at a task and cannot place it.
 */
export function queuedExplanation(task: Task, now = Date.now()): string | undefined {
  if (task.status !== TaskStatus.QUEUED) return undefined;
  if (task.queuedReason === "") {
    return task.lastScheduleAttemptAt
      ? "Waiting for the scheduler."
      : "Waiting for the scheduler to look at it for the first time.";
  }
  const when = task.lastScheduleAttemptAt
    ? ` (last checked ${relative(task.lastScheduleAttemptAt, now)})`
    : "";
  return `${task.queuedReason}${when}.`;
}
