import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { WorkTable } from "./WorkTable";

const listWorkItems = vi.fn();

vi.mock("../../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../../lib/client")>("../../../lib/client");
  return {
    ...actual,
    agent: {
      listWorkItems: (...a: unknown[]) => listWorkItems(...a),
      listMcpServers: () => Promise.resolve({ servers: [] }),
    },
  };
});

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

function Search() {
  return <p data-testid="search">{useLocation().search}</p>;
}

function mount(at = "/agent/issues") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[at]}>
        <Routes>
          <Route
            path="/agent/issues"
            element={
              <>
                <WorkTable />
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

const item = (over: Record<string, unknown> = {}) => ({
  chatId: "chat_eng12",
  provider: "linear",
  kind: "issue",
  key: "ENG-12",
  title: "Fix login",
  url: "https://linear.app/acme/issue/ENG-12/fix-login",
  team: "ENG",
  status: "in_progress",
  statusLabel: "In Progress",
  state: "working",
  askedOf: "",
  lastActivityAt: timestampFromDate(new Date(Date.now() - 60_000)),
  lastText: "Working on the fix.",
  costUsd: 1.25,
  myRole: "viewer",
  mine: true,
  ...over,
});

const team = (key: string, provider = "linear") => ({ provider, key });

describe("WorkTable", () => {
  beforeEach(() => {
    localStorage.clear();
    listWorkItems.mockReset();
    listWorkItems.mockResolvedValue({
      items: [
        item(),
        item({
          chatId: "chat_eng13",
          key: "ENG-13",
          title: "Flaky test",
          state: "waiting",
          askedOf: "ada@acme.com",
        }),
      ],
      nextCursor: "",
      teams: [team("ENG"), team("OPS")],
      providers: ["linear"],
    });
  });

  it("lists one row per issue, with what its tracker and Podium each say about it", async () => {
    mount();
    const rows = await screen.findAllByTestId("work-row");
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

  it("marks each row with its tracker", async () => {
    mount();
    const rows = await screen.findAllByTestId("work-row");
    expect(within(rows[0]).getByRole("img", { name: "Linear" })).toBeInTheDocument();
    expect(within(rows[0]).getByRole("link", { name: /ENG-12/ })).toHaveAttribute("title", "Open in Linear");
  });

  it("offers no tracker filter while there is one tracker", async () => {
    mount();
    await screen.findAllByTestId("work-row");
    expect(screen.queryByTestId("work-provider")).not.toBeInTheDocument();
    expect(screen.getByTestId("work-team")).toBeInTheDocument();
  });

  it("offers a tracker filter once there are two", async () => {
    listWorkItems.mockResolvedValue({
      items: [item(), item({ chatId: "chat_gh1", provider: "github", key: "#7", team: "acme/web" })],
      nextCursor: "",
      teams: [team("ENG"), team("acme/web", "github")],
      providers: ["linear", "github"],
    });
    mount();
    const rows = await screen.findAllByTestId("work-row");
    expect(screen.getByTestId("work-provider")).toHaveTextContent("Every tracker");
    expect(within(rows[1]).getByTestId("work-provider-mark")).toHaveTextContent("github");
  });

  it("opens the issue's conversation from its row", async () => {
    mount();
    const rows = await screen.findAllByTestId("work-row");
    await userEvent.click(rows[0]);
    expect(await screen.findByTestId("where")).toHaveTextContent("/agent/chat/chat_eng12");
  });

  it("asks the server for the filters, not the page in hand", async () => {
    mount();
    await screen.findAllByTestId("work-row");
    expect(listWorkItems).toHaveBeenLastCalledWith(
      expect.objectContaining({ mine: false, team: "", state: "" }),
    );
    await userEvent.click(screen.getByRole("radio", { name: "Mine" }));
    await waitFor(() =>
      expect(listWorkItems).toHaveBeenLastCalledWith(expect.objectContaining({ mine: true })),
    );
    await userEvent.click(screen.getByRole("radio", { name: "Needs attention" }));
    await waitFor(() =>
      expect(listWorkItems).toHaveBeenLastCalledWith(
        expect.objectContaining({ mine: true, state: "attention" }),
      ),
    );
  });

  it("pages on the server", async () => {
    listWorkItems.mockResolvedValueOnce({
      items: [item()],
      nextCursor: "cursor-1",
      teams: [team("ENG")], providers: ["linear"],
    });
    listWorkItems.mockResolvedValueOnce({
      items: [item({ chatId: "chat_eng14", key: "ENG-14" })],
      nextCursor: "",
      teams: [team("ENG")], providers: ["linear"],
    });
    mount();
    await screen.findAllByTestId("work-row");
    await userEvent.click(screen.getByRole("button", { name: "Show more" }));
    await waitFor(() => expect(screen.getAllByTestId("work-row")).toHaveLength(2));
    expect(listWorkItems).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: expect.objectContaining({ cursor: "cursor-1" }) }),
    );
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
  });

  it("says how an issue gets here when there is none", async () => {
    listWorkItems.mockResolvedValue({ items: [], nextCursor: "", teams: [], providers: ["linear"] });
    mount();
    expect(await screen.findByText("No issues yet.")).toBeInTheDocument();
  });
});

type Req = { status?: string; mine?: boolean; page?: { limit?: number; cursor?: string } };

/** cards is n issues of one column, numbered from `from`. */
const cards = (status: string, n: number, from = 1) =>
  Array.from({ length: n }, (_, i) =>
    item({
      chatId: `chat_${status}_${from + i}`,
      key: `ENG-${from + i}`,
      title: `${status} issue ${from + i}`,
      status,
    }),
  );

