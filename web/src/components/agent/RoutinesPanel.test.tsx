import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { RoutineSchema } from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { duplicateOf } from "../../lib/routines";
import { RoutinesPanel } from "./RoutinesPanel";

const listRoutines = vi.fn();
const createRoutine = vi.fn();
const pauseRoutine = vi.fn();
const deleteRoutine = vi.fn();
const listPersonalities = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listRoutines: (...a: unknown[]) => listRoutines(...a),
      createRoutine: (...a: unknown[]) => createRoutine(...a),
      pauseRoutine: (...a: unknown[]) => pauseRoutine(...a),
      deleteRoutine: (...a: unknown[]) => deleteRoutine(...a),
      listPersonalities: (...a: unknown[]) => listPersonalities(...a),
    },
  };
});

function routine(over: MessageInitShape<typeof RoutineSchema> = {}) {
  return create(RoutineSchema, {
    id: "rtn_1",
    name: "PR digest",
    schedule: "0 9 * * 1-5",
    scheduleText: "weekdays at 09:00",
    timezone: "UTC",
    instruction: "Report pull requests whose CI failed.",
    enabled: true,
    chatId: "chat_r",
    nextRunAt: timestampFromDate(new Date(Date.now() + 3_600_000)),
    spendTodayUsd: 0.12,
    ...over,
  });
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter initialEntries={["/agent/routines"]}>
          <Routes>
            <Route path="/agent/routines" element={<RoutinesPanel />} />
            <Route path="/agent/chat/*" element={<p>chat open</p>} />
          </Routes>
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  for (const fn of [listRoutines, createRoutine, pauseRoutine, deleteRoutine, listPersonalities]) fn.mockReset();
  listPersonalities.mockResolvedValue({ personalities: [] });
});

