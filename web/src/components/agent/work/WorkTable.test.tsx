import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { IdentityKind } from "../../../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../../../lib/identity";
import { RoleAdmin, RoleMember } from "../../../lib/rbac";
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

/** person is a signed-in person with one role, as the server's RBAC sees them. */
const person = (role: string): Viewer => ({
  login: "ada@acme.com",
  displayName: "Ada",
  kind: IdentityKind.USER,
  agentEnabled: true,
  serverVersion: "dev",
  roles: [role],
  claimed: true,
  hostedDomain: "acme.com",
  canClaim: false,
  googleAuthEnabled: true,
  claimDomain: "",
  pictureUrl: "",
});

function mount(at = "/agent/issues", who: Viewer = person(RoleAdmin)) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ViewerContext value={who}>
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
      </ViewerContext>
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
  involved: true,
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
      expect.objectContaining({ all: false, team: "", state: "" }),
    );
    expect(screen.getByRole("radio", { name: "Mine" })).toHaveAttribute("aria-checked", "true");
    await userEvent.click(screen.getByRole("radio", { name: "All issues" }));
    await waitFor(() =>
      expect(listWorkItems).toHaveBeenLastCalledWith(expect.objectContaining({ all: true })),
    );
    await userEvent.click(screen.getByRole("radio", { name: "Needs attention" }));
    await waitFor(() =>
      expect(listWorkItems).toHaveBeenLastCalledWith(
        expect.objectContaining({ all: true, state: "attention" }),
      ),
    );
  });

  it("gives a member their own issues, with no toggle for everybody's", async () => {
    mount("/agent/issues", person(RoleMember));
    await screen.findAllByTestId("work-row");
    expect(screen.queryByRole("radio", { name: "All issues" })).not.toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: "Mine" })).not.toBeInTheDocument();
    for (const c of listWorkItems.mock.calls as [{ all?: boolean }][]) {
      expect(c[0].all).toBe(false);
    }
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

  it("says how an issue becomes yours when there is none", async () => {
    listWorkItems.mockResolvedValue({ items: [], nextCursor: "", teams: [], providers: ["linear"] });
    mount("/agent/issues", person(RoleMember));
    expect(await screen.findByText("No issues of yours yet.")).toBeInTheDocument();
    expect(screen.getByText(/assigned it, created it, asked Podium about it, or were @mentioned in it/)).toBeInTheDocument();
  });
});

type Req = { state?: string; all?: boolean; page?: { limit?: number; cursor?: string } };

/** cards is n issues of one column, numbered from `from`. */
const cards = (state: string, n: number, from = 1) =>
  Array.from({ length: n }, (_, i) =>
    item({
      chatId: `chat_${state}_${from + i}`,
      key: `ENG-${from + i}`,
      title: `${state} issue ${from + i}`,
      state,
    }),
  );

