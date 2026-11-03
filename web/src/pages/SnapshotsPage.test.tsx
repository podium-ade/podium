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
const listSandboxSnapshots = vi.fn();
const deleteSandboxSnapshots = vi.fn();
const setSandboxSnapshotRetention = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    snapshots: {
      listSnapshots: (...a: unknown[]) => listSnapshots(...a),
      deleteSnapshot: (...a: unknown[]) => deleteSnapshot(...a),
      setSnapshotRetention: (...a: unknown[]) => setSnapshotRetention(...a),
      listSandboxSnapshots: (...a: unknown[]) => listSandboxSnapshots(...a),
      deleteSandboxSnapshots: (...a: unknown[]) => deleteSandboxSnapshots(...a),
      setSandboxSnapshotRetention: (...a: unknown[]) => setSandboxSnapshotRetention(...a),
    },
    admin: { listNodes: () => Promise.resolve({ nodes: [{ id: "node_1", name: "desk" }] }) },
  };
});

function mount(path = "/snapshots") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
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
    listSandboxSnapshots.mockReset();
    deleteSandboxSnapshots.mockReset();
    setSandboxSnapshotRetention.mockReset();
    listSandboxSnapshots.mockResolvedValue({ snapshots: [], providers: [], retention: { maxAgeSeconds: 0n, maxCount: 0n } });
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

  describe("with Modal keeping workspaces", () => {
    const image = (id: string, daysOld: number, inUse = false) => ({
      sessionId: id,
      provider: "modal",
      imageId: `im-${id}`,
      nodeId: "node_modal",
      taskId: `task_${id}`,
      updatedAt: timestampFromDate(new Date(Date.now() - daysOld * day)),
      inUse,
    });

    beforeEach(() => {
      listSandboxSnapshots.mockResolvedValue({
        snapshots: [image("m_new", 0), image("m_mid", 3), image("m_old", 9), image("m_busy", 12, true)],
        providers: ["modal"],
        retention: { maxAgeSeconds: 0n, maxCount: 0n, updatedBy: "" },
      });
    });

    it("shows a tab of Modal's own beside the nodes", async () => {
      mount();
      expect(await screen.findByRole("tab", { name: /Modal/ })).toBeInTheDocument();
      expect(screen.getByRole("tab", { name: "Nodes", selected: true })).toBeInTheDocument();
      expect(await screen.findByText("sess_old")).toBeInTheDocument();
      expect(screen.queryByText("im-m_old")).not.toBeInTheDocument();

      await userEvent.click(screen.getByRole("tab", { name: /Modal/ }));
      expect(await screen.findByText("im-m_old")).toBeInTheDocument();
      expect(screen.getByTestId("sandbox-snapshot-summary")).toHaveTextContent("4 snapshots, kept by Modal");
      expect(screen.getByRole("link", { name: "task_m_old" })).toHaveAttribute("href", "/tasks/task_m_old");
    });

    it("has no tabs when no provider keeps images", async () => {
      listSandboxSnapshots.mockResolvedValue({ snapshots: [], providers: [], retention: {} });
      mount();
      expect(await screen.findByText("sess_old")).toBeInTheDocument();
      expect(screen.queryByRole("tab")).not.toBeInTheDocument();
    });

    it("previews and saves a retention policy by age and count", async () => {
      setSandboxSnapshotRetention.mockResolvedValue({ removed: 2, retention: {} });
      mount("/snapshots?tab=modal");
      await screen.findByText("im-m_old");

      await userEvent.type(screen.getByLabelText("Keep at most this many"), "2");
      // m_old goes; m_busy is older and over the count, but a running task keeps it.
      expect(screen.getByTestId("sandbox-retention-preview")).toHaveTextContent("Saving deletes 1 snapshot.");
      await userEvent.type(screen.getByLabelText("Delete after this many days unused"), "2");
      expect(screen.getByTestId("sandbox-retention-preview")).toHaveTextContent("Saving deletes 2 snapshots.");
      expect(screen.getAllByText("past retention")).toHaveLength(2);

      await userEvent.click(screen.getByRole("button", { name: "Save retention" }));
      await waitFor(() =>
        expect(setSandboxSnapshotRetention).toHaveBeenCalledWith({ maxAgeSeconds: 2n * 86_400n, maxCount: 2n }),
      );
      expect(await screen.findByText("Retention saved. Deleted 2 snapshots from Modal.")).toBeInTheDocument();
    });

    it("refuses a count that is not a whole number", async () => {
      mount("/snapshots?tab=modal");
      await screen.findByText("im-m_old");
      await userEvent.type(screen.getByLabelText("Keep at most this many"), "2.5");
      expect(screen.getByRole("button", { name: "Save retention" })).toBeDisabled();
    });

    it("deletes one snapshot after confirming", async () => {
      deleteSandboxSnapshots.mockResolvedValue({ removed: 1, failed: {} });
      mount("/snapshots?tab=modal");
      await screen.findByText("im-m_old");
      await userEvent.click(screen.getByRole("button", { name: "Delete snapshot m_old" }));
      expect(screen.getByRole("heading", { name: "Delete this snapshot?" })).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Delete snapshot" }));
      await waitFor(() => expect(deleteSandboxSnapshots).toHaveBeenCalledWith({ sessionIds: ["m_old"] }));
      expect(await screen.findByText("Snapshot deleted from Modal.")).toBeInTheDocument();
    });

    it("deletes the selected snapshots at once, and says which it could not", async () => {
      deleteSandboxSnapshots.mockResolvedValue({ removed: 1, failed: { m_busy: "no snapshot" } });
      mount("/snapshots?tab=modal");
      await screen.findByText("im-m_old");
      expect(screen.queryByRole("button", { name: /selected/ })).not.toBeInTheDocument();

      await userEvent.click(screen.getByRole("checkbox", { name: "Select snapshot m_old" }));
      await userEvent.click(screen.getByRole("checkbox", { name: "Select snapshot m_busy" }));
      expect(screen.getByRole("checkbox", { name: "Select every snapshot" })).toHaveAttribute("data-state", "indeterminate");
      await userEvent.click(screen.getByRole("button", { name: "Delete 2 selected" }));
      expect(screen.getByRole("heading", { name: "Delete 2 snapshots?" })).toBeInTheDocument();
      expect(screen.getByText("1 of these have a task still running")).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Delete 2 snapshots" }));
      await waitFor(() =>
        expect(deleteSandboxSnapshots).toHaveBeenCalledWith({ sessionIds: ["m_old", "m_busy"] }),
      );
      expect(await screen.findByText(/Deleted 1 of 2. Not deleted: m_busy \(no snapshot\)/)).toBeInTheDocument();
    });

    it("selects every snapshot from the header", async () => {
      mount("/snapshots?tab=modal");
      await screen.findByText("im-m_old");
      await userEvent.click(screen.getByRole("checkbox", { name: "Select every snapshot" }));
      expect(screen.getByRole("button", { name: "Delete 4 selected" })).toBeInTheDocument();
    });

    it("keeps a tab for a provider removed with snapshots left, and says so", async () => {
      listSandboxSnapshots.mockResolvedValue({
        snapshots: [image("m_old", 9)],
        providers: [],
        retention: {},
      });
      mount("/snapshots?tab=modal");
      expect(await screen.findByText("Modal is not configured as a sandbox provider")).toBeInTheDocument();
      expect(screen.getByText("im-m_old")).toBeInTheDocument();
    });
  });
});
