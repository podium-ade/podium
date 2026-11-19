import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { MemoryRouter, Route, Routes } from "react-router";
import { UsagePage } from "./UsagePage";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { AGENT_UNREACHABLE } from "../lib/client";
import { ViewerContext, type Viewer } from "../lib/identity";
import { addDays, dayKey, startOfDay, startOfMonth } from "../lib/usage";

const getUsage = vi.fn();
const listUsageRows = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    agent: {
      getUsage: (...a: unknown[]) => getUsage(...a),
      listUsageRows: (...a: unknown[]) => listUsageRows(...a),
    },
  };
});

const VIEWER: Viewer = {
  login: "user@example.com",
  displayName: "Example User",
  kind: IdentityKind.USER,
  agentEnabled: true,
  serverVersion: "v0",
  roles: [],
  claimed: false,
  hostedDomain: "",
  canClaim: false,
  googleAuthEnabled: false,
  claimDomain: "",
  pictureUrl: "",
};

// Mounted under the same splat route App.tsx uses. The tabs are nested routes, so mounting
// the page bare would resolve them against "/" and no tab would ever match.
function mount(viewer: Viewer = VIEWER, path = "/usage") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ViewerContext value={viewer}>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/usage/*" element={<UsagePage />} />
            <Route path="/tasks/:id" element={<p>task page</p>} />
            <Route path="/agent/chat/:id" element={<p>chat page</p>} />
          </Routes>
        </MemoryRouter>
      </ViewerContext>
    </QueryClientProvider>,
  );
}

/** The from/to of the most recent call to a mock, as local Dates. */
function lastRange(mock: typeof getUsage) {
  const req = mock.mock.calls[mock.mock.calls.length - 1][0] as { from: { seconds: bigint }; to: { seconds: bigint } };
  return {
    from: new Date(Number(req.from.seconds) * 1000),
    to: new Date(Number(req.to.seconds) * 1000),
  };
}

// The screen opens on the last 7 days, so fixtures are pinned relative to today rather than
// to a fixed date that would fall out of range as the calendar rolls forward.
const today = startOfDay(new Date());
const yesterday = addDays(today, -1);

const sum = (o: Partial<Record<string, number | bigint>>) => ({
  costUsd: 0,
  subscriptionCostUsd: 0,
  turns: 0,
  modelTurns: 0,
  unpriced: 0,
  inputTokens: 0n,
  outputTokens: 0n,
  reasoningTokens: 0n,
  cacheReadTokens: 0n,
  cacheWriteTokens: 0n,
  ...o,
});

/** A delegated task, billed to the company's API key, that ran as a control-plane task. */
const delegated = {
  taskId: "tsk_01j8zc4m2qk7x9nrwd3pvb",
  turnId: "dlg_1",
  sessionId: "ses_1",
  sourceKind: "slack",
  sourceKey: "slack:C0123:1725000000.000100",
  origin: "slack",
  kind: "delegation",
  playbook: "triage",
  profile: "default",
  status: "succeeded",
  startedAt: timestampFromDate(yesterday),
  numTurns: 4,
  costUsd: 0.0182,
  provider: "anthropic",
  agent: "claude",
  model: "claude-opus-5",
  effort: "high",
  authKind: "api_key",
  credentialOwner: "",
  credentialAccount: "",
  startedBy: "alice@example.com",
  inputTokens: 12_400n,
  outputTokens: 900n,
  cacheReadTokens: 50_000n,
  chatId: "",
  chatTitle: "Triage the flaky build",
  routineId: "",
  routineName: "",
};

/** An assistant turn on the chat host: no task, a chat, and a subscription paid for it. */
const hostTurn = {
  ...delegated,
  taskId: "",
  turnId: "turn_2",
  kind: "turn",
  origin: "chat",
  sourceKind: "chat",
  playbook: "",
  costUsd: 0.5,
  authKind: "subscription",
  credentialOwner: "alice@example.com",
  credentialAccount: "alice@example.com",
  chatId: "cht_1",
  chatTitle: "Release notes",
  inputTokens: undefined,
  outputTokens: undefined,
};

