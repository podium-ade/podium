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

/** The four actions live behind a per-node menu, so every test starts by opening it. */
async function openMenu(name = "worker-3") {
  await userEvent.click(screen.getByRole("button", { name: `Actions for ${name}` }));
  return screen.findByRole("menu");
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

    await openMenu();
    await userEvent.click(screen.getByRole("menuitem", { name: "Drain" }));
    expect(drainNode).not.toHaveBeenCalled();

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Drain worker-3?");
    expect(dialog).toHaveTextContent(/keeps running until it finishes/);

    await userEvent.click(screen.getByRole("button", { name: "Drain node" }));
    await waitFor(() => expect(drainNode).toHaveBeenCalledWith({ nodeId: "node_01abc" }));
  });

  it("backs out of a confirm without calling anything", async () => {
    mount();
    await openMenu();
    await userEvent.click(screen.getByRole("menuitem", { name: "Drain" }));
    await screen.findByRole("dialog");

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(drainNode).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("button", { name: "Actions for worker-3" })).toBeInTheDocument();
  });

  it("offers Undrain instead of Drain once the node is draining", async () => {
    undrainNode.mockResolvedValue({ node: node() });
    mount(node({ draining: true }));

    await openMenu();
    expect(screen.queryByRole("menuitem", { name: "Drain" })).toBeNull();
    await userEvent.click(screen.getByRole("menuitem", { name: "Undrain" }));

    await screen.findByRole("dialog");
    await userEvent.click(screen.getByRole("button", { name: "Undrain node" }));
    await waitFor(() => expect(undrainNode).toHaveBeenCalledWith({ nodeId: "node_01abc" }));
  });

  it("treats a DRAINING status the same as the draining flag", async () => {
    mount(node({ status: NodeStatus.DRAINING, draining: false }));
    await openMenu();
    expect(screen.getByRole("menuitem", { name: "Undrain" })).toBeInTheDocument();
  });

  it("keeps the server's refusal in front of the operator instead of in a toast that scrolls away", async () => {
    deleteNode.mockRejectedValue(
      new ConnectError(
        "delete node: worker-3 is online and not drained; drain it first",
        Code.FailedPrecondition,
      ),
    );
    mount();

    await openMenu();
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    await screen.findByRole("dialog");
    await userEvent.click(screen.getByRole("button", { name: "Delete node" }));

    expect(
      await screen.findByText(/is online and not drained; drain it first/),
    ).toBeInTheDocument();
    // The dialog stays open, because the checkbox that answers this refusal is inside it.
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("sends force only when the operator asks for it", async () => {
    deleteNode.mockResolvedValue({});
    mount();

    await openMenu();
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    await screen.findByRole("dialog");
    await userEvent.click(
      screen.getByRole("checkbox", { name: /not drained/ }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Delete node" }));

    await waitFor(() =>
      expect(deleteNode).toHaveBeenCalledWith({ nodeId: "node_01abc", force: true }),
    );
  });

  it("offers Rekey only for a node bound to a Tailscale device", async () => {
    const unbound = mount();
    await openMenu();
    expect(screen.queryByRole("menuitem", { name: "Rekey" })).toBeNull();
    unbound.unmount();

    rekeyNode.mockResolvedValue({ node: node() });
    mount(node({ tsStableId: "nWxYz" }));
    await openMenu();
    await userEvent.click(screen.getByRole("menuitem", { name: "Rekey" }));
    await screen.findByRole("dialog");
    await userEvent.click(screen.getByRole("button", { name: "Rekey node" }));
    await waitFor(() => expect(rekeyNode).toHaveBeenCalledWith({ nodeId: "node_01abc" }));
  });
});
