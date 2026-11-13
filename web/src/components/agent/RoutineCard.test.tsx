import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { MemoryRouter } from "react-router";
import { RoutineSchema, type RoutineDraft } from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { QuietRuns, RoutineCard } from "./RoutineCard";
import { RoutineDialog } from "./RoutineDialog";

const getRoutine = vi.fn();
const pauseRoutine = vi.fn();
const runRoutineNow = vi.fn();
const resolveRoutineNote = vi.fn();
const cancelRoutineRun = vi.fn();
const listRoutineRevisions = vi.fn();
const listRoutineRuns = vi.fn();
const getConnections = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      getRoutine: (...a: unknown[]) => getRoutine(...a),
      pauseRoutine: (...a: unknown[]) => pauseRoutine(...a),
      runRoutineNow: (...a: unknown[]) => runRoutineNow(...a),
      resolveRoutineNote: (...a: unknown[]) => resolveRoutineNote(...a),
      cancelRoutineRun: (...a: unknown[]) => cancelRoutineRun(...a),
      listRoutineRevisions: (...a: unknown[]) => listRoutineRevisions(...a),
      listRoutineRuns: (...a: unknown[]) => listRoutineRuns(...a),
      getConnections: (...a: unknown[]) => getConnections(...a),
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
      <ToastHost>
        <MemoryRouter>{node}</MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  for (const fn of [getRoutine, pauseRoutine, runRoutineNow, resolveRoutineNote, cancelRoutineRun, listRoutineRevisions])
    fn.mockReset();
  listRoutineRevisions.mockResolvedValue({ revisions: [] });
  listRoutineRuns.mockReset();
  listRoutineRuns.mockResolvedValue({ runs: [] });
  getConnections.mockReset();
  getConnections.mockResolvedValue({ linear: { configured: true } });
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

  it("says a routine on Linear events runs on them, and what is waiting instead of a next run", async () => {
    getRoutine.mockResolvedValue({
      routine: routine({
        schedule: "",
        scheduleText: "On Linear: issue created (ENG)",
        timezone: "America/Toronto",
        trigger: "linear",
        events: ["Issue.create"],
        teams: ["ENG"],
        nextRunAt: undefined,
        pendingEvents: 2,
        idleCount: 0,
      }),
    });
    mount(<RoutineCard chatId="chat_r" />);

    expect(await screen.findByText("Runs on Linear: issue created (ENG)")).toBeInTheDocument();
    expect(screen.queryByText(/America\/Toronto/)).not.toBeInTheDocument();
    const status = screen.getByTestId("routine-status");
    expect(status).toHaveTextContent("2 events waiting");
    expect(status).not.toHaveTextContent("Next");
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

  it("opens its instruction and notes when asked, and keeps what it remembers to itself", async () => {
    getRoutine.mockResolvedValue({ routine: routine({ notes: "- skip drafts" }) });
    mount(<RoutineCard chatId="chat_r" />);
    expect(screen.queryByTestId("routine-details")).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "Show details" }));
    const details = screen.getByTestId("routine-details");
    expect(details).toHaveTextContent("Report pull requests whose CI failed.");
    expect(details).toHaveTextContent("- skip drafts");
    // The state is the agent's own bookkeeping; the card does not show it.
    expect(screen.queryByText(/acme\/api#7@abc/)).not.toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    // Closed; the element itself leaves once its closing animation ends, which jsdom never plays.
    await waitFor(() => expect(screen.getByTestId("routine-card")).toHaveAttribute("data-state", "closed"));
  });

  it("names the ticket in a ticket's chat and links the routine's own chat", async () => {
    getRoutine.mockResolvedValue({
      routine: routine({
        schedule: "",
        scheduleText: "On Linear: issue created",
        trigger: "linear",
        events: ["Issue.create"],
        chatMode: "subject",
        nextRunAt: undefined,
      }),
      subjectTitle: "ENG-12 Fix login",
    });
    mount(<RoutineCard chatId="chat_ticket" />);

    expect(await screen.findByText("Runs on Linear: issue created")).toBeInTheDocument();
    expect(screen.getByText("· a chat per ticket", { exact: false })).toBeInTheDocument();
    const ticket = screen.getByTestId("routine-ticket");
    expect(ticket).toHaveTextContent("This chat is for ENG-12 Fix login.");
    expect(within(ticket).getByRole("link", { name: "Open the routine’s chat" })).toHaveAttribute(
      "href",
      "/agent/chat/chat_r",
    );
  });

  it("says nothing of tickets in the routine's own chat", async () => {
    getRoutine.mockResolvedValue({
      routine: routine({ trigger: "linear", scheduleText: "On Linear: issue created", chatMode: "subject" }),
      subjectTitle: "",
    });
    mount(<RoutineCard chatId="chat_r" />);
    expect(await screen.findByTestId("routine-card")).toBeInTheDocument();
    expect(screen.queryByTestId("routine-ticket")).not.toBeInTheDocument();
  });

  it("is nothing for a chat with no routine", async () => {
    getRoutine.mockResolvedValue({ routine: undefined });
    const { container } = mount(<RoutineCard chatId="chat_x" />);
    await waitFor(() => expect(getRoutine).toHaveBeenCalled());
    expect(container.querySelector('[data-testid="routine-card"]')).toBeNull();
  });
});

