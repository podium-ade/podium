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

function Search() {
  return <p data-testid="search">{useLocation().search}</p>;
}

function mount(at = "/agent/linear") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[at]}>
        <Routes>
          <Route
            path="/agent/linear"
            element={
              <>
                <LinearTable />
                <Search />
              </>
            }
          />
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
    localStorage.clear();
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

type Req = { linearStateType?: string; mine?: boolean; page?: { limit?: number; cursor?: string } };

/** cards is n issues of one column, numbered from `from`. */
const cards = (type: string, n: number, from = 1) =>
  Array.from({ length: n }, (_, i) =>
    conversation({
      chatId: `chat_${type}_${from + i}`,
      identifier: `ENG-${from + i}`,
      title: `${type} issue ${from + i}`,
      linearStateType: type,
    }),
  );

describe("LinearTable as a board", () => {
  beforeEach(() => {
    localStorage.clear();
    listLinearConversations.mockReset();
    // In progress holds 23: ten a page. Todo holds one; every other column none.
    listLinearConversations.mockImplementation(async (req: Req) => {
      const page = { conversations: [] as unknown[], nextCursor: "", teams: ["ENG"], total: 0 };
      if (req.linearStateType === "started") {
        if (!req.page?.cursor) return { ...page, conversations: cards("started", 10), nextCursor: "c1", total: 23 };
        if (req.page.cursor === "c1") return { ...page, conversations: cards("started", 10, 11), nextCursor: "c2" };
        return { ...page, conversations: cards("started", 3, 21) };
      }
      if (req.linearStateType === "unstarted") return { ...page, conversations: cards("unstarted", 1, 90), total: 1 };
      if (!req.linearStateType) return { ...page, conversations: [conversation()] };
      return page;
    });
  });

  const column = (type: string) => screen.getByTestId(`linear-column-${type}`);

  it("switches to Linear's states as columns, and says so in the URL and to the browser", async () => {
    mount();
    expect(await screen.findAllByTestId("linear-row")).not.toHaveLength(0);
    await userEvent.click(screen.getByRole("radio", { name: "Board" }));

    expect(await screen.findByTestId("linear-board")).toBeInTheDocument();
    expect(screen.queryByTestId("linear-row")).not.toBeInTheDocument();
    expect(screen.getByTestId("search")).toHaveTextContent("?view=board");
    expect(localStorage.getItem("podium.linear.view")).toBe("board");

    const headings = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(headings).toEqual(["Backlog", "Todo", "In progress", "Done", "Canceled"]);
    expect(screen.queryByTestId("linear-column-triage")).not.toBeInTheDocument();
    expect(screen.queryByTestId("linear-column-none")).not.toBeInTheDocument();
    await waitFor(() => expect(within(column("started")).getAllByTestId("linear-card")).toHaveLength(10));
    expect(within(column("started")).getByTestId("linear-column-count")).toHaveTextContent("23");
    expect(within(column("backlog")).getByText("None")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "Table" }));
    expect(await screen.findAllByTestId("linear-row")).not.toHaveLength(0);
    expect(screen.getByTestId("search")).toHaveTextContent(/^$/);
    expect(localStorage.getItem("podium.linear.view")).toBe("table");
  });

  it("opens on the board a link names", async () => {
    mount("/agent/linear?view=board");
    expect(await screen.findByTestId("linear-board")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Board" })).toHaveAttribute("aria-checked", "true");
  });

  it("opens on the board the browser was left on", async () => {
    localStorage.setItem("podium.linear.view", "board");
    mount();
    expect(await screen.findByTestId("linear-board")).toBeInTheDocument();
  });

  it("loads ten more of one column at a time", async () => {
    mount("/agent/linear?view=board");
    await waitFor(() => expect(within(column("started")).getAllByTestId("linear-card")).toHaveLength(10));
    for (const c of listLinearConversations.mock.calls as [Req][]) {
      expect(c[0].page?.limit).toBe(10);
    }
    expect(within(column("unstarted")).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();

    listLinearConversations.mockClear();
    await userEvent.click(within(column("started")).getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(within(column("started")).getAllByTestId("linear-card")).toHaveLength(20));
    expect(listLinearConversations).toHaveBeenCalledTimes(1);
    expect(listLinearConversations).toHaveBeenCalledWith(
      expect.objectContaining({ linearStateType: "started", page: { limit: 10, cursor: "c1" } }),
    );
    expect(within(column("unstarted")).getAllByTestId("linear-card")).toHaveLength(1);

    await userEvent.click(within(column("started")).getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(within(column("started")).getAllByTestId("linear-card")).toHaveLength(23));
    expect(within(column("started")).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("asks every column for the filters", async () => {
    mount("/agent/linear?view=board");
    await screen.findByTestId("linear-board");
    await userEvent.click(screen.getByRole("radio", { name: "Mine" }));
    await waitFor(() => {
      const asked = (listLinearConversations.mock.calls as [Req][])
        .filter((c) => c[0].mine)
        .map((c) => c[0].linearStateType);
      expect(new Set(asked)).toEqual(
        new Set(["triage", "backlog", "unstarted", "started", "completed", "canceled", "none"]),
      );
    });
  });

  it("opens an issue's conversation from its card, and the issue in Linear from its identifier", async () => {
    mount("/agent/linear?view=board");
    const todo = await waitFor(() => within(column("unstarted")).getByTestId("linear-card"));
    expect(within(todo).getByRole("link", { name: "ENG-90" })).toHaveAttribute(
      "href",
      "https://linear.app/acme/issue/ENG-12/fix-login",
    );
    expect(todo).toHaveTextContent("ENG");
    expect(todo).toHaveTextContent("working");
    expect(todo).toHaveTextContent("$1.25");
    await userEvent.click(within(todo).getByRole("link", { name: "Open the conversation of ENG-90 unstarted issue 90" }));
    expect(await screen.findByTestId("where")).toHaveTextContent("/agent/chat/chat_unstarted_90");
  });

  it("shows a column Linear's states lack only when something is in it", async () => {
    listLinearConversations.mockImplementation(async (req: Req) => ({
      conversations: req.linearStateType === "none" ? [conversation({ kind: "project", identifier: "", title: "Roadmap" })] : [],
      nextCursor: "",
      teams: [],
      total: req.linearStateType === "none" ? 1 : 0,
    }));
    mount("/agent/linear?view=board");
    expect(await screen.findByTestId("linear-column-none")).toHaveTextContent("Roadmap");
    expect(screen.getByTestId("linear-column-none")).toHaveTextContent("Project");
    expect(screen.queryByTestId("linear-column-triage")).not.toBeInTheDocument();
  });
});
