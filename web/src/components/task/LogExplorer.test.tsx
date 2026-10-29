import { useState } from "react";
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LogExplorer } from "./LogExplorer";
import { LogChunk_Stream, type TaskEvent } from "../../gen/podium/v1/node_pb";
import { EntryStream, type Entry } from "../../lib/entries";
import { activityEvent, at, errorEvent, logEvent, messageEvent, sidecarLogEvent, stderr, stdout } from "../../test/events";

type Chunk = [number, LogChunk_Stream, string];

function build(events: readonly (Chunk | TaskEvent)[]): Entry[] {
  const s = new EntryStream();
  return events.flatMap((ev) => s.append(Array.isArray(ev) ? logEvent(ev[0], ev[1], ev[2]) : ev));
}

const mixed = build([
  [1, stdout, "tick 1\n"],
  [2, stderr, "warn: disk\n"],
  [3, stdout, "tick 2\n"],
  [4, stdout, "tick 3\n"],
]);

/** Explorer holds the state the page keeps in the URL. */
function Explorer({ entries, initial, trace }: { entries: readonly Entry[]; initial?: string; trace?: boolean }) {
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<string | undefined>(initial);
  return (
    <LogExplorer
      entries={entries}
      phase="streaming"
      taskId="task_1"
      query={query}
      onQuery={setQuery}
      selected={selected}
      onSelect={setSelected}
      trace={trace}
    />
  );
}

function texts() {
  return screen.getAllByTestId("log-text").map((el) => el.textContent);
}

function chip(name: string) {
  return screen.getByRole("button", { name: new RegExp(`^${name} \\d+$`) });
}

/** jsdom does no layout, so the test supplies the geometry the virtualizer reads. */
function withGeometry(el: HTMLElement, clientHeight: number, scrollHeight: number) {
  Object.defineProperty(el, "clientHeight", { value: clientHeight, configurable: true });
  Object.defineProperty(el, "scrollHeight", { value: scrollHeight, configurable: true });
}