describe("RoutineDialog", () => {
  it("offers an admin a routine on Linear events, with its events and teams instead of a schedule", async () => {
    getConnections.mockResolvedValue({ linear: { configured: false } });
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog open title="Routine"
        lockAssistant
        canChooseTrigger
        initial={{ name: "Triage", instruction: "Label each new issue.", timezone: "UTC" }}
        onClose={() => {}}
        onSave={onSave}
      />,
    );
    expect(screen.getByTestId("schedule-summary")).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("routine-trigger-linear"));
    expect(screen.queryByTestId("schedule-summary")).not.toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Issue created" })).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Project update removed" })).toBeInTheDocument();
    // No event, nothing to run on.
    expect(screen.getByTestId("routine-save")).toBeDisabled();

    // Not connected: it says so and where, and still saves.
    expect(await screen.findByText(/The Linear app is not connected/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Connections" })).toHaveAttribute("href", "/org/settings/connections");

    await userEvent.click(screen.getByTestId("routine-event-Comment.create"));
    await userEvent.click(screen.getByTestId("routine-event-Issue.create"));
    await userEvent.type(screen.getByTestId("routine-teams"), "eng, web eng");
    expect(screen.getByText("Leave empty for every team.", { exact: false })).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("routine-save"));

    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({
          trigger: "linear",
          schedule: "",
          events: ["Issue.create", "Comment.create"],
          teams: ["ENG", "WEB"],
          chatMode: "routine",
        }),
        "personal",
      ),
    );
  });

  it("offers a chat per ticket for a routine on Linear events, and says what each choice means", async () => {
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog open title="Routine"
        lockAssistant
        canChooseTrigger
        initial={{ name: "Triage", instruction: "Label each new issue.", timezone: "UTC" }}
        onClose={() => {}}
        onSave={onSave}
      />,
    );
    // A schedule has no tickets: the choice is not offered.
    expect(screen.queryByTestId("routine-chat-mode-subject")).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("routine-trigger-linear"));
    expect(screen.getByText("Runs happen in")).toBeInTheDocument();
    expect(screen.getByTestId("routine-chat-mode-routine")).toHaveAttribute("data-state", "on");
    expect(screen.getByTestId("routine-chat-mode-hint")).toHaveTextContent(
      "Every run is in one chat, with the whole history in view.",
    );
    await userEvent.click(screen.getByTestId("routine-chat-mode-subject"));
    expect(screen.getByTestId("routine-chat-mode-hint")).toHaveTextContent(
      "Each issue or project gets its own chat, so a run sees what was said about that ticket before.",
    );
    await userEvent.click(screen.getByTestId("routine-event-Issue.create"));
    await userEvent.click(screen.getByTestId("routine-save"));
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({ trigger: "linear", chatMode: "subject" }),
        "personal",
      ),
    );
  });

  it("keeps a routine's chat per ticket when editing, and drops it for a schedule", async () => {
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog open title="Routine"
        lockAssistant
        canChooseTrigger
        initial={{
          name: "Triage",
          instruction: "Label each new issue.",
          timezone: "UTC",
          trigger: "linear",
          events: ["Issue.create"],
          chatMode: "subject",
        }}
        onClose={() => {}}
        onSave={onSave}
      />,
    );
    expect(screen.getByTestId("routine-chat-mode-subject")).toHaveAttribute("data-state", "on");
    await userEvent.click(screen.getByTestId("routine-trigger-schedule"));
    expect(screen.queryByTestId("routine-chat-mode-subject")).not.toBeInTheDocument();
    await userEvent.click(screen.getByTestId("routine-save"));
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ trigger: "schedule", chatMode: "routine" }), "personal"),
    );
  });

  it("does not offer Linear events to a person who is not an admin", () => {
    mount(<RoutineDialog open title="Routine" initial={{}} onClose={() => {}} onSave={async () => {}} />);
    expect(screen.queryByTestId("routine-trigger-linear")).not.toBeInTheDocument();
    expect(screen.getByTestId("schedule-summary")).toBeInTheDocument();
  });

  it("saves a schedule with no events, so a routine is never both", async () => {
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog open title="Routine"
        lockAssistant
        canChooseTrigger
        initial={{
          name: "Triage",
          instruction: "Label each new issue.",
          timezone: "UTC",
          trigger: "linear",
          events: ["Issue.create"],
          teams: ["ENG"],
        }}
        onClose={() => {}}
        onSave={onSave}
      />,
    );
    expect(screen.getByTestId("routine-teams")).toHaveValue("ENG");
    await userEvent.click(screen.getByTestId("routine-trigger-schedule"));
    await userEvent.click(screen.getByTestId("routine-save"));
    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({ trigger: "schedule", events: [], teams: [], schedule: "0 9 * * 1-5" }),
        "personal",
      ),
    );
  });

  it("saves a preset schedule, and shows the server's refusal as it says it", async () => {
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>();
    onSave.mockRejectedValueOnce(
      new ConnectError("invalid schedule: runs every 5 minutes", Code.InvalidArgument),
    );
    mount(
      <RoutineDialog open title="Routine" initial={{ timezone: "America/Toronto" }} onClose={() => {}} onSave={onSave} />,
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
      "personal",
    );
  });

  it("builds a schedule the way a calendar does, and says it in words", async () => {
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog open title="Routine"
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
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ schedule: "0 */2 * * 1-4,6" }), "personal"));
  });

  it("takes any time typed, not only the listed ones", async () => {
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog open title="Routine"
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
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ schedule: "42 17 * * 1-5" }), "personal"));
  });

  it("opens a schedule Repeat cannot say on the Cron tab", () => {
    mount(
      <RoutineDialog open title="Routine"
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
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog open title="Routine"
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
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ timezone: "Europe/Paris" }), "personal"));
  });
});

