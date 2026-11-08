import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { RoutineSchema, type RoutineDraft } from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { RoutineCard, RoutineDialog } from "./RoutineCard";

const getRoutine = vi.fn();
const pauseRoutine = vi.fn();
const runRoutineNow = vi.fn();
const resolveRoutineNote = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      getRoutine: (...a: unknown[]) => getRoutine(...a),
      pauseRoutine: (...a: unknown[]) => pauseRoutine(...a),
      runRoutineNow: (...a: unknown[]) => runRoutineNow(...a),
      resolveRoutineNote: (...a: unknown[]) => resolveRoutineNote(...a),
      listPersonalities: async () => ({ personalities: [] }),
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
    state: '{"seen":["acme/api#7@abc"]}',
    chatId: "chat_r",
    nextRunAt: timestampFromDate(new Date(Date.now() + 3_600_000)),
    idleCount: 4,
    spendTodayUsd: 0.12,
    ...over,
  });
}

function mount(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>{node}</ToastHost>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  for (const fn of [getRoutine, pauseRoutine, runRoutineNow, resolveRoutineNote]) fn.mockReset();
});

describe("RoutineCard", () => {
  it("says when it runs, how quiet it has been and what it spent, and pauses", async () => {
    getRoutine.mockResolvedValue({ routine: routine() });
    pauseRoutine.mockResolvedValue({ routine: routine({ enabled: false }) });
    mount(<RoutineCard chatId="chat_r" />);

    expect(await screen.findByText("Runs weekdays at 09:00")).toBeInTheDocument();
    const status = screen.getByTestId("routine-status");
    expect(status).toHaveTextContent(/Next today|Next tomorrow/);
    expect(status).toHaveTextContent("4 quiet runs since the last answer");
    expect(status).toHaveTextContent("$0.12 in 24h");
    expect(getRoutine).toHaveBeenCalledWith({ chatId: "chat_r" });

    await userEvent.click(screen.getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(pauseRoutine).toHaveBeenCalledWith({ id: "rtn_1" }));
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
  });

  it("shows why Podium paused it, and the note the agent proposed", async () => {
    getRoutine.mockResolvedValue({
      routine: routine({ enabled: false, pausedReason: "5 runs failed within an hour.", proposedNote: "skip drafts" }),
    });
    resolveRoutineNote.mockResolvedValue({ routine: routine({ notes: "- skip drafts" }) });
    mount(<RoutineCard chatId="chat_r" />);

    expect(await screen.findByTestId("routine-paused-reason")).toHaveTextContent("5 runs failed within an hour.");
    expect(screen.getByTestId("routine-proposed-note")).toHaveTextContent("skip drafts");
    expect(screen.getByTestId("routine-proposed-note")).toHaveTextContent("Suggested standing note:");
    await userEvent.click(screen.getByRole("button", { name: "Add to notes" }));
    await waitFor(() => expect(resolveRoutineNote).toHaveBeenCalledWith({ id: "rtn_1", accept: true }));
    await waitFor(() => expect(screen.queryByTestId("routine-proposed-note")).not.toBeInTheDocument());
  });

  it("shows what it remembers when opened", async () => {
    getRoutine.mockResolvedValue({ routine: routine() });
    mount(<RoutineCard chatId="chat_r" />);
    await userEvent.click(await screen.findByRole("button", { name: "Show details" }));
    expect(screen.getByTestId("routine-state")).toHaveTextContent("acme/api#7@abc");
    expect(screen.getByText("Report pull requests whose CI failed.")).toBeInTheDocument();
  });

  it("is nothing for a chat with no routine", async () => {
    getRoutine.mockResolvedValue({ routine: undefined });
    const { container } = mount(<RoutineCard chatId="chat_x" />);
    await waitFor(() => expect(getRoutine).toHaveBeenCalled());
    expect(container.querySelector('[data-testid="routine-card"]')).toBeNull();
  });
});