const backends = [
  { provider: "anthropic", agent: "claude", model: "claude-opus-5", effort: "high",
    costUsd: 12.5, turns: 8, modelTurns: 120, unpriced: 0, totals: sum({ costUsd: 12.5, subscriptionCostUsd: 2.5, turns: 8, modelTurns: 120 }) },
  { provider: "xai", agent: "grok", model: "grok-4.6", effort: "",
    costUsd: 3.25, turns: 4, modelTurns: 40, unpriced: 1, totals: sum({ costUsd: 3.25, turns: 4, modelTurns: 40, unpriced: 1 }) },
  // Turns from before the conductor recorded any of this.
  { provider: "", agent: "", model: "", effort: "",
    costUsd: 0.75, turns: 2, modelTurns: 6, unpriced: 2, totals: sum({ costUsd: 0.75, turns: 2, modelTurns: 6, unpriced: 2 }) },
];

const usageResponse = {
  days: [
    // One day inside the window and one before it, so a total that ignored the range would
    // be visibly wrong rather than coincidentally right.
    { date: dayKey(addDays(today, -10)), costUsd: 2, turns: 4, modelTurns: 9, unpriced: 0,
      totals: sum({ costUsd: 2, turns: 4 }) },
    { date: dayKey(yesterday), costUsd: 0.0182, turns: 1, modelTurns: 4, unpriced: 0,
      totals: sum({ costUsd: 0.0182, turns: 1 }) },
    { date: dayKey(today), costUsd: 0.5, turns: 3, modelTurns: 11, unpriced: 1,
      totals: sum({ costUsd: 0.5, subscriptionCostUsd: 0.5, turns: 3 }) },
  ],
  costs: [],
  totalCostUsd: 0.5182,
  totalTurns: 4,
  totalModelTurns: 15,
  unpriced: 1,
  totals: sum({
    costUsd: 0.5182,
    subscriptionCostUsd: 0.5,
    turns: 4,
    modelTurns: 15,
    unpriced: 1,
    inputTokens: 1_250_000n,
    outputTokens: 34_000n,
    cacheReadTokens: 9_000_000n,
  }),
  backends,
  // Out of order on purpose: the table ranks them itself.
  people: [
    { startedBy: "github-app", costUsd: 1, turns: 2, modelTurns: 5, unpriced: 0, totals: sum({ costUsd: 1, turns: 2 }) },
    { startedBy: "alice@example.com", costUsd: 3, turns: 4, modelTurns: 30, unpriced: 1, totals: sum({ costUsd: 3, turns: 4 }) },
    { startedBy: "routine:rtn_1", costUsd: 0.5, turns: 1, modelTurns: 2, unpriced: 0, totals: sum({ costUsd: 0.5, turns: 1 }) },
    { startedBy: "", costUsd: 0.25, turns: 1, modelTurns: 2, unpriced: 0, totals: sum({ costUsd: 0.25, turns: 1 }) },
  ],
  sources: [
    { key: "slack", label: "", totals: sum({ costUsd: 0.0182, turns: 1 }) },
    { key: "chat", label: "", totals: sum({ costUsd: 0.5, subscriptionCostUsd: 0.5, turns: 3 }) },
    { key: "housekeeping", label: "", totals: sum({ costUsd: 0.01, turns: 2 }) },
  ],
  credentials: [
    { key: "subscription|anthropic|alice@example.com", authKind: "subscription", provider: "anthropic",
      owner: "alice@example.com", account: "alice@example.com", totals: sum({ costUsd: 0.5, subscriptionCostUsd: 0.5, turns: 3 }) },
    { key: "api_key|anthropic|", authKind: "api_key", provider: "anthropic", owner: "", account: "",
      totals: sum({ costUsd: 0.0182, turns: 1 }) },
    { key: "||", authKind: "", provider: "", owner: "", account: "", totals: sum({ costUsd: 0.75, turns: 2 }) },
  ],
  conversations: [
    { key: "ses_2", label: "Release notes", chatId: "cht_1", origin: "chat", totals: sum({ costUsd: 0.5, turns: 3 }) },
  ],
  routines: [{ key: "rtn_1", label: "Nightly triage", totals: sum({ costUsd: 0.5, turns: 1 }) }],
};