describe("system routines", () => {
  it("are marked System and as running as Podium, stop a run, and show who changed what", async () => {
    getRoutine.mockResolvedValue({
      routine: routine({ scope: "system", running: true, dailyCapUsd: 5, createdBy: "ana" }),
    });
    cancelRoutineRun.mockResolvedValue({
      routine: routine({ scope: "system", running: false, dailyCapUsd: 5 }),
    });
    listRoutineRevisions.mockResolvedValue({
      revisions: [
        { id: 2n, instruction: "Fix it.", notes: "", schedule: "0 9 * * 1-5", timezone: "UTC", changedBy: "bo" },
        { id: 1n, instruction: "Report it.", notes: "", schedule: "0 9 * * 1-5", timezone: "UTC", changedBy: "ana" },
      ],
    });
    mount(<RoutineCard chatId="chat_r" />);

    expect(await screen.findByTestId("routine-system-badge")).toHaveTextContent("System");
    expect(screen.getByTestId("routine-status")).toHaveTextContent("Runs as Podium");
    await userEvent.click(screen.getByRole("button", { name: "Stop run" }));
    await waitFor(() => expect(cancelRoutineRun).toHaveBeenCalledWith({ id: "rtn_1" }));

    await userEvent.click(screen.getByRole("button", { name: "Show details" }));
    const details = await screen.findByTestId("routine-details");
    expect(details).not.toHaveTextContent("Repositories");
    expect(details).toHaveTextContent("$5.00 a day");
    const history = await screen.findByTestId("routine-history");
    await waitFor(() => expect(history).toHaveTextContent("Changed the instruction"));
    expect(history).toHaveTextContent("bo");
    expect(history).toHaveTextContent("Set up");
  });
});

