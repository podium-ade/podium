import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { SnapshotsPage } from "./SnapshotsPage";
import { ToastHost } from "../components/Toast";

const listSnapshots = vi.fn();
const deleteSnapshot = vi.fn();
const setSnapshotRetention = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    snapshots: {
      listSnapshots: (...a: unknown[]) => listSnapshots(...a),
      deleteSnapshot: (...a: unknown[]) => deleteSnapshot(...a),
      setSnapshotRetention: (...a: unknown[]) => setSnapshotRetention(...a),
    },
    admin: { listNodes: () => Promise.resolve({ nodes: [{ id: "node_1", name: "desk" }] }) },
  };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ToastHost>
          <SnapshotsPage />
        </ToastHost>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const day = 86_400_000;
const snap = (id: string, daysOld: number, gib: number, inUse = false) => ({
  sessionId: id,
  sizeBytes: BigInt(gib * 1024 ** 3),
  sha256: "abc",
  nodeId: "node_1",
  taskId: `task_${id}`,
  updatedAt: timestampFromDate(new Date(Date.now() - daysOld * day)),
  inUse,
});

describe("SnapshotsPage", () => {
  beforeEach(() => {
    listSnapshots.mockReset();
    deleteSnapshot.mockReset();
    setSnapshotRetention.mockReset();
    listSnapshots.mockResolvedValue({
      enabled: true,
      snapshots: [snap("sess_new", 0, 1), snap("sess_old", 10, 2), snap("sess_busy", 20, 1, true)],
      retention: { maxAgeSeconds: 0n, maxTotalBytes: 0n, updatedBy: "" },
    });
  });

  it("lists every snapshot with its size, node and task", async () => {
    mount();
    expect(await screen.findByText("sess_old")).toBeInTheDocument();
    expect(screen.getByTestId("snapshot-summary")).toHaveTextContent("3 snapshots, 4.0 GB");
    expect(screen.getByRole("link", { name: "task_sess_old" })).toHaveAttribute("href", "/tasks/task_sess_old");
    expect(await screen.findAllByRole("link", { name: "desk" })).toHaveLength(3);
    expect(screen.getByText("in use")).toBeInTheDocument();
    expect(screen.getByTestId("retention-preview")).toHaveTextContent("Nothing is deleted automatically.");
  });

  it("previews what a policy deletes and saves it", async () => {
    setSnapshotRetention.mockResolvedValue({ removed: 1, freedBytes: 2n * 1024n ** 3n, retention: {} });
    mount();
    await screen.findByText("sess_old");

    await userEvent.type(screen.getByLabelText("Delete after this many days unused"), "7");
    // The busy one is older still, but a running task keeps it.
    expect(screen.getByTestId("retention-preview")).toHaveTextContent("Saving deletes 1 snapshot, 2.0 GB.");
    expect(screen.getAllByText("past retention")).toHaveLength(1);

    await userEvent.click(screen.getByRole("button", { name: "Save retention" }));
    await waitFor(() =>
      expect(setSnapshotRetention).toHaveBeenCalledWith({ maxAgeSeconds: 7n * 86_400n, maxTotalBytes: 0n }),
    );
  });

  it("refuses a limit that is not a positive number", async () => {
    mount();
    await screen.findByText("sess_old");
    await userEvent.type(screen.getByLabelText("Keep the total under (GB)"), "-1");
    expect(screen.getByRole("button", { name: "Save retention" })).toBeDisabled();
  });

  it("deletes one snapshot after confirming", async () => {
    deleteSnapshot.mockResolvedValue({});
    mount();
    await screen.findByText("sess_old");
    await userEvent.click(screen.getByRole("button", { name: "Delete snapshot sess_old" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete 2.0 GB" }));
    await waitFor(() => expect(deleteSnapshot).toHaveBeenCalledWith({ sessionId: "sess_old" }));
  });

  it("says so when the server has no object store", async () => {
    listSnapshots.mockResolvedValue({ enabled: false, snapshots: [], retention: {} });
    mount();
    expect(await screen.findByText("This server has no object store configured")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save retention" })).not.toBeInTheDocument();
  });
});
