import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { NodeActions } from "./NodeActions";
import { ToastHost } from "./Toast";
import { NodeSchema } from "../gen/podium/v1/admin_pb";
import { NodeStatus } from "../gen/podium/v1/common_pb";

const drainNode = vi.fn();
const undrainNode = vi.fn();
const deleteNode = vi.fn();
const rekeyNode = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    admin: {
      drainNode: (...a: unknown[]) => drainNode(...a),
      undrainNode: (...a: unknown[]) => undrainNode(...a),
      deleteNode: (...a: unknown[]) => deleteNode(...a),
      rekeyNode: (...a: unknown[]) => rekeyNode(...a),
    },
  };
});

function node(over: MessageInitShape<typeof NodeSchema> = {}) {
  return create(NodeSchema, {
    id: "node_01abc",
    name: "worker-3",
    status: NodeStatus.ONLINE,
    ...over,
  });
}

function mount(n = node()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <NodeActions node={n} />
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("NodeActions", () => {
  beforeEach(() => {
    drainNode.mockReset();
    undrainNode.mockReset();
    deleteNode.mockReset();
    rekeyNode.mockReset();
  });

  it("confirms before draining, and drains by ID", async () => {
    drainNode.mockResolvedValue({ node: node({ draining: true }) });
    mount();

    await userEvent.click(screen.getByRole("button", { name: "Drain" }));
    expect(drainNode).not.toHaveBeenCalled();
    expect(screen.getByText(/Stop scheduling new work on worker-3\?/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Yes, drain" }));
    await waitFor(() => expect(drainNode).toHaveBeenCalledWith({ nodeId: "node_01abc" }));
  });

  it("backs out of a confirm without calling anything", async () => {
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Drain" }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(drainNode).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Drain" })).toBeInTheDocument();
  });

  it("offers Undrain instead of Drain once the node is draining", async () => {
    undrainNode.mockResolvedValue({ node: node() });
    mount(node({ draining: true }));

    expect(screen.queryByRole("button", { name: "Drain" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Undrain" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, undrain" }));
    await waitFor(() => expect(undrainNode).toHaveBeenCalledWith({ nodeId: "node_01abc" }));
  });

  it("treats a DRAINING status the same as the draining flag", () => {
    mount(node({ status: NodeStatus.DRAINING, draining: false }));
    expect(screen.getByRole("button", { name: "Undrain" })).toBeInTheDocument();
  });

  it("shows the server's refusal beside the node instead of a toast that scrolls away", async () => {
    deleteNode.mockRejectedValue(
      new ConnectError(
        "delete node: worker-3 is online and not drained; drain it first",
        Code.FailedPrecondition,
      ),
    );
    mount();

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, delete" }));

    expect(
      await screen.findByText(/is online and not drained; drain it first/),
    ).toBeInTheDocument();
  });

  it("offers Rekey only for a node bound to a Tailscale device", async () => {
    mount();
    expect(screen.queryByRole("button", { name: "Rekey" })).toBeNull();

    rekeyNode.mockResolvedValue({ node: node() });
    mount(node({ tsStableId: "nWxYz" }));
    await userEvent.click(screen.getByRole("button", { name: "Rekey" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, rekey" }));
    await waitFor(() => expect(rekeyNode).toHaveBeenCalledWith({ nodeId: "node_01abc" }));
  });
});