describe("RoutineDialog for an admin", () => {
  it("makes a system routine: no assistant, no repository list, and a template to start from", async () => {
    const onSave = vi.fn<(d: RoutineDraft, scope: string) => Promise<void>>().mockResolvedValue();
    mount(
      <RoutineDialog open title="Routine" canChooseScope initial={{ timezone: "UTC" }} onClose={() => {}} onSave={onSave} />,
    );
    expect(screen.getByTestId("routine-scope-hint")).toHaveTextContent("Runs as you");

    await userEvent.click(screen.getByTestId("routine-scope-system"));
    expect(screen.getByTestId("routine-scope-hint")).toHaveTextContent("Runs as nobody");
    await userEvent.click(screen.getByTestId("routine-template-ci"));
    expect(screen.getByTestId("routine-name")).toHaveValue("Fix failing CI");
    expect(screen.getByTestId("schedule-summary")).toHaveTextContent("Every 10 minutes");

    // Repositories are named in the instruction, in words; there is no list to fill.
    expect((screen.getByTestId("routine-instruction") as HTMLTextAreaElement).value).toContain("[name the repositories");
    expect(screen.queryByText("Repositories")).not.toBeInTheDocument();
    await userEvent.type(screen.getByTestId("routine-cap"), "4.5");
    await userEvent.click(screen.getByTestId("routine-save"));

    await waitFor(() =>
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({ name: "Fix failing CI", repos: [], dailyCapUsd: 4.5, personalityId: "" }),
        "system",
      ),
    );
  });

  it("offers no choice of who it runs as to a member, nor when editing", () => {
    mount(<RoutineDialog open title="Routine" initial={{}} onClose={() => {}} onSave={vi.fn()} />);
    expect(screen.queryByTestId("routine-scope-system")).not.toBeInTheDocument();
  });

  it("refuses a spend cap that is not an amount", async () => {
    mount(<RoutineDialog open title="Routine" initial={{ name: "x", instruction: "y" }} onClose={() => {}} onSave={vi.fn()} />);
    await userEvent.type(screen.getByTestId("routine-cap"), "lots");
    expect(screen.getByText(/amount in dollars/)).toBeInTheDocument();
    expect(screen.getByTestId("routine-save")).toBeDisabled();
  });
});

describe("quiet runs", () => {
  const ago = (min: number) => timestampFromDate(new Date(Date.now() - min * 60_000));
  const runs = [
    { id: "r3", status: "idle", summary: "All open PRs have passing CI checks", createdAt: ago(5) },
    { id: "r2", status: "skipped_overlap", summary: "", createdAt: ago(15) },
    { id: "r1", status: "done", summary: "1 PR failing", createdAt: ago(25) },
  ];

  it("are listed in the card's details, with what each found", async () => {
    getRoutine.mockResolvedValue({ routine: routine() });
    listRoutineRuns.mockResolvedValue({ runs });
    mount(<RoutineCard chatId="chat_r" />);
    await userEvent.click(await screen.findByRole("button", { name: "Show details" }));
    const list = await screen.findByTestId("routine-runs");
    await waitFor(() => expect(list).toHaveTextContent("Nothing to do"));
    expect(list).toHaveTextContent("All open PRs have passing CI checks");
    expect(list).toHaveTextContent("Skipped, busy");
    expect(list).toHaveTextContent("Answered");
    expect(listRoutineRuns).toHaveBeenCalledWith(expect.objectContaining({ id: "rtn_1", includeIdle: true }));
  });

  it("say in an empty chat that the routine has been checking", async () => {
    listRoutineRuns.mockResolvedValue({ runs });
    mount(<QuietRuns routineId="rtn_1" />);
    const line = await screen.findByTestId("routine-quiet-runs");
    expect(line).toHaveTextContent("Checked 2 times");
    expect(line).toHaveTextContent("All open PRs have passing CI checks");
  });
});