describe("UsagePage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getUsage.mockResolvedValue(usageResponse);
    listUsageRows.mockResolvedValue({ rows: [hostTurn, delegated], nextCursor: "" });
  });

  it("opens on the last 7 days", async () => {
    mount();
    await screen.findAllByTestId("usage-row");
    expect(screen.getByRole("button", { name: "7d" })).toHaveAttribute("aria-pressed", "true");
    const { from, to } = lastRange(listUsageRows);
    expect(from).toEqual(addDays(today, -6));
    expect(to).toEqual(addDays(today, 1));
  });

  it("splits billed spend from what a subscription covered", async () => {
    mount();
    await screen.findAllByTestId("usage-row");
    const totals = within(screen.getByRole("group", { name: "Totals" }));
    // $0.5182 in all, $0.50 of it a subscription's list-price figure: $0.0182 was billed.
    expect(totals.getByText("$0.02")).toBeVisible();
    expect(totals.getByText("$0.50")).toBeVisible();
    expect(totals.getByText("1.3M / 34k")).toBeVisible();
    expect(totals.getByText("9M read from cache")).toBeVisible();
    expect(totals.getByText("4")).toBeVisible();
    expect(totals.getByText("1 reported no cost")).toBeVisible();
  });

  it("gives the exact token counts behind the shortened ones on hover", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findAllByTestId("usage-row");
    const totals = within(screen.getByRole("group", { name: "Totals" }));
    await user.hover(totals.getByText("1.3M / 34k"));
    const tip = await screen.findByRole("tooltip");
    expect(tip).toHaveTextContent(
      `${(1_250_000).toLocaleString()} in, ${(34_000).toLocaleString()} out, ${(9_000_000).toLocaleString()} read from cache`,
    );
  });

  it("compares billed spend with the window before, from the day rows", async () => {
    // Ten days ago is in the previous seven-day window. Its $2 billed against $0.0182 billed
    // now is a fall of 99%; the subscription's $0.50 is not in either figure.
    mount();
    expect(await screen.findByText("-99%")).toBeVisible();
  });

  it.each([
    ["1d", 0, 1],
    ["2d", -1, 1],
    ["30d", -29, 1],
    ["90d", -89, 1],
  ])("asks for the %s range", async (preset, fromOffset, toOffset) => {
    const user = userEvent.setup();
    mount();
    await screen.findAllByTestId("usage-row");
    await user.click(screen.getByRole("button", { name: preset }));
    await waitFor(() => {
      const { from, to } = lastRange(listUsageRows);
      expect(from).toEqual(addDays(today, fromOffset));
      expect(to).toEqual(addDays(today, toOffset));
    });
  });

  it("asks for the calendar month on This month, and the one before on Last month", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findAllByTestId("usage-row");
    const first = startOfMonth(today);

    await user.click(screen.getByRole("button", { name: "This month" }));
    await waitFor(() => expect(lastRange(getUsage).from).toEqual(first));

    await user.click(screen.getByRole("button", { name: "Last month" }));
    await waitFor(() => {
      const { from, to } = lastRange(getUsage);
      expect(to).toEqual(first);
      expect(from).toEqual(new Date(first.getFullYear(), first.getMonth() - 1, 1));
    });
  });

  it("takes a custom range and includes both of the days typed", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findAllByTestId("usage-row");

    await user.click(screen.getByRole("button", { name: "Custom" }));
    const from = addDays(today, -3);
    await user.clear(screen.getByLabelText("From"));
    await user.type(screen.getByLabelText("From"), dayKey(from));
    await user.clear(screen.getByLabelText("To"));
    await user.type(screen.getByLabelText("To"), dayKey(yesterday));
    await user.click(screen.getByRole("button", { name: /apply/i }));

    await waitFor(() => {
      const r = lastRange(listUsageRows);
      expect(r.from).toEqual(from);
      // The "to" day is inclusive, so the exclusive bound is the day after it.
      expect(r.to).toEqual(today);
    });
  });

  it("asks for the range itself, and reaches back for the comparison separately", async () => {
    mount();
    await waitFor(() => expect(getUsage).toHaveBeenCalled());
    const req = getUsage.mock.calls[0][0];
    expect(req.tzOffsetMinutes).toBe(-new Date().getTimezoneOffset());
    expect(new Date(Number(req.from.seconds) * 1000)).toEqual(addDays(today, -6));
    expect(new Date(Number(req.to.seconds) * 1000)).toEqual(addDays(today, 1));
    // Only the day rows reach back over the seven days before it, for the "vs" figure.
    expect(new Date(Number(req.compareFrom.seconds) * 1000)).toEqual(addDays(today, -13));
  });

  it("lists every unit of spend with its model, what paid, whose it was and its tokens", async () => {
    mount();
    const rows = await screen.findAllByTestId("usage-row");
    expect(rows).toHaveLength(2);

    const host = within(rows[0]);
    // A chat-host turn has no task, but it is still a row: it is where the conversation's money went.
    expect(host.getByText("assistant")).toBeVisible();
    expect(host.getByText("Release notes")).toBeVisible();
    expect(host.getByText("Podium chat")).toBeVisible();
    expect(host.getByText("Subscription")).toBeVisible();
    expect(host.getByText("covered")).toBeVisible();
    expect(host.getByText("$0.5000")).toBeVisible();

    const task = within(rows[1]);
    expect(task.getByText("Task")).toBeVisible();
    expect(task.getByText("triage")).toBeVisible();
    expect(task.getByText("claude-opus-5")).toBeVisible();
    expect(task.getByText("high effort")).toBeVisible();
    expect(task.getByText("API key")).toBeVisible();
    expect(task.getByText("Company")).toBeVisible();
    expect(task.getByText("alice@example.com")).toBeVisible();
    expect(task.getByText(/12\.4k/)).toBeVisible();
    expect(task.getByText("$0.0182")).toBeVisible();
  });

  it("opens a row's task, or its conversation when it has no task, from the keyboard too", async () => {
    const user = userEvent.setup();
    const { container } = mount();
    const rows = await screen.findAllByTestId("usage-row");
    rows[1].focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(container.textContent).toContain("task page"));
  });

  it("pages through the rows the server holds", async () => {
    const user = userEvent.setup();
    listUsageRows.mockResolvedValueOnce({ rows: [hostTurn], nextCursor: "c1" });
    listUsageRows.mockResolvedValueOnce({ rows: [delegated], nextCursor: "" });
    mount();
    await screen.findAllByTestId("usage-row");
    await user.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findAllByTestId("usage-row")).toHaveLength(2);
    expect(listUsageRows.mock.calls[1][0].cursor).toBe("c1");
    expect(screen.queryByRole("button", { name: "Show more" })).toBeNull();
  });

  it("narrows the whole screen with a filter kept in the URL", async () => {
    mount(VIEWER, "/usage?source=slack&paid=api_key%7Canthropic%7C");
    await screen.findAllByTestId("usage-row");
    const usageReq = getUsage.mock.calls[0][0];
    expect(usageReq.filter).toMatchObject({ origin: "slack", credential: "api_key|anthropic|" });
    expect(listUsageRows.mock.calls[0][0].filter).toMatchObject({ origin: "slack" });
    expect(screen.getByRole("button", { name: /clear filters/i })).toBeVisible();
  });

  it("draws a bar per day in the range, empty days included", async () => {
    mount();
    await screen.findAllByTestId("usage-row");
    const trend = screen.getByRole("img", { name: /spend per day/i });
    expect(trend.children).toHaveLength(7);
    expect(trend).toHaveAccessibleName(/\$0\.0182 billed and \$0\.5000 covered by subscription/);
  });

  it("breaks the range down by source, model, person and credential", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findAllByTestId("usage-row");
    const panel = within(screen.getByRole("region", { name: "Where it went" }));

    expect(panel.getByText("Housekeeping")).toBeVisible();
    await user.click(panel.getByRole("radio", { name: "Model" }));
    await waitFor(() => expect(panel.getByText("grok-4.6")).toBeVisible());
    await user.click(panel.getByRole("radio", { name: "Person" }));
    await waitFor(() => expect(panel.getByText("Nightly triage")).toBeVisible());
    await user.click(panel.getByRole("radio", { name: "Paid with" }));
    await waitFor(() => expect(panel.getAllByText("Subscription")[0]).toBeVisible());
  });

  it("keeps the by-model tab on its own route, and shows one row per backend", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findAllByTestId("usage-row");

    await user.click(screen.getByRole("tab", { name: /by model/i }));

    const rows = await screen.findAllByTestId("backend-row");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByText("claude-opus-5")).toBeVisible();
    expect(within(rows[0]).getByText("Anthropic")).toBeVisible();
    // $12.50 in all, $2.50 of it covered: $10 billed.
    expect(within(rows[0]).getByText("$10.0000")).toBeVisible();
    expect(within(rows[0]).getByText("$2.5000")).toBeVisible();
    // Cost per turn, which is the number that makes two models comparable.
    expect(within(rows[0]).getByText("$1.5625")).toBeVisible();
    // A recorded turn with no effort means the model's default, and says so.
    expect(within(rows[1]).getByText("default effort")).toBeVisible();
    // Unrecorded is its own bucket, not a model with an empty name.
    expect(within(rows[2]).getByText("Unrecorded")).toBeVisible();
    expect(screen.queryByTestId("usage-row")).toBeNull();
  });

  it("ranks spend by the person who started each turn, naming a system routine", async () => {
    mount(VIEWER, "/usage/people");
    const rows = await screen.findAllByTestId("person-row");
    expect(rows).toHaveLength(4);
    expect(within(rows[0]).getByText("alice@example.com")).toBeVisible();
    expect(within(rows[0]).getByText("63%")).toBeVisible();
    expect(within(rows[0]).getByRole("img")).toHaveAccessibleName(/alice@example\.com: \$3\.0000/);
    expect(within(rows[1]).getByText("GitHub App")).toBeVisible();
    expect(within(rows[2]).getByText("Nightly triage")).toBeVisible();
    expect(within(rows[3]).getByText("Unattributed")).toBeVisible();
  });

  it("says so when nobody spent anything in the range", async () => {
    getUsage.mockResolvedValue({ ...usageResponse, people: [] });
    mount(VIEWER, "/usage/people");
    expect(await screen.findByText(/nobody to attribute spend to/i)).toBeVisible();
  });

  it("shows sources, routines and the most expensive conversations", async () => {
    mount(VIEWER, "/usage/sources");
    const sources = await screen.findAllByTestId("source-row");
    expect(sources).toHaveLength(3);
    expect(within(sources[0]).getByText("Podium chat")).toBeVisible();
    expect(within(screen.getByTestId("routine-row")).getByText("Nightly triage")).toBeVisible();
    expect(within(screen.getByTestId("conversation-row")).getByText("Release notes")).toBeVisible();
  });

  it("shows what paid, whose it was, and the unrecorded bucket", async () => {
    mount(VIEWER, "/usage/credentials");
    const rows = await screen.findAllByTestId("credential-row");
    expect(rows).toHaveLength(3);
    // Most expensive first: the unrecorded bucket's real but unattributed $0.75 leads.
    expect(within(rows[0]).getAllByText("Unrecorded")[0]).toBeVisible();
    expect(within(rows[1]).getByText("Subscription")).toBeVisible();
    expect(within(rows[2]).getByText("Company")).toBeVisible();
  });

  it("drills from a grouping into the table, filtered", async () => {
    const user = userEvent.setup();
    mount(VIEWER, "/usage/sources");
    const rows = await screen.findAllByTestId("source-row");
    await user.click(rows[1]);
    await waitFor(() => {
      const req = listUsageRows.mock.calls[listUsageRows.mock.calls.length - 1][0];
      expect(req.filter).toMatchObject({ origin: "slack" });
    });
    expect(await screen.findAllByTestId("usage-row")).toHaveLength(2);
  });

  it("summarises only what is attributed, never the unrecorded bucket", async () => {
    mount(VIEWER, "/usage/models");
    // $12.50 + $3.25; the $0.75 of unrecorded spend is real but cannot be attributed.
    expect(await screen.findByText("2 models across 2 providers, $15.75 attributed.")).toBeVisible();
  });

  it("keeps the range picker working across tabs", async () => {
    const user = userEvent.setup();
    mount(VIEWER, "/usage/models");
    await screen.findAllByTestId("backend-row");
    await user.click(screen.getByRole("button", { name: "30d" }));
    await waitFor(() => expect(lastRange(getUsage).from).toEqual(addDays(today, -29)));
    expect(await screen.findAllByTestId("backend-row")).toHaveLength(3);
  });

  it("tells the operator when the conductor is down instead of showing an empty bill", async () => {
    getUsage.mockRejectedValue(new ConnectError(AGENT_UNREACHABLE, Code.Unavailable));
    listUsageRows.mockRejectedValue(new ConnectError(AGENT_UNREACHABLE, Code.Unavailable));
    mount();
    expect(await screen.findByText("The spend for this range could not be read")).toBeVisible();
  });

  it("sends the operator to docs when there is no conductor at all", async () => {
    mount({ ...VIEWER, agentEnabled: false });
    expect(await screen.findByText("There is no conductor on this control plane")).toBeVisible();
    expect(getUsage).not.toHaveBeenCalled();
    expect(listUsageRows).not.toHaveBeenCalled();
  });
});
