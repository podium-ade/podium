import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { ArtifactsPanel } from "./ArtifactsPanel";
import { ToastHost } from "./Toast";

const listArtifacts = vi.fn();
const getArtifactURL = vi.fn();
const download = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    artifacts: {
      listArtifacts: (...a: unknown[]) => listArtifacts(...a),
      getArtifactURL: (...a: unknown[]) => getArtifactURL(...a),
    },
  };
});

vi.mock("../lib/artifacts", async () => {
  const actual = await vi.importActual<typeof import("../lib/artifacts")>("../lib/artifacts");
  return { ...actual, download: (...a: unknown[]) => download(...a) };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <ArtifactsPanel taskId="task_01abc" refetch={false} />
      </ToastHost>
    </QueryClientProvider>,
  );
}

const file = {
  id: "art_01file",
  taskId: "task_01abc",
  name: "report.txt",
  objectKey: "tasks/task_01abc/report.txt",
  sizeBytes: 2048n,
  contentType: "text/plain",
  sha256: "abc",
  kind: "file",
  createdAt: timestampFromDate(new Date(Date.now() - 30_000)),
};
const log = { ...file, id: "art_01log", name: "stdout.log", kind: "log", sizeBytes: 512n };

describe("ArtifactsPanel", () => {
  beforeEach(() => {
    listArtifacts.mockReset();
    getArtifactURL.mockReset();
    download.mockReset();
  });

  it("shows name, size, content type and kind, and keeps rolled-up logs apart from files", async () => {
    listArtifacts.mockResolvedValue({ artifacts: [log, file] });
    mount();

    expect(await screen.findByText("report.txt")).toBeInTheDocument();
    expect(screen.getByText("2.0 KB")).toBeInTheDocument();
    expect(screen.getByText("512 B")).toBeInTheDocument();
    expect(screen.getAllByText("text/plain")).toHaveLength(2);
    expect(screen.getByText("file")).toBeInTheDocument();
    expect(screen.getByText("log")).toBeInTheDocument();
    expect(screen.getByText(/Archived logs/)).toBeInTheDocument();

    // Files first, then the archived-log note, then the log rows.
    const order = screen.getAllByText(/report\.txt|Archived logs|stdout\.log/);
    expect(order.map((n) => n.textContent?.slice(0, 8))).toEqual([
      "report.t",
      "Archived",
      "stdout.l",
    ]);
  });

  it("downloads through the server's proxy route rather than a presigned link", async () => {
    listArtifacts.mockResolvedValue({ artifacts: [file] });
    download.mockResolvedValue(undefined);
    mount();

    await userEvent.click(await screen.findByRole("button", { name: "Download" }));
    await waitFor(() => expect(download).toHaveBeenCalledTimes(1));
    expect(download.mock.calls[0][0]).toMatchObject({ id: "art_01file" });
    expect(getArtifactURL).not.toHaveBeenCalled();
  });

  it("uses the presigned URL only for copy-link", async () => {
    listArtifacts.mockResolvedValue({ artifacts: [file] });
    getArtifactURL.mockResolvedValue({
      url: "https://s3.example/obj?sig=x",
      expiresAt: timestampFromDate(new Date(Date.now() + 900_000)),
    });
    mount();

    await userEvent.click(await screen.findByRole("button", { name: "Copy link" }));
    await waitFor(() => expect(getArtifactURL).toHaveBeenCalledWith({ artifactId: "art_01file" }));
    expect(download).not.toHaveBeenCalled();
  });

  it("says once that the deployment has no object store, not once per row", async () => {
    listArtifacts.mockRejectedValue(
      new ConnectError("artifacts are not configured", Code.FailedPrecondition),
    );
    mount();
    expect(await screen.findByText(/no object store configured/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Download" })).toBeNull();
  });

  it("tells the operator where a task's files come from when there are none", async () => {
    listArtifacts.mockResolvedValue({ artifacts: [] });
    mount();
    expect(await screen.findByText(/Nothing stored/)).toBeInTheDocument();
    expect(screen.getByText("/workspace/.podium/artifacts/")).toBeInTheDocument();
  });
});
