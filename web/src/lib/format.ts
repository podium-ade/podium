import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { NodeStatus, TaskStatus, type TaskSpec } from "../gen/podium/v1/common_pb";
import { TaskEventKind } from "../gen/podium/v1/node_pb";

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
export function taskStatusTone(s: TaskStatus): "ok" | "err" | "run" | "idle" | "warn" {
  switch (s) {
    case TaskStatus.SUCCEEDED:
      return "ok";
    case TaskStatus.FAILED:
    case TaskStatus.LOST:
      return "err";
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
};

export function eventKindLabel(k: TaskEventKind): string {
  return EVENT_KIND_LABEL[k] ?? "unknown";
}

function yamlScalar(v: string): string {
  return /^[A-Za-z0-9_./:@-]+$/.test(v) && v !== "" ? v : JSON.stringify(v);
}

/** specToYaml renders the defaulted spec the server echoed back. Display only. */
export function specToYaml(spec?: TaskSpec): string {
  if (!spec) return "# no spec";
  const out: string[] = [`image: ${yamlScalar(spec.image)}`];
  if (spec.command.length > 0) {
    out.push("command:");
    for (const c of spec.command) out.push(`  - ${yamlScalar(c)}`);
  }
  if (spec.workingDir) out.push(`working_dir: ${yamlScalar(spec.workingDir)}`);
  const env = Object.keys(spec.env).sort();
  if (env.length > 0) {
    out.push("env:");
    for (const k of env) out.push(`  ${k}: ${yamlScalar(spec.env[k])}`);
  }
  if (spec.labels.length > 0) {
    out.push("labels:");
    for (const l of spec.labels) out.push(`  - ${yamlScalar(l)}`);
  }
  if (spec.timeout) out.push(`timeout: ${Number(spec.timeout.seconds)}s`);
  if (spec.maxAttempts) out.push(`max_attempts: ${spec.maxAttempts}`);
  return out.join("\n");
}
