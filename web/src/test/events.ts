import { create } from "@bufbuild/protobuf";
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

export const stdout = LogChunk_Stream.STDOUT;
export const stderr = LogChunk_Stream.STDERR;