describe("RoutineDialog", () => {
  it("saves a preset schedule, and shows the server's refusal as it says it", async () => {
    const onSave = vi.fn<(d: RoutineDraft) => Promise<void>>();
    onSave.mockRejectedValueOnce(
      new ConnectError("invalid schedule: runs every 5 minutes", Code.InvalidArgument),
    );
    mount(
      <RoutineDialog open title="New routine" initial={{ timezone: "America/Toronto" }} onClose={() => {}} onSave={onSave} />,
    );
    await userEvent.type(screen.getByTestId("routine-name"), "PR digest");
    await userEvent.type(screen.getByTestId("routine-instruction"), "Report failing PRs.");
    await userEvent.click(screen.getByTestId("schedule-mode-cron"));
    // The cron tab starts from what Repeat built.
    expect(screen.getByTestId("routine-cron")).toHaveValue("0 9 * * 1-5");
    await userEvent.clear(screen.getByTestId("routine-cron"));
    await userEvent.type(screen.getByTestId("routine-cron"), "*/5 * * * *");
    await userEvent.click(screen.getByTestId("routine-save"));

    expect(await screen.findByTestId("routine-error")).toHaveTextContent("runs every 5 minutes");
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "PR digest",
        instruction: "Report failing PRs.",
        schedule: "*/5 * * * *",
        timezone: "America/Toronto",
      }),
    );
  });

  it("builds a schedule the way a calendar does, and says it in words", async () => {
    const onSave = vi.fn<(d: RoutineDraft) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog
        open
        title="New routine"
        lockAssistant
        initial={{ name: "Digest", instruction: "Summarise.", timezone: "UTC" }}
        onClose={() => {}}
        onSave={onSave}
      />,
    );
    const summary = screen.getByTestId("schedule-summary");
    expect(summary).toHaveTextContent("Weekdays at 09:00");
    // Friday off, Saturday on.
    await userEvent.click(screen.getByRole("button", { name: "Friday" }));
    await userEvent.click(screen.getByRole("button", { name: "Saturday" }));
    expect(summary).toHaveTextContent("Mon, Tue, Wed, Thu and Sat at 09:00");
    await userEvent.click(screen.getByRole("radio", { name: "Hourly" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Hours between runs" }));
    await userEvent.click(await screen.findByRole("option", { name: "2 hours" }));
    expect(summary).toHaveTextContent("Every 2 hours at :00");
    await userEvent.click(screen.getByTestId("routine-save"));
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ schedule: "0 */2 * * 1-4,6" })));
  });

  it("takes any time typed, not only the listed ones", async () => {
    const onSave = vi.fn<(d: RoutineDraft) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog
        open
        title="New routine"
        lockAssistant
        initial={{ name: "Digest", instruction: "Summarise.", timezone: "UTC" }}
        onClose={() => {}}
        onSave={onSave}
      />,
    );
    await userEvent.click(screen.getByTestId("schedule-time"));
    await userEvent.type(await screen.findByRole("searchbox", { name: "Time: type one" }), "5:42pm{Enter}");
    expect(screen.getByTestId("schedule-summary")).toHaveTextContent("Weekdays at 17:42");
    await userEvent.click(screen.getByTestId("routine-save"));
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ schedule: "42 17 * * 1-5" })));
  });

  it("opens a schedule Repeat cannot say on the Cron tab", () => {
    mount(
      <RoutineDialog
        open
        title="Edit routine"
        lockAssistant
        initial={{ name: "Digest", instruction: "Summarise.", schedule: "0 9,17 * * *", timezone: "UTC" }}
        onClose={() => {}}
        onSave={vi.fn()}
      />,
    );
    expect(screen.getByTestId("routine-cron")).toHaveValue("0 9,17 * * *");
    expect(screen.getByTestId("schedule-summary")).toHaveTextContent("Custom schedule");
  });

  it("picks a timezone by searching for the place it is known by", async () => {
    const onSave = vi.fn<(d: RoutineDraft) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog
        open
        title="Edit routine"
        lockAssistant
        initial={{ name: "PR digest", instruction: "Report failing PRs.", timezone: "America/Toronto" }}
        onClose={() => {}}
        onSave={onSave}
      />,
    );
    const zone = screen.getByRole("combobox", { name: "Timezone" });
    // Toronto stands in for its timezone's row.
    expect(zone).toHaveTextContent("Eastern Time · Toronto");
    await userEvent.click(zone);
    await userEvent.type(await screen.findByRole("searchbox", { name: "Search timezones" }), "berlin");
    await userEvent.click(await screen.findByRole("option", { name: /Central Europe/ }));
    expect(zone).toHaveTextContent("Central Europe · Paris");
    await userEvent.click(screen.getByTestId("routine-save"));
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ timezone: "Europe/Paris" })));
  });
});
