import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { MemoryPanel } from "./MemoryPanel";
import { ToastHost } from "../Toast";

const listMemories = vi.fn();
const searchMemories = vi.fn();
const deleteMemory = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listMemories: (...a: unknown[]) => listMemories(...a),
      searchMemories: (...a: unknown[]) => searchMemories(...a),
      deleteMemory: (...a: unknown[]) => deleteMemory(...a),
    },
  };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter>
          <MemoryPanel />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

const memory = {
  id: "ed1bd235-bd25-483c-beff-4d54ff776e52",
  text: "Bob owns the Podium scheduler.",
  factType: "world",
  tags: ["source:slack", "playbook:general"],
  metadata: {
    session_id: "sess_01abc",
    turn_id: "turn_01abc",
    task_id: "task_01xyz",
    source_ref: "C0123/1725000000.000100/1725000000.000100",
    source_url: "https://example.slack.com/archives/C0123/p1725000000000100",
  },
  createdAt: timestampFromDate(new Date(Date.now() - 60_000)),
  entities: ["Bob", "scheduler"],
  context: "podium agent, playbook general",
  documentId: "turn_01abc",
};

describe("MemoryPanel", () => {
  beforeEach(() => {
    vi.useRealTimers();
    listMemories.mockReset();
    searchMemories.mockReset();
    deleteMemory.mockReset();
    listMemories.mockResolvedValue({ items: [memory], nextCursor: "" });
    searchMemories.mockResolvedValue({ items: [] });
    deleteMemory.mockResolvedValue({});
  });

  it("shows a memory with the provenance a human needs to judge it", async () => {
    mount();
    const row = await screen.findByTestId("memory-row");

    expect(row).toHaveTextContent("Bob owns the Podium scheduler.");
    expect(row).toHaveTextContent("world");
    expect(row).toHaveTextContent("general");
    expect(row).toHaveTextContent("1m ago");
    expect(row).toHaveTextContent("about Bob, scheduler");

    // The source chip links back to the conversation that produced it.
    expect(screen.getByRole("link", { name: "slack" })).toHaveAttribute(
      "href",
      "https://example.slack.com/archives/C0123/p1725000000000100",
    );
    // And the task it came out of is one click away, so the logs are too.
    expect(screen.getByRole("link", { name: "task_01xyz" })).toHaveAttribute(
      "href",
      "/tasks/task_01xyz",
    );
  });

  it("does not link a source it has no url for", async () => {
    listMemories.mockResolvedValue({
      items: [{ ...memory, metadata: { turn_id: "turn_01abc" } }],
      nextCursor: "",
    });
    mount();
    const row = await screen.findByTestId("memory-row");
    expect(row).toHaveTextContent("slack");
    expect(screen.queryByRole("link", { name: "slack" })).toBeNull();
    expect(screen.queryByRole("link", { name: /task_/ })).toBeNull();
  });

  it("says so rather than showing 1970 when the memory has no timestamp", async () => {
    listMemories.mockResolvedValue({
      items: [{ ...memory, createdAt: undefined }],
      nextCursor: "",
    });
    mount();
    expect(await screen.findByText("learned at an unknown time")).toBeInTheDocument();
  });

  it("searches instead of listing once something is typed", async () => {
    searchMemories.mockResolvedValue({ items: [{ ...memory, text: "a searched fact" }] });
    mount();
    await screen.findByTestId("memory-row");

    await userEvent.type(screen.getByTestId("memory-search"), "who owns the scheduler");

    await waitFor(() =>
      expect(searchMemories).toHaveBeenCalledWith({ query: "who owns the scheduler", limit: 25 }),
    );
    expect(await screen.findByText("a searched fact")).toBeInTheDocument();
  });

  it("says a search matched nothing without claiming the memory is empty", async () => {
    mount();
    await userEvent.type(screen.getByTestId("memory-search"), "nothing like this");
    expect(await screen.findByText("Nothing remembered matches that.")).toBeInTheDocument();
  });

  it("asks before forgetting, and the row goes when it is confirmed", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("memory-delete"));

    expect(
      screen.getByText("Forget this? Every future agent turn stops seeing it."),
    ).toBeInTheDocument();
    expect(deleteMemory).not.toHaveBeenCalled();

    listMemories.mockResolvedValue({ items: [], nextCursor: "" });
    await userEvent.click(screen.getByTestId("memory-delete-confirm"));

    await waitFor(() =>
      expect(deleteMemory).toHaveBeenCalledWith({ id: "ed1bd235-bd25-483c-beff-4d54ff776e52" }),
    );
    expect(await screen.findByRole("status")).toHaveTextContent("Forgotten");
    await waitFor(() => expect(screen.queryByTestId("memory-row")).toBeNull());
  });

  it("keeps the memory when the confirm is declined", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("memory-delete"));
    await userEvent.click(screen.getByRole("button", { name: "Keep" }));

    expect(deleteMemory).not.toHaveBeenCalled();
    expect(screen.getByTestId("memory-row")).toBeInTheDocument();
  });

  it("says the memory is not configured rather than showing an error", async () => {
    listMemories.mockRejectedValue(
      new ConnectError("memory is not configured on this host", Code.FailedPrecondition),
    );
    mount();
    expect(
      await screen.findByText("Memory is not configured on this host"),
    ).toBeInTheDocument();
    expect(screen.getByText(/PODIUM_AGENT_MEMORY_URL/)).toBeInTheDocument();
  });

  it("points a human at the empty memory rather than at nothing", async () => {
    listMemories.mockResolvedValue({ items: [], nextCursor: "" });
    mount();
    expect(await screen.findByText("Nothing remembered yet.")).toBeInTheDocument();
    // The warning is on the screen whether or not there is anything on it: it is the reason
    // the screen exists.
    expect(screen.getByText(/can try to plant a false memory/)).toBeInTheDocument();
  });

  it("keeps the list usable when the conductor is down", async () => {
    listMemories.mockRejectedValue(
      new ConnectError("podium-agent is not reachable", Code.Unavailable),
    );
    mount();
    expect(await screen.findByText(/podium-agent is not reachable/)).toBeInTheDocument();
    expect(screen.getByTestId("memory-search")).toBeInTheDocument();
  });

  it("loads the next page without losing the first", async () => {
    listMemories.mockResolvedValueOnce({ items: [memory], nextCursor: "25" });
    mount();
    await screen.findByTestId("memory-row");

    listMemories.mockReset();
    listMemories.mockImplementation(({ cursor }: { cursor: string }) =>
      cursor === ""
        ? Promise.resolve({ items: [memory], nextCursor: "25" })
        : Promise.resolve({ items: [{ ...memory, id: "second", text: "a later fact" }], nextCursor: "" }),
    );

    await userEvent.click(screen.getByRole("button", { name: "Load more" }));

    expect(await screen.findByText("a later fact")).toBeInTheDocument();
    expect(screen.getByText("Bob owns the Podium scheduler.")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("button", { name: "Load more" })).toBeNull());
  });
});
