import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { MemoryRouter } from "react-router";
import { UsagePage } from "./UsagePage";
import { TaskStatus } from "../gen/podium/v1/common_pb";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { AGENT_UNREACHABLE } from "../lib/client";
import { ViewerContext, type Viewer } from "../lib/identity";
import { addDays, dayKey, startOfDay, startOfMonth } from "../lib/usage";

const getUsage = vi.fn();
const listTasks = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    agent: { getUsage: (...a: unknown[]) => getUsage(...a) },
    tasks: { listTasks: (...a: unknown[]) => listTasks(...a) },
  };
});

const VIEWER: Viewer = {
  login: "alvaro@example.com",
  displayName: "Alvaro",
  kind: IdentityKind.USER,
  agentEnabled: true,
  serverVersion: "v0",
};

function mount(viewer: Viewer = VIEWER) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ViewerContext value={viewer}>
        <MemoryRouter>
          <UsagePage />
        </MemoryRouter>
      </ViewerContext>
    </QueryClientProvider>,
  );
}

/** The from/to of the most recent call to a mock, as local Dates. */
function lastRange(mock: typeof listTasks, pick: (a: Record<string, never>) => unknown) {
  const arg = mock.mock.calls[mock.mock.calls.length - 1][0];
  const f = pick(arg) as { createdAfter?: { seconds: bigint }; createdBefore?: { seconds: bigint } };
  return {
    from: new Date(Number(f.createdAfter!.seconds) * 1000),
    to: new Date(Number(f.createdBefore!.seconds) * 1000),
  };
}

// The screen opens on the last 7 days, so fixtures are pinned relative to today rather than
// to a fixed date that would fall out of range as the calendar rolls forward.
const today = startOfDay(new Date());
const yesterday = addDays(today, -1);

const agentTask = {
  id: "tsk_01j8zc4m2qk7x9nrwd3pvb",
  spec: { image: "ghcr.io/affiniti/agent:1" },
  status: TaskStatus.SUCCEEDED,
  nodeId: "nod_1",
  attempts: 1,
  createdAt: timestampFromDate(yesterday),
  startedAt: timestampFromDate(yesterday),
  finishedAt: timestampFromDate(new Date(yesterday.getTime() + 134_000)),
  requestedBy: "podium-agent",
  failureReason: "",
  queuedReason: "",
  usage: { cpuSeconds: 12.5, peakMemoryMb: 400n, wallMs: 134_000n },
};

const plainTask = {
  id: "tsk_01j8zbwk4r2d8yfqna5jzx",
  spec: { image: "alpine:3" },
  status: TaskStatus.SUCCEEDED,
  nodeId: "nod_1",
  attempts: 1,
  createdAt: timestampFromDate(today),
  startedAt: timestampFromDate(today),
  finishedAt: timestampFromDate(new Date(today.getTime() + 12_000)),
  requestedBy: "alvaro@example.com",
  failureReason: "",
  queuedReason: "",
  usage: { cpuSeconds: 0.4, peakMemoryMb: 12n, wallMs: 12_000n },
};

const turnCost = {
  taskId: agentTask.id,
  turnId: "trn_1",
  sessionId: "ses_1",
  sourceKind: "slack",
  sourceKey: "slack:C0123:1725000000.000100",
  playbook: "triage",
  profile: "default",
  status: "succeeded",
  startedAt: timestampFromDate(yesterday),
  finishedAt: timestampFromDate(new Date(yesterday.getTime() + 134_000)),
  numTurns: 4,
  costUsd: 0.0182,
};

const usageResponse = {
  days: [
    // One day inside the window and one before it, so a total that ignored the range would
    // be visibly wrong rather than coincidentally right.
    { date: dayKey(addDays(today, -20)), costUsd: 99, turns: 40, modelTurns: 90, unpriced: 0 },
    { date: dayKey(yesterday), costUsd: 0.0182, turns: 1, modelTurns: 4, unpriced: 0 },
    { date: dayKey(today), costUsd: 0.5, turns: 3, modelTurns: 11, unpriced: 1 },
  ],
  costs: [turnCost],
  totalCostUsd: 99.5182,
  totalTurns: 44,
  totalModelTurns: 105,
  unpriced: 1,
};

