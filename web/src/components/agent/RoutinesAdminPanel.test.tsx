import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  MemberRoutineSchema,
  RoutineLimitsSchema,
} from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { RoutinesAdminPanel } from "./RoutinesAdminPanel";

const listMemberRoutines = vi.fn();
const getRoutineLimits = vi.fn();
const setRoutineLimits = vi.fn();
const pauseRoutine = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual =
    await vi.importActual<typeof import("../../lib/client")>(
      "../../lib/client",
    );
  return {
    ...actual,
    agent: {
      listMemberRoutines: (...a: unknown[]) => listMemberRoutines(...a),
      getRoutineLimits: (...a: unknown[]) => getRoutineLimits(...a),
      setRoutineLimits: (...a: unknown[]) => setRoutineLimits(...a),
      pauseRoutine: (...a: unknown[]) => pauseRoutine(...a),
    },
  };
});

function member(over: MessageInitShape<typeof MemberRoutineSchema> = {}) {
  return create(MemberRoutineSchema, {
    id: "rtn_1",
    name: "PR digest",
    ownerLogin: "bob@acme.com",
    scheduleText: "weekdays at 09:00",
    timezone: "America/Toronto",
    enabled: true,
    spendTodayUsd: 0.42,
    lastRunStatus: "done",
    lastRunAt: timestampFromDate(new Date(Date.now() - 2 * 3_600_000)),
    ...over,
  });
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <RoutinesAdminPanel />
      </ToastHost>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  for (const fn of [
    listMemberRoutines,
    getRoutineLimits,
    setRoutineLimits,
    pauseRoutine,
  ])
    fn.mockReset();
  getRoutineLimits.mockResolvedValue({
    limits: create(RoutineLimitsSchema, { maxPerPerson: 10 }),
  });
  listMemberRoutines.mockResolvedValue({ routines: [] });
});

describe("RoutinesAdminPanel", () => {
  it("loads the limits and saves new ones", async () => {
    setRoutineLimits.mockImplementation(async ({ limits }) => ({ limits }));
    mount();

    const count = await screen.findByLabelText("Routines per person");
    await waitFor(() => expect(count).toHaveValue(10));
    const cap = screen.getByLabelText("Daily spend per person");
    expect(cap).toHaveValue(0);
    const save = screen.getByRole("button", { name: "Save limits" });
    expect(save).toBeDisabled();

    await userEvent.clear(count);
    await userEvent.type(count, "3");
    await userEvent.clear(cap);
    await userEvent.type(cap, "7.5");
    await userEvent.click(save);

    await waitFor(() =>
      expect(setRoutineLimits).toHaveBeenCalledWith({
        limits: { maxPerPerson: 3, personDailyCapUsd: 7.5 },
      }),
    );
    expect(
      await screen.findByText("Routine limits saved."),
    ).toBeInTheDocument();
  });

  it("refuses a count out of range, and shows what the server says", async () => {
    setRoutineLimits.mockRejectedValue(
      new Error("a person may have between 1 and 100 routines"),
    );
    mount();
    const count = await screen.findByLabelText("Routines per person");
    await waitFor(() => expect(count).toHaveValue(10));

    await userEvent.clear(count);
    await userEvent.type(count, "0");
    expect(screen.getByRole("button", { name: "Save limits" })).toBeDisabled();

    await userEvent.clear(count);
    await userEvent.type(count, "12");
    await userEvent.click(screen.getByRole("button", { name: "Save limits" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "between 1 and 100 routines",
    );
  });

  it("lists people's routines as metadata", async () => {
    listMemberRoutines.mockResolvedValue({
      routines: [
        member(),
        member({
          id: "rtn_2",
          name: "Stale branches",
          ownerLogin: "carol@acme.com",
          enabled: false,
          pausedReason: "5 runs failed within an hour.",
          lastRunStatus: "failed",
        }),
        member({
          id: "rtn_3",
          name: "New one",
          lastRunStatus: "",
          lastRunAt: undefined,
        }),
      ],
    });
    mount();

    const rows = await screen.findAllByTestId("member-routine-row");
    expect(rows).toHaveLength(3);
    expect(rows[0]).toHaveTextContent("PR digest");
    expect(rows[0]).toHaveTextContent("bob@acme.com");
    expect(rows[0]).toHaveTextContent("weekdays at 09:00");
    expect(rows[0]).toHaveTextContent("America/Toronto");
    expect(rows[0]).toHaveTextContent("$0.42");
    expect(rows[0]).toHaveTextContent("Did something");
    expect(rows[0]).toHaveTextContent("Active");
    expect(rows[1]).toHaveTextContent("Paused");
    expect(rows[1]).toHaveTextContent("5 runs failed within an hour.");
    expect(rows[1]).toHaveTextContent("Failed");
    expect(
      within(rows[1]!).queryByRole("button", { name: /Pause/ }),
    ).toBeNull();
    expect(rows[2]).toHaveTextContent("Not yet");
  });

  it("says so when nobody has a routine", async () => {
    mount();
    expect(
      await screen.findByTestId("member-routines-empty"),
    ).toHaveTextContent("Nobody has a routine yet.");
  });

  it("pauses a person's routine after asking, and says its owner was told", async () => {
    listMemberRoutines.mockResolvedValue({ routines: [member()] });
    pauseRoutine.mockResolvedValue({});
    mount();

    await userEvent.click(
      await screen.findByRole("button", { name: "Pause PR digest" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Pause PR digest?");
    expect(dialog).toHaveTextContent(
      "bob@acme.com is told in their inbox and can resume it.",
    );
    expect(pauseRoutine).not.toHaveBeenCalled();

    await userEvent.click(screen.getByTestId("member-routine-pause-confirm"));
    await waitFor(() =>
      expect(pauseRoutine).toHaveBeenCalledWith({ id: "rtn_1" }),
    );
    expect(
      await screen.findByText("PR digest paused. bob@acme.com was told."),
    ).toBeInTheDocument();
  });
});
