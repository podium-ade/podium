import { describe, expect, it } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { NodeSchema } from "../gen/podium/v1/admin_pb";
import { NodeStatus, TaskStatus } from "../gen/podium/v1/common_pb";
import { TaskSchema } from "../gen/podium/v1/task_pb";
import {
  humanBytes,
  nodeStateLabel,
  nodeStateTone,
  queuedExplanation,
  taskOutcome,
  taskStatusTone,
} from "./format";

const NOW = Date.now();

function task(over: MessageInitShape<typeof TaskSchema> = {}) {
  return create(TaskSchema, { id: "task_01abc", ...over });
}

describe("humanBytes", () => {
  it("matches the CLI's rendering", () => {
    expect(humanBytes(0)).toBe("0 B");
    expect(humanBytes(512)).toBe("512 B");
    expect(humanBytes(2048)).toBe("2.0 KB");
    expect(humanBytes(1536n)).toBe("1.5 KB");
    expect(humanBytes(5 * 1024 * 1024)).toBe("5.0 MB");
    expect(humanBytes(3n * 1024n * 1024n * 1024n)).toBe("3.0 GB");
  });
});

describe("taskStatusTone", () => {
  it("gives lost a colour of its own, distinct from failed", () => {
    expect(taskStatusTone(TaskStatus.LOST)).toBe("lost");
    expect(taskStatusTone(TaskStatus.FAILED)).toBe("err");
    expect(taskStatusTone(TaskStatus.LOST)).not.toBe(taskStatusTone(TaskStatus.FAILED));
  });
});

describe("taskOutcome", () => {
  it("says a lost task was not a failure, and names the node", () => {
    const text = taskOutcome(
      task({ status: TaskStatus.LOST, nodeId: "node_01worker", failureReason: "node worker-3 went offline" }),
    );
    expect(text).toContain("node_01worker");
    expect(text).toContain("went away");
    expect(text).toContain("Nothing about the task itself failed");
    expect(text).toContain("retry_on_node_loss");
  });

  it("gives OOM its own sentence rather than the bare reason", () => {
    const text = taskOutcome(task({ status: TaskStatus.FAILED, failureReason: "oom" }));
    expect(text).toContain("out of memory");
    expect(text).toContain("resources.memory_mb");
  });

  it("explains a timeout", () => {
    expect(taskOutcome(task({ status: TaskStatus.FAILED, failureReason: "timeout" }))).toContain(
      "exceeding the task's timeout",
    );
  });

  it("renders any other failure reason verbatim", () => {
    expect(
      taskOutcome(task({ status: TaskStatus.FAILED, failureReason: 'missing secret "DB_PASSWORD"' })),
    ).toBe('missing secret "DB_PASSWORD"');
  });

  it("falls back to the exit code, then to saying there was none", () => {
    expect(taskOutcome(task({ status: TaskStatus.FAILED, exitCode: 3 }))).toBe(
      "The command exited 3.",
    );
    expect(taskOutcome(task({ status: TaskStatus.FAILED }))).toBe(
      "The task failed without an exit code.",
    );
  });

  it("has nothing to add about a task that succeeded", () => {
    expect(taskOutcome(task({ status: TaskStatus.SUCCEEDED, exitCode: 0 }))).toBe("");
  });

  it("carries the operator's own cancel reason", () => {
    expect(
      taskOutcome(task({ status: TaskStatus.CANCELLED, failureReason: "cancelled from the web UI" })),
    ).toBe("cancelled from the web UI");
  });
});

describe("queuedExplanation", () => {
  it("is only for queued tasks", () => {
    expect(queuedExplanation(task({ status: TaskStatus.RUNNING, queuedReason: "stale" }))).toBeUndefined();
  });

  it("gives the scheduler's reason and when it last looked", () => {
    const text = queuedExplanation(
      task({
        status: TaskStatus.QUEUED,
        queuedReason: "no online node carries every label this task requires (browser)",
        lastScheduleAttemptAt: timestampFromDate(new Date(NOW - 5000)),
      }),
      NOW,
    );
    expect(text).toBe(
      "no online node carries every label this task requires (browser) (last checked 5s ago).",
    );
  });

  it("says the scheduler has not looked yet when there is no reason", () => {
    expect(queuedExplanation(task({ status: TaskStatus.QUEUED }), NOW)).toBe(
      "Waiting for the scheduler to look at it for the first time.",
    );
  });
});

describe("nodeState", () => {
  it("shows drain and status as the two separate things they are", () => {
    expect(nodeStateLabel(create(NodeSchema, { status: NodeStatus.ONLINE }))).toBe("online");
    expect(
      nodeStateLabel(create(NodeSchema, { status: NodeStatus.OFFLINE, draining: true })),
    ).toBe("offline (draining)");
    // A connected node that is draining already says so in its status; do not say it twice.
    expect(
      nodeStateLabel(create(NodeSchema, { status: NodeStatus.DRAINING, draining: true })),
    ).toBe("draining");
  });

  it("tones a draining node as a warning whatever its status says", () => {
    expect(nodeStateTone(create(NodeSchema, { status: NodeStatus.ONLINE }))).toBe("ok");
    expect(
      nodeStateTone(create(NodeSchema, { status: NodeStatus.ONLINE, draining: true })),
    ).toBe("warn");
  });
});