describe("UsagePage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getUsage.mockResolvedValue(usageResponse);
    listTasks.mockResolvedValue({ tasks: [agentTask, plainTask], nextCursor: "" });
  });

  it("opens on the last 7 days", async () => {
    mount();
    await screen.findAllByTestId("usage-row");
    expect(screen.getByRole("button", { name: "7d" })).toHaveAttribute("aria-pressed", "true");

    const { from, to } = lastRange(listTasks, (a) => (a as never as { filter: unknown }).filter);
    expect(from).toEqual(addDays(today, -6));
    expect(to).toEqual(addDays(today, 1));
  });

  it("totals only the days inside the range, not everything the server returned", async () => {
    mount();
    await screen.findAllByTestId("usage-row");
    const totals = within(screen.getByRole("group", { name: "Totals" }));
    // 0.0182 + 0.50. The 99 twenty days ago is outside the last 7 days.
    expect(totals.getByText("$0.52")).toBeVisible();
    expect(totals.getByText("4")).toBeVisible(); // agent tasks
    expect(totals.getByText("15")).toBeVisible(); // model turns
  });

  it("says how many turns reported no cost, so a total is not read as complete", async () => {
    mount();
    expect(await screen.findByText("1 turn reported no cost")).toBeVisible();
  });

  it.each([
    ["1d", 0, 1],
    ["2d", -1, 1],
    ["30d", -29, 1],
    ["90d", -89, 1],
  ])("asks both services for the %s range", async (preset, fromOffset, toOffset) => {
    const user = userEvent.setup();
    mount();
    await screen.findAllByTestId("usage-row");

    await user.click(screen.getByRole("button", { name: preset }));

    await waitFor(() => {
      const { from, to } = lastRange(listTasks, (a) => (a as never as { filter: unknown }).filter);
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
    await waitFor(() => {
      const { from } = lastRange(listTasks, (a) => (a as never as { filter: unknown }).filter);
      expect(from).toEqual(first);
    });

    await user.click(screen.getByRole("button", { name: "Last month" }));
    await waitFor(() => {
      const { from, to } = lastRange(listTasks, (a) => (a as never as { filter: unknown }).filter);
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
      const r = lastRange(listTasks, (a) => (a as never as { filter: unknown }).filter);
      expect(r.from).toEqual(from);
      // The "to" day is inclusive, so the exclusive bound is the day after it.
      expect(r.to).toEqual(today);
    });
  });

  it("asks the conductor for the comparison window as well, in the viewer's zone", async () => {
    mount();
    await waitFor(() => expect(getUsage).toHaveBeenCalled());
    const req = getUsage.mock.calls[0][0];
    expect(req.tzOffsetMinutes).toBe(-new Date().getTimezoneOffset());
    // Fourteen days back: the seven shown plus the seven it is compared against.
    expect(new Date(Number(req.from.seconds) * 1000)).toEqual(addDays(today, -13));
    expect(new Date(Number(req.to.seconds) * 1000)).toEqual(addDays(today, 1));
  });

  it("joins cost onto the task that ran it and dashes the one no turn ran", async () => {
    mount();
    const rows = await screen.findAllByTestId("usage-row");
    expect(rows).toHaveLength(2);

    const agentRow = within(rows[0]);
    expect(agentRow.getByText("$0.0182")).toBeVisible();
    expect(agentRow.getByText("slack")).toBeVisible();
    expect(agentRow.getByText("triage")).toBeVisible();

    // A plain task has no agent turn behind it, so it has no cost — not a cost of zero.
    expect(within(rows[1]).queryByText(/^\$/)).toBeNull();
  });

  it("shows the compute a task used whether or not it cost anything", async () => {
    mount();
    const rows = await screen.findAllByTestId("usage-row");
    expect(within(rows[0]).getByText("12.5s")).toBeVisible();
    expect(within(rows[1]).getByText("0.4s")).toBeVisible();
  });

  it("draws a bar per day in the range, empty days included", async () => {
    mount();
    await screen.findAllByTestId("usage-row");
    const trend = screen.getByRole("img", { name: /spend per day/i });
    expect(trend).toBeVisible();
    expect(trend.children).toHaveLength(7);
  });

  it("splits the window's spend by playbook and by source", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findAllByTestId("usage-row");
    const panel = within(screen.getByRole("region", { name: "Where it went" }));

    expect(panel.getByText("triage")).toBeVisible();
    await user.click(panel.getByRole("button", { name: "By source" }));
    await waitFor(() => expect(panel.getByText("slack")).toBeVisible());
    expect(panel.queryByText("triage")).toBeNull();
  });

  it("tells the operator when the conductor is down instead of showing an empty bill", async () => {
    getUsage.mockRejectedValue(new ConnectError(AGENT_UNREACHABLE, Code.Unavailable));
    mount();
    expect(await screen.findByText("The spend for this range could not be read")).toBeVisible();
    // The tasks still list: the control plane is fine, only the cost half is missing.
    expect(await screen.findAllByTestId("usage-row")).toHaveLength(2);
  });

  it("sends the operator to docs when there is no conductor at all", async () => {
    mount({ ...VIEWER, agentEnabled: false });
    expect(await screen.findByText("There is no conductor on this control plane")).toBeVisible();
    expect(getUsage).not.toHaveBeenCalled();
  });
});