describe("LogExplorer", () => {
  it("renders every entry in order with its source and severity", () => {
    const entries = build([
      [1, stdout, "connecting\n"],
      sidecarLogEvent(2, "db", "ready\n"),
      messageEvent(3, "progress", "working"),
      errorEvent(4, "boom"),
    ]);
    render(<Explorer entries={entries} />);
    // A message and a node event lead with what they are, as a label.
    expect(texts()).toEqual(["connecting", "[db] ready", "progressworking", "errorboom"]);
    const rows = screen.getAllByTestId("log-line");
    expect(rows.map((r) => r.dataset.source)).toEqual(["stdout", "sidecar:db", "message", "node"]);
    expect(rows.map((r) => r.dataset.severity)).toEqual(["info", "info", "info", "error"]);
  });

  it("shows only a source when its chip is clicked, and counts what the query matched", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    expect(chip("stdout")).toHaveTextContent("3");

    await user.click(chip("stderr"));
    expect(texts()).toEqual(["warn: disk"]);
    expect(chip("stderr")).toHaveAttribute("aria-pressed", "true");
    expect(chip("stdout")).toHaveAttribute("aria-pressed", "false");

    // A second chip adds its group to what shows.
    await user.click(chip("stdout"));
    expect(texts()).toHaveLength(4);

    // Clicking a chosen chip takes it out again.
    await user.click(chip("stdout"));
    expect(texts()).toEqual(["warn: disk"]);
    await user.click(chip("stderr"));
    expect(texts()).toHaveLength(4);
  });

  it("clears every chip with 'Show all'", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.click(chip("stderr"));
    await user.click(chip("warn"));
    await user.click(screen.getByRole("button", { name: "Show all" }));
    expect(texts()).toHaveLength(4);
    expect(chip("stderr")).toHaveAttribute("aria-pressed", "false");
  });

  it("combines the severity and source rows", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.click(chip("info"));
    expect(texts()).toEqual(["tick 1", "tick 2", "tick 3"]);
    await user.click(chip("stderr"));
    expect(screen.getByText("nothing matches the filter")).toBeInTheDocument();
  });

  it("searches with the query language and reports the count", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.type(screen.getByLabelText("Search logs"), "tick -/2/");
    expect(texts()).toEqual(["tick 1", "tick 3"]);
    expect(screen.getByText("2 / 4 entries")).toBeInTheDocument();
  });

  it("says which term it could not read", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.type(screen.getByLabelText("Search logs"), "severity:loud");
    expect(screen.getByText(/no severity called loud/)).toBeInTheDocument();
  });

  it("opens a row's whole record, and a tool call's input and output", async () => {
    const user = userEvent.setup();
    const entries = build([
      activityEvent(1, {
        kind: "tool",
        tool: "bash",
        status: "completed",
        input: JSON.stringify({ command: "cat review.json" }),
        output: '{"verdict":"approve"}',
      }),
    ]);
    render(<Explorer entries={entries} />);
    expect(texts()).toEqual(["cat review.json"]);

    await user.click(screen.getByTestId("log-line"));
    const detail = screen.getByTestId("entry-detail");
    expect(screen.getByTestId("log-line")).toHaveAttribute("aria-selected", "true");
    // A tool call opens on what came back, under the line that asked for it.
    expect(within(detail).getByText("completed")).toBeInTheDocument();
    expect(within(detail).getByText((_, el) => el?.tagName === "P" && el.textContent === "$ cat review.json")).toBeInTheDocument();
    expect(within(detail).getByText('{"verdict":"approve"}')).toBeInTheDocument();

    await user.click(within(detail).getByRole("tab", { name: "input" }));
    expect(within(detail).getByText("command")).toBeInTheDocument();

    await user.click(within(detail).getByRole("tab", { name: "fields" }));
    expect(within(detail).getAllByText("tool:bash").length).toBeGreaterThan(0);
    // Fields that only repeat another are not listed twice.
    expect(within(detail).queryByText("activity.tool")).toBeNull();

    // Selecting the row again lets go of it; so does the panel's close.
    await user.click(screen.getByTestId("log-line"));
    expect(screen.queryByTestId("entry-detail")).toBeNull();
    await user.click(screen.getByTestId("log-line"));
    await user.click(within(screen.getByTestId("entry-detail")).getByRole("button", { name: "Close" }));
    expect(screen.queryByTestId("entry-detail")).toBeNull();
  });

  it("walks the rows with the arrow keys, the record following, and lets go on Escape", () => {
    render(<Explorer entries={mixed} />);
    const list = screen.getByTestId("log-scroll");
    const selected = () => screen.queryAllByTestId("log-line").find((r) => r.getAttribute("aria-selected") === "true")?.dataset.key;

    fireEvent.keyDown(list, { key: "ArrowDown" });
    expect(selected()).toBe("1.0");
    fireEvent.keyDown(list, { key: "ArrowDown" });
    fireEvent.keyDown(list, { key: "ArrowDown" });
    expect(selected()).toBe("3.0");
    expect(within(screen.getByTestId("entry-detail")).getByText("tick 2")).toBeInTheDocument();
    fireEvent.keyDown(list, { key: "ArrowUp" });
    expect(selected()).toBe("2.0");
    fireEvent.keyDown(list, { key: "Escape" });
    expect(selected()).toBeUndefined();
    expect(screen.queryByTestId("entry-detail")).toBeNull();
  });

  it("resizes the panel from its handle, remembers the width, and resets it", async () => {
    const user = userEvent.setup();
    localStorage.removeItem("podium.logs.panelWidth");
    render(<Explorer entries={mixed} />);
    await user.click(screen.getAllByTestId("log-line")[0]);
    const handle = screen.getByRole("separator", { name: "Resize the entry panel" });
    expect(handle).not.toHaveAttribute("aria-valuenow");

    // jsdom lays nothing out, so the split clamps every width to the narrowest allowed.
    fireEvent.keyDown(handle, { key: "ArrowLeft" });
    expect(handle).toHaveAttribute("aria-valuenow", "320");
    expect(localStorage.getItem("podium.logs.panelWidth")).toBe("320");

    fireEvent.doubleClick(handle);
    expect(handle).not.toHaveAttribute("aria-valuenow");
    expect(localStorage.getItem("podium.logs.panelWidth")).toBeNull();
  });

  it("filters from a field of the open record, and takes the filter out again", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.click(screen.getAllByTestId("log-line")[1]);
    await user.click(screen.getByRole("tab", { name: "fields" }));
    await user.click(screen.getByRole("button", { name: "Show only entries with source stderr" }));
    expect(screen.getByLabelText("Search logs")).toHaveValue("source:stderr");
    expect(texts()).toEqual(["warn: disk"]);

    const on = screen.getByRole("button", { name: "Stop showing only entries with source stderr" });
    expect(on).toHaveAttribute("aria-pressed", "true");
    await user.click(on);
    expect(screen.getByLabelText("Search logs")).toHaveValue("");
    expect(texts()).toHaveLength(4);
  });

  it("excludes by a field of the open record, in place of showing only it", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.click(screen.getAllByTestId("log-line")[1]);
    await user.click(screen.getByRole("tab", { name: "fields" }));
    await user.click(screen.getByRole("button", { name: "Show only entries with source stderr" }));
    await user.click(screen.getByRole("button", { name: "Hide entries with source stderr" }));
    expect(screen.getByLabelText("Search logs")).toHaveValue("-source:stderr");
    expect(texts()).toEqual(["tick 1", "tick 2", "tick 3"]);
  });

  it("marks only the lines a tool call printed, and only when opened from the transcript", async () => {
    const entries = build([
      messageEvent(1, "progress", "running tests"),
      [2, stdout, "=== RUN TestA\n"],
      [3, stdout, "--- PASS\n"],
      activityEvent(4, { kind: "tool", tool: "bash", status: "completed" }),
      [5, stdout, "after\n"],
    ]);
    const { unmount } = render(<Explorer entries={entries} initial="4" />);
    // A pasted link is somebody pointing at a row, not tracing a step: nothing is marked.
    expect(screen.getAllByTestId("log-line").some((r) => r.dataset.band)).toBe(false);
    unmount();

    render(<Explorer entries={entries} initial="4" trace />);
    const banded = () => screen.getAllByTestId("log-line").filter((r) => r.dataset.band).map((r) => r.dataset.key);
    expect(banded()).toEqual(["2.0", "3.0"]);
    expect(screen.getByText(/2 lines in the list were printed while this ran/)).toBeInTheDocument();
    expect(screen.getAllByTestId("log-line").find((r) => r.dataset.key === "4")).toHaveAttribute("aria-selected", "true");

    // A click inside the list is somebody reading, not a step to trace: it marks nothing.
    const user = userEvent.setup();
    await user.click(screen.getAllByTestId("log-line").find((r) => r.dataset.key === "1")!);
    expect(banded()).toEqual([]);
  });

  it("brings back a row hidden by the filter with 'Show surrounding'", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.type(screen.getByLabelText("Search logs"), "disk");
    await user.click(screen.getByTestId("log-line"));
    await user.click(screen.getByRole("button", { name: /view in context/i }));
    expect(texts()).toHaveLength(4);
    expect(screen.getByLabelText("Search logs")).toHaveValue("");
    expect(screen.getAllByTestId("log-line").find((r) => r.dataset.key === "2.0")).toHaveAttribute("aria-selected", "true");
  });

  it("virtualizes: only a window of a long log is in the DOM", () => {
    const many = build(Array.from({ length: 5000 }, (_, i): Chunk => [i + 1, stdout, `line ${i}\n`]));
    render(<Explorer entries={many} />);
    const scroller = screen.getByTestId("log-scroll");
    withGeometry(scroller, 280, many.length * 28);
    fireEvent.scroll(scroller, { target: { scrollTop: 0 } });

    const rows = screen.getAllByTestId("log-text");
    expect(rows.length).toBeLessThan(40);
    expect(rows[0]).toHaveTextContent("line 0");
  });

  it("drops follow when the user scrolls up and restores it at the bottom", () => {
    const many = build(Array.from({ length: 500 }, (_, i): Chunk => [i + 1, stdout, `line ${i}\n`]));
    render(<Explorer entries={many} />);
    const scroller = screen.getByTestId("log-scroll");
    const follow = screen.getByRole("switch", { name: "Follow" });
    withGeometry(scroller, 280, 500 * 28);

    expect(follow).toBeChecked();
    fireEvent.scroll(scroller, { target: { scrollTop: 0 } });
    expect(follow).not.toBeChecked();
    fireEvent.scroll(scroller, { target: { scrollTop: 500 * 28 - 280 } });
    expect(follow).toBeChecked();
  });

  it("jumps to the first error, and offers nothing to jump to without one", () => {
    const noise = Array.from({ length: 200 }, (_, i): Chunk => [i + 1, stdout, `line ${i}\n`]);
    const { rerender } = render(<Explorer entries={build(noise)} />);
    expect(screen.queryByRole("button", { name: /first error/i })).toBeNull();
    // A warning is not an error: stderr alone offers no jump.
    rerender(<Explorer entries={build([...noise, [201, stderr, "warn\n"]])} />);
    expect(screen.queryByRole("button", { name: /first (error|warning)/i })).toBeNull();

    const withError = build([...noise, [201, stderr, "warn\n"], errorEvent(202, "boom")]);
    rerender(<Explorer entries={withError} />);
    const scroller = screen.getByTestId("log-scroll");
    withGeometry(scroller, 280, withError.length * 28);

    fireEvent.click(screen.getByRole("button", { name: /first error/i }));
    // Three rows of context above the error, so the lines that led to it are on screen too.
    expect(scroller.scrollTop).toBe((201 - 3) * 28);
    expect(screen.getByTestId("entry-detail")).toBeInTheDocument();
  });

  it("draws the run on its lanes and opens a step from its mark", async () => {
    const user = userEvent.setup();
    const entries = build([
      at(activityEvent(1, { kind: "tool", tool: "bash", status: "completed", input: '{"command":"ls"}' }), 1_000),
      at(logEvent(2, stdout, "out\n"), 2_000),
      at(activityEvent(3, { kind: "tool", tool: "read", status: "error", error: "no such file" }), 3_000),
      at(messageEvent(4, "final", "done"), 4_000),
    ]);
    render(<Explorer entries={entries} />);
    const timeline = screen.getByTestId("run-timeline");
    expect(within(timeline).getByText("tools")).toBeInTheDocument();
    expect(within(timeline).getByText("agent")).toBeInTheDocument();
    expect(within(timeline).getByText("output")).toBeInTheDocument();
    const marks = within(timeline).getAllByTestId("timeline-mark");
    // Lane by lane, top to bottom: the agent's answer, then the two tool calls.
    expect(marks.map((m) => m.dataset.key)).toEqual(["4", "1", "3"]);

    await user.click(marks[2]);
    expect(within(screen.getByTestId("entry-detail")).getByText("failed")).toBeInTheDocument();
    expect(screen.getAllByTestId("log-line").find((r) => r.dataset.key === "3")).toHaveAttribute("aria-selected", "true");
  });

  it("draws only what the filter keeps", async () => {
    const user = userEvent.setup();
    const entries = build([
      at(activityEvent(1, { kind: "tool", tool: "bash", status: "completed" }), 1_000),
      at(activityEvent(2, { kind: "tool", tool: "read", status: "completed" }), 2_000),
    ]);
    render(<Explorer entries={entries} />);
    await user.click(chip("tool:read"));
    expect(within(screen.getByTestId("run-timeline")).getAllByTestId("timeline-mark").map((m) => m.dataset.key)).toEqual(["2"]);
  });

  it("keeps every lane of the run when a filter empties one", async () => {
    const user = userEvent.setup();
    const entries = build([
      at(activityEvent(1, { kind: "tool", tool: "bash", status: "completed" }), 1_000),
      at(logEvent(2, stdout, "out\n"), 2_000),
      at(messageEvent(3, "progress", "hi"), 3_000),
    ]);
    render(<Explorer entries={entries} />);
    const lanes = () => ["tools", "agent", "output"].filter((l) => within(screen.getByTestId("run-timeline")).queryByText(l));
    expect(lanes()).toEqual(["tools", "agent", "output"]);
    await user.click(chip("message"));
    expect(lanes()).toEqual(["tools", "agent", "output"]);
  });

  it("says so when there is no output at all", () => {
    render(<Explorer entries={[]} />);
    expect(screen.getByText("no output yet")).toBeInTheDocument();
    expect(screen.getByText("live")).toBeInTheDocument();
  });
});