describe("RoutinesPanel", () => {
  it("shows what waits for a routine on Linear events where a scheduled one shows its next run", async () => {
    listRoutines.mockResolvedValue({
      routines: [
        routine({
          schedule: "",
          scheduleText: "On Linear: issue created, comment created",
          trigger: "linear",
          events: ["Issue.create", "Comment.create"],
          nextRunAt: undefined,
          pendingEvents: 3,
          timezone: "Europe/Madrid",
        }),
      ],
    });
    mount();

    const row = await screen.findByTestId("routine-row");
    expect(row).toHaveTextContent("Runs on Linear: issue created, comment created");
    expect(row).not.toHaveTextContent("Europe/Madrid");
    const status = within(row).getByTestId("routine-row-status");
    expect(status).toHaveTextContent("Waiting3 events");
    expect(status).not.toHaveTextContent("Next run");
    // It runs when its events arrive: there is no run by hand.
    expect(within(row).queryByRole("button", { name: /run now/i })).not.toBeInTheDocument();
    expect(within(row).getByRole("button", { name: /pause/i })).toBeInTheDocument();
  });

  it("shows what waits for a routine on GitHub events, and opens its page until its first run", async () => {
    listRoutines.mockResolvedValue({
      routines: [
        routine({
          schedule: "",
          scheduleText: "On GitHub: workflow run failed (acme/api)",
          trigger: "github",
          events: ["workflow_run.failed"],
          teams: ["acme/api"],
          nextRunAt: undefined,
          pendingEvents: 1,
          timezone: "Europe/Madrid",
          chatId: "",
        }),
      ],
    });
    mount();

    const row = await screen.findByTestId("routine-row");
    expect(row).toHaveTextContent("Runs on GitHub: workflow run failed (acme/api)");
    expect(row).not.toHaveTextContent("Europe/Madrid");
    expect(row).not.toHaveTextContent("in each ticket’s conversation");
    const status = within(row).getByTestId("routine-row-status");
    expect(status).toHaveTextContent("Waiting1 event");
    expect(status).not.toHaveTextContent("Next run");
    expect(within(row).queryByRole("button", { name: /run now/i })).not.toBeInTheDocument();
    expect(within(row).getByRole("link", { name: "PR digest" })).toHaveAttribute("href", "/agent/routines/rtn_1");
  });

  it("says when a routine on Linear events runs in each ticket's conversation, and opens its page when it has no chat", async () => {
    listRoutines.mockResolvedValue({
      routines: [
        routine({
          schedule: "",
          scheduleText: "On Linear: issue created",
          trigger: "linear",
          events: ["Issue.create"],
          chatMode: "subject",
          nextRunAt: undefined,
          chatId: "",
        }),
        routine({ id: "rtn_2", name: "Digest", chatId: "chat_d" }),
      ],
    });
    mount();

    const rows = await screen.findAllByTestId("routine-row");
    const triage = rows.find((r) => r.textContent?.includes("PR digest"));
    const digest = rows.find((r) => r.textContent?.includes("Digest") && r !== triage);
    expect(triage).toHaveTextContent("Runs on Linear: issue created · in each ticket’s conversation");
    expect(digest).not.toHaveTextContent("in each ticket’s conversation");
    expect(within(triage as HTMLElement).getByRole("link", { name: "PR digest" })).toHaveAttribute(
      "href",
      "/agent/routines/rtn_1",
    );
    expect(within(digest as HTMLElement).getByRole("link", { name: "Digest" })).toHaveAttribute(
      "href",
      "/agent/chat/chat_d",
    );
  });

  it("lists each routine with when it runs, and pauses one", async () => {
    listRoutines.mockResolvedValue({ routines: [routine()] });
    pauseRoutine.mockResolvedValue({ routine: routine({ enabled: false }) });
    mount();

    const row = await screen.findByTestId("routine-row");
    expect(row).toHaveTextContent("PR digest");
    expect(row).toHaveTextContent("Runs weekdays at 09:00");
    const status = within(row).getByTestId("routine-row-status");
    expect(status).toHaveTextContent(/Next run(today|tomorrow) \d\d:\d\d/);
    expect(status).toHaveTextContent("Last runnot yet");

    listRoutines.mockResolvedValue({ routines: [routine({ enabled: false })] });
    await userEvent.click(within(row).getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(pauseRoutine).toHaveBeenCalledWith({ id: "rtn_1" }));
    expect(await within(row).findByRole("button", { name: "Resume" })).toBeInTheDocument();
  });

  it("opens a routine's chat from its name, and from its menu", async () => {
    listRoutines.mockResolvedValue({ routines: [routine()] });
    const first = mount();
    await userEvent.click(await screen.findByRole("link", { name: "PR digest" }));
    expect(await screen.findByText("chat open")).toBeInTheDocument();
    first.unmount();

    mount();
    await userEvent.click(await screen.findByRole("button", { name: "More for PR digest" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Open chat" }));
    expect(await screen.findByText("chat open")).toBeInTheDocument();
  });

  it("says why Podium paused a routine", async () => {
    listRoutines.mockResolvedValue({
      routines: [routine({ enabled: false, pausedReason: "5 runs failed within an hour." })],
    });
    mount();
    const row = await screen.findByTestId("routine-row");
    expect(row).toHaveTextContent("Paused by Podium. 5 runs failed within an hour.");
    expect(within(row).getByRole("button", { name: "Resume" })).toBeInTheDocument();
  });

  it("says a routine can also be asked for in a chat when there are none, and makes one right away", async () => {
    listRoutines.mockResolvedValue({ routines: [] });
    createRoutine.mockResolvedValue({ routine: routine() });
    mount();

    expect(await screen.findByText("No routines yet")).toBeInTheDocument();
    expect(screen.getByText(/ask an assistant in a chat/)).toBeInTheDocument();

    await userEvent.click(screen.getByTestId("routines-new"));
    await userEvent.type(screen.getByTestId("routine-name"), "PR digest");
    await userEvent.type(screen.getByTestId("routine-instruction"), "Report failing PRs.");
    listRoutines.mockResolvedValue({ routines: [routine()] });
    await userEvent.click(screen.getByTestId("routine-save"));

    await waitFor(() =>
      expect(createRoutine).toHaveBeenCalledWith({
        draft: expect.objectContaining({ name: "PR digest", instruction: "Report failing PRs.", schedule: "0 9 * * 1-5" }),
        scope: "personal",
      }),
    );
    expect(await screen.findByTestId("routine-row")).toHaveTextContent("PR digest");
  });

  it("duplicates a routine into a new one, prefilled and named as a copy", async () => {
    listRoutines.mockResolvedValue({ routines: [routine()] });
    createRoutine.mockResolvedValue({ routine: routine({ id: "rtn_2", name: "PR digest (copy)" }) });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "More for PR digest" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Duplicate" }));

    expect(await screen.findByText("Duplicate routine")).toBeInTheDocument();
    expect(screen.getByTestId("routine-name")).toHaveValue("PR digest (copy)");
    expect(screen.getByTestId("routine-instruction")).toHaveValue("Report pull requests whose CI failed.");
    await userEvent.click(screen.getByTestId("routine-save"));

    await waitFor(() =>
      expect(createRoutine).toHaveBeenCalledWith({
        draft: expect.objectContaining({
          name: "PR digest (copy)",
          instruction: "Report pull requests whose CI failed.",
          schedule: "0 9 * * 1-5",
        }),
        scope: "personal",
      }),
    );
  });

  it("deletes a routine after asking", async () => {
    listRoutines.mockResolvedValue({ routines: [routine()] });
    deleteRoutine.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "More for PR digest" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
    listRoutines.mockResolvedValue({ routines: [] });
    await userEvent.click(screen.getByTestId("routine-delete-confirm"));
    await waitFor(() => expect(deleteRoutine).toHaveBeenCalledWith({ id: "rtn_1" }));
    expect(await screen.findByText("No routines yet")).toBeInTheDocument();
  });
});

describe("RoutinesPanel for an admin", () => {
  it("marks a system routine as such, and as running as Podium", async () => {
    listRoutines.mockResolvedValue({
      routines: [
        routine({ id: "rtn_sys", name: "Fix failing CI", scope: "system" }),
        routine({ id: "rtn_mine", name: "PR digest", scope: "personal", ownerLogin: "ana" }),
      ],
    });
    mount();
    const rows = await screen.findAllByTestId("routine-row");
    expect(within(rows[0]!).getByTestId("routine-system-badge")).toBeInTheDocument();
    expect(rows[0]).toHaveTextContent("as Podium");
    expect(within(rows[1]!).queryByTestId("routine-system-badge")).not.toBeInTheDocument();
  });

  it("says an admin's pause as it is, not as Podium's", async () => {
    listRoutines.mockResolvedValue({
      routines: [routine({ enabled: false, pausedReason: "Paused by an admin." })],
    });
    mount();
    const row = await screen.findByTestId("routine-row");
    expect(row).toHaveTextContent("Paused by an admin.");
    expect(row).not.toHaveTextContent("Paused by Podium");
  });
});

describe("duplicateOf", () => {
  it("runs the copy as the original does for an admin, and as the person otherwise", () => {
    const sys = routine({ name: "Fix failing CI", scope: "system" });
    expect(duplicateOf(sys, true)).toMatchObject({ scope: "system", initial: { name: "Fix failing CI (copy)" } });
    expect(duplicateOf(sys, false).scope).toBe("personal");
    expect(duplicateOf(routine({ scope: "personal" }), true).scope).toBe("personal");
  });
});
