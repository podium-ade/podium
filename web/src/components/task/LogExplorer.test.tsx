import { useState } from "react";
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LogExplorer } from "./LogExplorer";
import { LogChunk_Stream, type TaskEvent } from "../../gen/podium/v1/node_pb";
import { EntryStream, type Entry } from "../../lib/entries";
import { activityEvent, errorEvent, logEvent, messageEvent, sidecarLogEvent, stderr, stdout } from "../../test/events";

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

  it("hides a source with its chip and counts what the query matched", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    expect(chip("stdout")).toHaveTextContent("3");

    await user.click(chip("stdout"));
    expect(texts()).toEqual(["warn: disk"]);
    expect(chip("stdout")).toHaveAttribute("aria-pressed", "false");

    await user.click(chip("stderr"));
    expect(screen.queryAllByTestId("log-line")).toHaveLength(0);
    expect(screen.getByText("nothing matches the filter")).toBeInTheDocument();
  });

  it("shows only one source on alt-click, and everything again on a second", () => {
    render(<Explorer entries={mixed} />);
    fireEvent.click(chip("stderr"), { altKey: true });
    expect(texts()).toEqual(["warn: disk"]);
    fireEvent.click(chip("stderr"), { altKey: true });
    expect(texts()).toHaveLength(4);
  });

  it("filters by severity", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.click(chip("info"));
    expect(texts()).toEqual(["warn: disk"]);
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
    expect(screen.getByTestId("log-line")).toHaveAttribute("aria-expanded", "true");
    expect(within(detail).getByText("Input")).toBeInTheDocument();
    expect(within(detail).getByText('{"verdict":"approve"}')).toBeInTheDocument();
    expect(within(detail).getByText("tool:bash")).toBeInTheDocument();

    await user.click(screen.getByTestId("log-line"));
    expect(screen.queryByTestId("entry-detail")).toBeNull();
  });

  it("keeps several rows open at once, each in place under its own line", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    const [a, , b] = screen.getAllByTestId("log-line");
    await user.click(a);
    await user.click(b);
    const entries = screen.getAllByTestId("log-entry");
    expect(entries.filter((e) => e.dataset.open).length).toBe(2);
    expect(within(entries[0]).getByTestId("entry-detail")).toBeInTheDocument();
    expect(within(entries[1]).queryByTestId("entry-detail")).toBeNull();
  });

  it("narrows the query from a field of an opened row", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.click(screen.getAllByTestId("log-line")[1]);
    await user.click(screen.getByRole("button", { name: "Show only entries with source stderr" }));
    expect(screen.getByLabelText("Search logs")).toHaveValue("source:stderr");
    expect(texts()).toEqual(["warn: disk"]);
  });

  it("excludes by a field of an opened row", async () => {
    const user = userEvent.setup();
    render(<Explorer entries={mixed} />);
    await user.click(screen.getAllByTestId("log-line")[1]);
    await user.click(screen.getByRole("button", { name: "Hide entries with source stderr" }));
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
    expect(screen.getByText(/2 lines above were printed while this ran/)).toBeInTheDocument();
    expect(screen.getAllByTestId("log-line").find((r) => r.dataset.key === "4")).toHaveAttribute("aria-current", "true");

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
    await user.click(screen.getByRole("button", { name: /show surrounding/i }));
    expect(texts()).toHaveLength(4);
    expect(screen.getByLabelText("Search logs")).toHaveValue("");
    expect(screen.getAllByTestId("log-line").find((r) => r.dataset.key === "2.0")).toHaveAttribute("aria-current", "true");
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

  it("jumps to the first error, and offers nothing to jump to without a problem", () => {
    const noise = Array.from({ length: 200 }, (_, i): Chunk => [i + 1, stdout, `line ${i}\n`]);
    const { rerender } = render(<Explorer entries={build(noise)} />);
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

  it("says so when there is no output at all", () => {
    render(<Explorer entries={[]} />);
    expect(screen.getByText("no output yet")).toBeInTheDocument();
    expect(screen.getByText("live")).toBeInTheDocument();
  });
});
