import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { SessionsTable } from "./SessionsTable";

const listSessions = vi.fn();
const listTurns = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listSessions: (...a: unknown[]) => listSessions(...a),
      listTurns: (...a: unknown[]) => listTurns(...a),
    },
  };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <SessionsTable />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const session = {
  id: "sess_01abc",
  sourceKind: "slack",
  sourceKey: "slack:C0123:1725000000.000100",
  profile: "podium",
  playbook: "general",
  createdAt: timestampFromDate(new Date(Date.now() - 3_600_000)),
  lastTurnAt: timestampFromDate(new Date(Date.now() - 60_000)),
};

const turn = {
  id: "turn_01abc",
  sessionId: "sess_01abc",
  taskId: "task_01xyz",
  triggerRef: "C0123/1725000000.000100/1725000000.000100",
  status: "succeeded",
  startedAt: timestampFromDate(new Date(Date.now() - 120_000)),
  finishedAt: timestampFromDate(new Date(Date.now() - 60_000)),
  numTurns: 4,
  costUsd: 0.0123,
  finalText: "the answer",
};

describe("SessionsTable", () => {
  beforeEach(() => {
    listSessions.mockReset();
    listTurns.mockReset();
    listSessions.mockResolvedValue({ sessions: [session], nextCursor: "" });
    listTurns.mockResolvedValue({ turns: [turn] });
  });

  it("points at Slack when there is nothing to show yet", async () => {
    listSessions.mockResolvedValue({ sessions: [], nextCursor: "" });
    mount();
    expect(await screen.findByText("No sessions yet.")).toBeInTheDocument();
    expect(screen.getByText(/Mention the bot in Slack/)).toBeInTheDocument();
  });

  it("shows the source, a readable conversation ref and the playbook", async () => {
    mount();
    const row = await screen.findByTestId("session-row");
    expect(row).toHaveTextContent("slack");
    expect(row).toHaveTextContent("#C0123 · 1725000000.000100");
    expect(row).toHaveTextContent("general");
    expect(row).toHaveTextContent("1m ago");
  });

  it("opens a session's turns in a drawer and links each one to its task", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("session-row"));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    await waitFor(() => expect(listTurns).toHaveBeenCalledWith({ sessionId: "sess_01abc" }));

    const turnRow = await screen.findByTestId("turn-row");
    expect(turnRow).toHaveTextContent("succeeded");
    expect(turnRow).toHaveTextContent("$0.0123");
    expect(turnRow).toHaveTextContent("4 model turns");
    expect(turnRow).toHaveTextContent("the answer");
    expect(screen.getByRole("link", { name: "task_01xyz" })).toHaveAttribute(
      "href",
      "/tasks/task_01xyz",
    );
  });

  it("closes the drawer on Escape", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("session-row"));
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("says a turn has no task rather than linking nowhere", async () => {
    listTurns.mockResolvedValue({
      turns: [{ ...turn, taskId: "", status: "failed", numTurns: undefined, costUsd: undefined }],
    });
    mount();
    await userEvent.click(await screen.findByTestId("session-row"));
    const turnRow = await screen.findByTestId("turn-row");
    expect(turnRow).toHaveTextContent("no task");
    expect(turnRow).toHaveTextContent("failed");
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("reports a failure to list instead of rendering an empty table", async () => {
    listSessions.mockRejectedValue(new ConnectError("podium-agent is not reachable", Code.Unavailable));
    mount();
    expect(await screen.findByText("Could not list sessions")).toBeInTheDocument();
    expect(screen.getByText(/podium-agent is not reachable/)).toBeInTheDocument();
  });
});
