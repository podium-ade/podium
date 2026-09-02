import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LogViewer } from "./LogViewer";
import { LogChunk_Stream } from "../gen/podium/v1/node_pb";
import { Accumulator, type LogLine } from "../lib/logs";
import { logEvent, stderr, stdout } from "../test/events";

type Chunk = [number, LogChunk_Stream, string];

function build(events: readonly Chunk[]): LogLine[] {
  const acc = new Accumulator();
  return events.flatMap(([seq, stream, text]) => acc.append(logEvent(seq, stream, text)));
}

const mixed = build([
  [1, stdout, "tick 1\n"],
  [2, stderr, "warn: disk\n"],
  [3, stdout, "tick 2\n"],
  [4, stdout, "tick 3\n"],
]);

function texts() {
  return screen.getAllByTestId("log-line").map((el) => el.textContent);
}

/** jsdom does no layout, so the test supplies the geometry the virtualizer reads. */
function withGeometry(el: HTMLElement, clientHeight: number, scrollHeight: number) {
  Object.defineProperty(el, "clientHeight", { value: clientHeight, configurable: true });
  Object.defineProperty(el, "scrollHeight", { value: scrollHeight, configurable: true });
}

describe("LogViewer", () => {
  it("renders lines in seq order with the stream on each row", () => {
    render(<LogViewer lines={mixed} phase="streaming" taskId="task_1" />);
    expect(texts()).toEqual(["tick 1", "warn: disk", "tick 2", "tick 3"]);
    expect(screen.getAllByTestId("log-line").map((el) => el.dataset.stream)).toEqual([
      "stdout",
      "stderr",
      "stdout",
      "stdout",
    ]);
  });

  it("filters by stream", async () => {
    const user = userEvent.setup();
    render(<LogViewer lines={mixed} phase="streaming" taskId="task_1" />);

    await user.click(screen.getByLabelText("stdout"));
    expect(texts()).toEqual(["warn: disk"]);

    await user.click(screen.getByLabelText("stderr"));
    expect(screen.queryAllByTestId("log-line")).toHaveLength(0);
    expect(screen.getByText("no lines match the filter")).toBeInTheDocument();

    await user.click(screen.getByLabelText("stdout"));
    expect(texts()).toEqual(["tick 1", "tick 2", "tick 3"]);
  });

  it("searches within the visible streams and reports the count", async () => {
    const user = userEvent.setup();
    render(<LogViewer lines={mixed} phase="streaming" taskId="task_1" />);
    await user.type(screen.getByLabelText("Search logs"), "tick");
    expect(texts()).toEqual(["tick 1", "tick 2", "tick 3"]);
    expect(screen.getByText("3 / 4 lines")).toBeInTheDocument();
  });

  it("virtualizes: only a window of a long log is in the DOM", () => {
    const many = build(
      Array.from({ length: 5000 }, (_, i): Chunk => [i + 1, stdout, `line ${i}\n`]),
    );
    render(<LogViewer lines={many} phase="streaming" taskId="task_1" />);
    const scroller = screen.getByTestId("log-scroll");
    withGeometry(scroller, 180, many.length * 18);
    fireEvent.scroll(scroller, { target: { scrollTop: 0 } });

    const rows = screen.getAllByTestId("log-line");
    expect(rows.length).toBeLessThan(40);
    expect(rows[0]).toHaveTextContent("line 0");
  });

  it("drops follow when the user scrolls up and restores it at the bottom", () => {
    const many = build(
      Array.from({ length: 500 }, (_, i): Chunk => [i + 1, stdout, `line ${i}\n`]),
    );
    render(<LogViewer lines={many} phase="streaming" taskId="task_1" />);
    const scroller = screen.getByTestId("log-scroll");
    const follow = screen.getByLabelText("follow") as HTMLInputElement;
    withGeometry(scroller, 180, 500 * 18);

    expect(follow.checked).toBe(true);

    fireEvent.scroll(scroller, { target: { scrollTop: 0 } });
    expect(follow.checked).toBe(false);
    expect(screen.getAllByTestId("log-line")[0]).toHaveTextContent("line 0");

    fireEvent.scroll(scroller, { target: { scrollTop: 500 * 18 - 180 } });
    expect(follow.checked).toBe(true);
  });

  it("follow scrolls a newly appended line into view", () => {
    const many = build(
      Array.from({ length: 100 }, (_, i): Chunk => [i + 1, stdout, `line ${i}\n`]),
    );
    const { rerender } = render(<LogViewer lines={many} phase="streaming" taskId="task_1" />);
    const scroller = screen.getByTestId("log-scroll");
    withGeometry(scroller, 180, 101 * 18);

    rerender(
      <LogViewer
        lines={[...many, { id: 999, seq: 101n, stream: "stdout", source: "", text: "last" }]}
        phase="streaming"
        taskId="task_1"
      />,
    );
    expect(scroller.scrollTop).toBe(101 * 18);
  });

  it("says so when there is no output at all", () => {
    render(<LogViewer lines={[]} phase="connecting" taskId="task_1" />);
    expect(screen.getByText("no output yet")).toBeInTheDocument();
    expect(screen.getByText("connecting…")).toBeInTheDocument();
  });
});
