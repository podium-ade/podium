import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { MemoryRouter } from "react-router";
import { TasksPage } from "./TasksPage";
import { ToastHost } from "../components/Toast";
import { TaskStatus } from "../gen/podium/v1/common_pb";

const listTasks = vi.fn();
const cancelTask = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    tasks: {
      listTasks: (...a: unknown[]) => listTasks(...a),
      cancelTask: (...a: unknown[]) => cancelTask(...a),
    },
  };
});

const ago = (s: number) => timestampFromDate(new Date(Date.now() - s * 1000));

const running = {
  id: "tsk_01J8ZC4M2QK7X9NRWD3PVB",
  spec: { image: "ghcr.io/example/etl:9" },
  status: TaskStatus.RUNNING,
  nodeId: "nod_7KQ2WXB4",
  attempts: 1,
  createdAt: ago(400),
  startedAt: ago(390),
  requestedBy: "alvaro@example.com",
  failureReason: "",
  queuedReason: "",
};

const queued = {
  id: "tsk_01J8ZBWK4R2D8YFQNA5JZX",
  spec: { image: "ghcr.io/example/pgdump:v3" },
  status: TaskStatus.QUEUED,
  nodeId: "",
  attempts: 0,
  createdAt: ago(90),
  requestedBy: "alvaro@example.com",
  failureReason: "",
  queuedReason: "no online node carries the label gpu",
  lastScheduleAttemptAt: ago(12),
};

const succeeded = {
  id: "tsk_01J8ZC1A7FD0M4TBQR8HKN",
  spec: { image: "alpine:3.20" },
  status: TaskStatus.SUCCEEDED,
  nodeId: "nod_7KQ2WXB4",
  attempts: 1,
  createdAt: ago(1900),
  startedAt: ago(1880),
  finishedAt: ago(1876),
  exitCode: 0,
  requestedBy: "alvaro@example.com",
  failureReason: "",
  queuedReason: "",
};

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter>
          <TasksPage />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("TasksPage", () => {
  beforeEach(() => {
    listTasks.mockReset();
    cancelTask.mockReset();
    listTasks.mockResolvedValue({ tasks: [running, queued, succeeded], nextCursor: "" });
  });

  it("counts the page it is showing by status", async () => {
    mount();
    expect(await screen.findByRole("button", { name: /^on this page/i })).toHaveAccessibleName(
      /3/,
    );
    expect(screen.getByRole("button", { name: /^running/i })).toHaveAccessibleName(/1/);
    expect(screen.getByRole("button", { name: /^failed/i })).toHaveAccessibleName(/0/);
  });

  it("says why a queued task is still queued", async () => {
    mount();
    expect(await screen.findByTestId("queued-reason")).toHaveTextContent(
      /no online node carries the label gpu/,
    );
  });

  it("filters to one status from its counter and clears again", async () => {
    const user = userEvent.setup();
    mount();
    await user.click(await screen.findByRole("button", { name: /^queued/i }));

    await waitFor(() =>
      expect(listTasks).toHaveBeenCalledWith(
        expect.objectContaining({ filter: { status: [TaskStatus.QUEUED], search: "" } }),
      ),
    );

    await user.click(screen.getByRole("button", { name: /clear filters/i }));
    await waitFor(() =>
      expect(listTasks).toHaveBeenLastCalledWith(
        expect.objectContaining({ filter: { status: [], search: "" } }),
      ),
    );
  });

  it("sends the search box to the server as a filter", async () => {
    const user = userEvent.setup();
    mount();
    await user.type(await screen.findByLabelText("Search"), "pgdump");
    await waitFor(() =>
      expect(listTasks).toHaveBeenLastCalledWith(
        expect.objectContaining({ filter: { status: [], search: "pgdump" } }),
      ),
    );
  });

  it("cancels a running task only after the dialog names it", async () => {
    const user = userEvent.setup();
    cancelTask.mockResolvedValue({ task: running });
    mount();

    await user.click(await screen.findByRole("button", { name: `Actions for task ${running.id}` }));
    await user.click(await screen.findByRole("menuitem", { name: /cancel task/i }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(running.id);
    expect(cancelTask).not.toHaveBeenCalled();

    await user.click(await screen.findByRole("button", { name: /^cancel task$/i }));
    await waitFor(() =>
      expect(cancelTask).toHaveBeenCalledWith(
        expect.objectContaining({ taskId: running.id }),
      ),
    );
  });

  it("does not offer cancel on a task that has already finished", async () => {
    const user = userEvent.setup();
    listTasks.mockResolvedValue({ tasks: [succeeded], nextCursor: "" });
    mount();

    await user.click(
      await screen.findByRole("button", { name: `Actions for task ${succeeded.id}` }),
    );
    expect(await screen.findByRole("menuitem", { name: /copy id/i })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /cancel task/i })).not.toBeInTheDocument();
  });

  it("disables paging where there is nowhere to page to", async () => {
    mount();
    expect(await screen.findByRole("button", { name: /previous/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /next/i })).toBeDisabled();
  });

  it("offers a way out of a search that matched nothing", async () => {
    const user = userEvent.setup();
    mount();
    listTasks.mockResolvedValue({ tasks: [], nextCursor: "" });
    await user.type(await screen.findByLabelText("Search"), "nope");

    expect(await screen.findByText(/nothing matches these filters/i)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /clear filters/i }).length).toBeGreaterThan(0);
  });
});