describe("WorkTable as a board", () => {
  beforeEach(() => {
    localStorage.clear();
    listWorkItems.mockReset();
    // In progress holds 23: ten a page. Todo holds one; every other column none.
    listWorkItems.mockImplementation(async (req: Req) => {
      const page = { items: [] as unknown[], nextCursor: "", teams: [team("ENG")], providers: ["linear"], total: 0 };
      if (req.status === "in_progress") {
        if (!req.page?.cursor) return { ...page, items: cards("in_progress", 10), nextCursor: "c1", total: 23 };
        if (req.page.cursor === "c1") return { ...page, items: cards("in_progress", 10, 11), nextCursor: "c2" };
        return { ...page, items: cards("in_progress", 3, 21) };
      }
      if (req.status === "todo") return { ...page, items: cards("todo", 1, 90), total: 1 };
      if (!req.status) return { ...page, items: [item()] };
      return page;
    });
  });

  const column = (status: string) => screen.getByTestId(`work-column-${status}`);

  it("switches to statuses as columns, and says so in the URL and to the browser", async () => {
    mount();
    expect(await screen.findAllByTestId("work-row")).not.toHaveLength(0);
    await userEvent.click(screen.getByRole("radio", { name: "Board" }));

    expect(await screen.findByTestId("work-board")).toBeInTheDocument();
    expect(screen.queryByTestId("work-row")).not.toBeInTheDocument();
    expect(screen.getByTestId("search")).toHaveTextContent("?view=board");
    expect(localStorage.getItem("podium.work.view")).toBe("board");

    const headings = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(headings).toEqual(["Backlog", "Todo", "In progress", "Done", "Canceled"]);
    expect(screen.queryByTestId("work-column-triage")).not.toBeInTheDocument();
    expect(screen.queryByTestId("work-column-none")).not.toBeInTheDocument();
    await waitFor(() => expect(within(column("in_progress")).getAllByTestId("work-card")).toHaveLength(10));
    expect(within(column("in_progress")).getByTestId("work-column-count")).toHaveTextContent("23");
    expect(within(column("backlog")).getByText("None")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "Table" }));
    expect(await screen.findAllByTestId("work-row")).not.toHaveLength(0);
    expect(screen.getByTestId("search")).toHaveTextContent(/^$/);
    expect(localStorage.getItem("podium.work.view")).toBe("table");
  });

  it("opens on the board a link names", async () => {
    mount("/agent/issues?view=board");
    expect(await screen.findByTestId("work-board")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Board" })).toHaveAttribute("aria-checked", "true");
  });

  it("opens on the board the browser was left on", async () => {
    localStorage.setItem("podium.work.view", "board");
    mount();
    expect(await screen.findByTestId("work-board")).toBeInTheDocument();
  });

  it("loads ten more of one column at a time", async () => {
    mount("/agent/issues?view=board");
    await waitFor(() => expect(within(column("in_progress")).getAllByTestId("work-card")).toHaveLength(10));
    for (const c of listWorkItems.mock.calls as [Req][]) {
      expect(c[0].page?.limit).toBe(10);
    }
    expect(within(column("todo")).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();

    listWorkItems.mockClear();
    await userEvent.click(within(column("in_progress")).getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(within(column("in_progress")).getAllByTestId("work-card")).toHaveLength(20));
    expect(listWorkItems).toHaveBeenCalledTimes(1);
    expect(listWorkItems).toHaveBeenCalledWith(
      expect.objectContaining({ status: "in_progress", page: { limit: 10, cursor: "c1" } }),
    );
    expect(within(column("todo")).getAllByTestId("work-card")).toHaveLength(1);

    await userEvent.click(within(column("in_progress")).getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(within(column("in_progress")).getAllByTestId("work-card")).toHaveLength(23));
    expect(within(column("in_progress")).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("asks every column for the filters", async () => {
    mount("/agent/issues?view=board");
    await screen.findByTestId("work-board");
    await userEvent.click(screen.getByRole("radio", { name: "Mine" }));
    await waitFor(() => {
      const asked = (listWorkItems.mock.calls as [Req][])
        .filter((c) => c[0].mine)
        .map((c) => c[0].status);
      expect(new Set(asked)).toEqual(
        new Set(["triage", "backlog", "todo", "in_progress", "done", "canceled", "none"]),
      );
    });
  });

  it("opens an issue's conversation from its card, and the issue in its tracker from its key", async () => {
    mount("/agent/issues?view=board");
    const todo = await waitFor(() => within(column("todo")).getByTestId("work-card"));
    expect(within(todo).getByRole("link", { name: "ENG-90" })).toHaveAttribute(
      "href",
      "https://linear.app/acme/issue/ENG-12/fix-login",
    );
    expect(todo).toHaveTextContent("ENG");
    expect(todo).toHaveTextContent("working");
    expect(todo).toHaveTextContent("$1.25");
    await userEvent.click(within(todo).getByRole("link", { name: "Open the conversation of ENG-90 todo issue 90" }));
    expect(await screen.findByTestId("where")).toHaveTextContent("/agent/chat/chat_todo_90");
  });

  it("shows a column no tracker state maps to only when something is in it", async () => {
    listWorkItems.mockImplementation(async (req: Req) => ({
      items: req.status === "none" ? [item({ kind: "project", key: "", title: "Roadmap" })] : [],
      nextCursor: "",
      teams: [],
      providers: ["linear"],
      total: req.status === "none" ? 1 : 0,
    }));
    mount("/agent/issues?view=board");
    expect(await screen.findByTestId("work-column-none")).toHaveTextContent("Roadmap");
    expect(screen.getByTestId("work-column-none")).toHaveTextContent("Project");
    expect(screen.queryByTestId("work-column-triage")).not.toBeInTheDocument();
  });
});
