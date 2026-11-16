import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { LinearTable } from "./LinearTable";

const listLinearConversations = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listLinearConversations: (...a: unknown[]) => listLinearConversations(...a),
    },
  };
});

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/agent/linear"]}>
        <Routes>
          <Route path="/agent/linear" element={<LinearTable />} />
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const conversation = (over: Record<string, unknown> = {}) => ({
  chatId: "chat_eng12",
  kind: "issue",
  identifier: "ENG-12",
  title: "Fix login",
  url: "https://linear.app/acme/issue/ENG-12/fix-login",
  teamKey: "ENG",
  linearState: "In Progress",
  linearStateType: "started",
  state: "working",
  askedOf: "",
  lastActivityAt: timestampFromDate(new Date(Date.now() - 60_000)),
  lastText: "Working on the fix.",
  costUsd: 1.25,
  myRole: "viewer",
  mine: true,
  ...over,
});

describe("LinearTable", () => {
  beforeEach(() => {
    listLinearConversations.mockReset();
    listLinearConversations.mockResolvedValue({
      conversations: [
        conversation(),
        conversation({
          chatId: "chat_eng13",
          identifier: "ENG-13",
          title: "Flaky test",
          state: "waiting",
          askedOf: "ada@acme.com",
        }),
      ],
      nextCursor: "",
      teams: ["ENG", "OPS"],
    });
  });

  it("lists one row per issue, with what Linear and Podium each say about it", async () => {
    mount();
    const rows = await screen.findAllByTestId("linear-row");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("ENG-12");
    expect(rows[0]).toHaveTextContent("Fix login");
    expect(rows[0]).toHaveTextContent("In Linear: In Progress");
    expect(rows[0]).toHaveTextContent("working");
    expect(rows[0]).toHaveTextContent("$1.25");
    expect(within(rows[0]).getByRole("link", { name: /ENG-12/ })).toHaveAttribute(
      "href",
      "https://linear.app/acme/issue/ENG-12/fix-login",
    );
    expect(rows[1]).toHaveTextContent("waiting on someone");
    expect(rows[1]).toHaveTextContent("asked of ada@acme.com");
  });

  it("opens the issue's conversation from its row", async () => {
    mount();
    const rows = await screen.findAllByTestId("linear-row");
    await userEvent.click(rows[0]);
    expect(await screen.findByTestId("where")).toHaveTextContent("/agent/chat/chat_eng12");
  });

  it("asks the server for the filters, not the page in hand", async () => {
    mount();
    await screen.findAllByTestId("linear-row");
    expect(listLinearConversations).toHaveBeenLastCalledWith(
      expect.objectContaining({ mine: false, team: "", state: "" }),
    );
    await userEvent.click(screen.getByRole("radio", { name: "Mine" }));
    await waitFor(() =>
      expect(listLinearConversations).toHaveBeenLastCalledWith(expect.objectContaining({ mine: true })),
    );
    await userEvent.click(screen.getByRole("radio", { name: "Needs attention" }));
    await waitFor(() =>
      expect(listLinearConversations).toHaveBeenLastCalledWith(
        expect.objectContaining({ mine: true, state: "attention" }),
      ),
    );
  });

  it("pages on the server", async () => {
    listLinearConversations.mockResolvedValueOnce({
      conversations: [conversation()],
      nextCursor: "cursor-1",
      teams: ["ENG"],
    });
    listLinearConversations.mockResolvedValueOnce({
      conversations: [conversation({ chatId: "chat_eng14", identifier: "ENG-14" })],
      nextCursor: "",
      teams: ["ENG"],
    });
    mount();
    await screen.findAllByTestId("linear-row");
    await userEvent.click(screen.getByRole("button", { name: "Show more" }));
    await waitFor(() => expect(screen.getAllByTestId("linear-row")).toHaveLength(2));
    expect(listLinearConversations).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: expect.objectContaining({ cursor: "cursor-1" }) }),
    );
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
  });

  it("says how an issue gets here when there is none", async () => {
    listLinearConversations.mockResolvedValue({ conversations: [], nextCursor: "", teams: [] });
    mount();
    expect(await screen.findByText("No Linear issues yet.")).toBeInTheDocument();
  });
});