describe("WorkTable as a board", () => {
  beforeEach(() => {
    localStorage.clear();
    listWorkItems.mockReset();
    // Working holds 23: ten a page. Waiting holds one; every other column none.
    listWorkItems.mockImplementation(async (req: Req) => {
      const page = { items: [] as unknown[], nextCursor: "", teams: [team("ENG")], providers: ["linear"], total: 0 };
      if (req.state === "working") {
        if (!req.page?.cursor) return { ...page, items: cards("working", 10), nextCursor: "c1", total: 23 };
        if (req.page.cursor === "c1") return { ...page, items: cards("working", 10, 11), nextCursor: "c2" };
        return { ...page, items: cards("working", 3, 21) };
      }
      if (req.state === "waiting") {
        return { ...page, items: [item({ ...cards("waiting", 1, 90)[0], askedOf: "grace@acme.com" })], total: 1 };
      }
      if (!req.state) return { ...page, items: [item()] };
      return page;
    });
  });

  const column = (state: string) => screen.getByTestId(`work-column-${state}`);

  it("switches to Podium's states as columns, and says so in the URL and to the browser", async () => {
    mount();
    expect(await screen.findAllByTestId("work-row")).not.toHaveLength(0);
    expect(screen.getByRole("radio", { name: "Needs attention" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("radio", { name: "Board" }));

    expect(await screen.findByTestId("work-board")).toBeInTheDocument();
    expect(screen.queryByTestId("work-row")).not.toBeInTheDocument();
    expect(screen.getByTestId("search")).toHaveTextContent("?view=board");
    expect(localStorage.getItem("podium.work.view")).toBe("board");
    expect(screen.queryByRole("radio", { name: "Needs attention" })).not.toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: "Any state" })).not.toBeInTheDocument();

    const headings = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(headings).toEqual(["Not started", "Working", "Waiting on someone", "Failed", "Done"]);
    await waitFor(() => expect(within(column("working")).getAllByTestId("work-card")).toHaveLength(10));
    expect(within(column("working")).getByTestId("work-column-count")).toHaveTextContent("23");
    expect(within(column("new")).getByText("None")).toBeInTheDocument();

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
    await waitFor(() => expect(within(column("working")).getAllByTestId("work-card")).toHaveLength(10));
    for (const c of listWorkItems.mock.calls as [Req][]) {
      expect(c[0].page?.limit).toBe(10);
    }
    expect(within(column("waiting")).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();

    listWorkItems.mockClear();
    await userEvent.click(within(column("working")).getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(within(column("working")).getAllByTestId("work-card")).toHaveLength(20));
    expect(listWorkItems).toHaveBeenCalledTimes(1);
    expect(listWorkItems).toHaveBeenCalledWith(
      expect.objectContaining({ state: "working", page: { limit: 10, cursor: "c1" } }),
    );
    expect(within(column("waiting")).getAllByTestId("work-card")).toHaveLength(1);

    await userEvent.click(within(column("working")).getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(within(column("working")).getAllByTestId("work-card")).toHaveLength(23));
    expect(within(column("working")).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("asks every column for the filters, each for its own state", async () => {
    mount("/agent/issues?view=board");
    await screen.findByTestId("work-board");
    await userEvent.click(screen.getByRole("radio", { name: "All issues" }));
    await waitFor(() => {
      const asked = (listWorkItems.mock.calls as [Req][]).filter((c) => c[0].all).map((c) => c[0].state);
      expect(new Set(asked)).toEqual(new Set(["new", "working", "waiting", "failed", "done"]));
    });
  });

  it("shows the tracker's state on a card, and whose answer a waiting one needs", async () => {
    mount("/agent/issues?view=board");
    const waiting = await waitFor(() => within(column("waiting")).getByTestId("work-card"));
    expect(within(waiting).getByRole("link", { name: "ENG-90" })).toHaveAttribute(
      "href",
      "https://linear.app/acme/issue/ENG-12/fix-login",
    );
    expect(within(waiting).getByRole("img", { name: "Linear" })).toBeInTheDocument();
    expect(within(waiting).getByTestId("work-card-status")).toHaveTextContent("In Progress");
    expect(waiting).toHaveTextContent("ENG");
    expect(waiting).toHaveTextContent("asked of grace@acme.com");
    expect(waiting).not.toHaveTextContent("waiting on someone");
    expect(waiting).toHaveTextContent("$1.25");
    await userEvent.click(within(waiting).getByRole("link", { name: "Open the conversation of ENG-90 waiting issue 90" }));
    expect(await screen.findByTestId("where")).toHaveTextContent("/agent/chat/chat_waiting_90");
  });

  it("keeps a working card free of Podium's state, which its column says", async () => {
    mount("/agent/issues?view=board");
    await waitFor(() => expect(within(column("working")).getAllByTestId("work-card")).toHaveLength(10));
    const card = within(column("working")).getAllByTestId("work-card")[0];
    expect(within(card).queryByText("working")).not.toBeInTheDocument();
    expect(within(card).queryByText(/asked of/)).not.toBeInTheDocument();
  });
});
