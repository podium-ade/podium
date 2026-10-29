import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Transcript } from "./Transcript";
import { phasesOf } from "../../lib/phases";
import { TaskStatus } from "../../gen/podium/v1/common_pb";
import { TaskEventKind, type TaskEvent } from "../../gen/podium/v1/node_pb";
import { TaskSchema } from "../../gen/podium/v1/task_pb";
import { EntryStream, type Entry } from "../../lib/entries";
import { activityEvent, at, errorEvent, exitedEvent, lifecycleEvent, logEvent, messageEvent, stdout } from "../../test/events";

function build(...events: TaskEvent[]): Entry[] {
  const s = new EntryStream();
  return events.flatMap((ev) => s.append(ev));
}

const run = build(
  lifecycleEvent(1, TaskEventKind.STARTED),
  logEvent(2, stdout, "noise\n"),
  messageEvent(3, "progress", "reviewing the diff"),
  activityEvent(4, { kind: "reasoning", text: "The change looks safe.\nIt only touches docs." }),
  activityEvent(5, {
    kind: "tool",
    tool: "bash",
    status: "completed",
    title: "printf …",
    input: JSON.stringify({ command: "cat /workspace/.podium/review.json" }),
    output: '{"verdict":"approve"}\n',
  }),
  activityEvent(6, { kind: "tool", tool: "read", status: "error", input: '{"filePath":"/nope"}', error: "no such file" }),
  messageEvent(7, "accounting", '{"num_turns":2}'),
  messageEvent(8, "final", "**Approved**: docs only."),
  errorEvent(9, "lease lost"),
);

describe("Transcript", () => {
  it("tells the story without the node's noise or the accounting", () => {
    render(<Transcript entries={run} live={false} onViewInLogs={() => {}} />);
    expect(screen.getAllByTestId("transcript-step").map((s) => s.dataset.kind)).toEqual([
      "message",
      "reasoning",
      "tool",
      "tool",
      "message",
      "error",
    ]);
    expect(screen.queryByText("noise")).toBeNull();
  });

  it("draws a tool call as one line, and opens it to its input and output", async () => {
    const user = userEvent.setup();
    render(<Transcript entries={run} live={false} onViewInLogs={() => {}} />);
    const [bash, read] = screen.getAllByTestId("transcript-tool");
    expect(bash).toHaveTextContent("bashcat /workspace/.podium/review.json");
    expect(read).toHaveAttribute("data-error", "true");

    await user.click(within(bash).getByText("cat /workspace/.podium/review.json"));
    expect(within(bash).getByText('{"verdict":"approve"}')).toBeInTheDocument();
  });

  it("reads the answer as markdown", () => {
    render(<Transcript entries={run} live={false} onViewInLogs={() => {}} />);
    expect(screen.getByText("Approved").tagName).toBe("STRONG");
  });

  it("opens the logs on a step", async () => {
    const user = userEvent.setup();
    const onViewInLogs = vi.fn();
    render(<Transcript entries={run} live={false} onViewInLogs={onViewInLogs} />);
    const [bash] = screen.getAllByTestId("transcript-step").filter((s) => s.dataset.kind === "tool");
    await user.click(within(bash).getByRole("button", { name: /view in logs/i }));
    expect(onViewInLogs).toHaveBeenCalledWith("5");
  });

  it("says so when nothing has been said", () => {
    render(<Transcript entries={build(logEvent(1, stdout, "x\n"))} live onViewInLogs={() => {}} />);
    expect(screen.getByText("Nothing said yet")).toBeInTheDocument();
  });
});

describe("phasesOf", () => {
  const task = create(TaskSchema, {
    status: TaskStatus.SUCCEEDED,
    createdAt: timestampFromMs(0),
    exitCode: 0,
  });

  it("measures each stage from its event to the next", () => {
    const entries = build(
      at(lifecycleEvent(1, TaskEventKind.PROVISIONING), 3_000),
      at(lifecycleEvent(2, TaskEventKind.PULLING), 5_000),
      at(lifecycleEvent(3, TaskEventKind.STARTED), 19_000),
      at(exitedEvent(4, 0), 79_000),
    );
    expect(phasesOf(task, entries, 100_000).map((p) => [p.name, p.to - p.from, p.open])).toEqual([
      ["Queued", 3_000, false],
      ["Provisioning", 2_000, false],
      ["Pulling", 14_000, false],
      ["Running", 60_000, false],
    ]);
  });

  it("leaves the stage a live run is in open, up to now", () => {
    const live = create(TaskSchema, { status: TaskStatus.RUNNING, createdAt: timestampFromMs(0) });
    const entries = build(at(lifecycleEvent(1, TaskEventKind.STARTED), 1_000));
    expect(phasesOf(live, entries, 4_000).map((p) => [p.name, p.to - p.from, p.open])).toEqual([
      ["Queued", 1_000, false],
      ["Running", 3_000, true],
    ]);
  });
});
