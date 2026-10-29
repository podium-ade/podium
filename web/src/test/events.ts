import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import {
  LogChunk_Stream,
  TaskEventKind,
  TaskEventSchema,
  type TaskEvent,
} from "../gen/podium/v1/node_pb";

const encoder = new TextEncoder();

/** logEvent builds the shape StreamTaskEvents delivers: raw bytes, not lines. */
export function logEvent(seq: number, stream: LogChunk_Stream, text: string): TaskEvent {
  return create(TaskEventSchema, {
    taskId: "task_test",
    seq: BigInt(seq),
    kind: TaskEventKind.LOG,
    payload: { case: "log", value: { stream, bytes: encoder.encode(text) } },
  });
}

/** sidecarLogEvent is the shape a sidecar's output arrives in: tagged with its name. */
export function sidecarLogEvent(seq: number, name: string, text: string): TaskEvent {
  return create(TaskEventSchema, {
    taskId: "task_test",
    seq: BigInt(seq),
    kind: TaskEventKind.LOG,
    payload: {
      case: "log",
      value: {
        stream: LogChunk_Stream.SIDECAR,
        sidecarName: name,
        bytes: encoder.encode(text),
      },
    },
  });
}

/** messageEvent is what `podium-runner message` becomes by the time the UI sees it. */
export function messageEvent(
  seq: number,
  type: string,
  text: string,
  attachments: string[] = [],
): TaskEvent {
  return create(TaskEventSchema, {
    taskId: "task_test",
    seq: BigInt(seq),
    kind: TaskEventKind.MESSAGE,
    payload: { case: "message", value: { type, text, attachments } },
  });
}

export const stdout = LogChunk_Stream.STDOUT;
export const stderr = LogChunk_Stream.STDERR;

/** at stamps an event with a time, in ms since the epoch: the explorer's clock reads it. */
export function at<T extends TaskEvent>(ev: T, ms: number): T {
  ev.ts = timestampFromMs(ms);
  return ev;
}

/** activityEvent is one tool call or reasoning block, as the agent runtime reports it. */
export function activityEvent(seq: number, activity: Record<string, unknown>): TaskEvent {
  return messageEvent(seq, "activity", JSON.stringify(activity));
}

/** exitedEvent is the container exiting with a code. */
export function exitedEvent(seq: number, exitCode: number, oomKilled = false): TaskEvent {
  return create(TaskEventSchema, {
    taskId: "task_test",
    seq: BigInt(seq),
    kind: TaskEventKind.EXITED,
    payload: { case: "exited", value: { exitCode, oomKilled } },
  });
}

/** errorEvent is the node reporting the run went wrong. */
export function errorEvent(seq: number, message: string, retryable = false): TaskEvent {
  return create(TaskEventSchema, {
    taskId: "task_test",
    seq: BigInt(seq),
    kind: TaskEventKind.ERROR,
    payload: { case: "error", value: { message, retryable } },
  });
}

/** lifecycleEvent is a stage with no payload: provisioning, pulling, started. */
export function lifecycleEvent(seq: number, kind: TaskEventKind): TaskEvent {
  return create(TaskEventSchema, { taskId: "task_test", seq: BigInt(seq), kind });
}
