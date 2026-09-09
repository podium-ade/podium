import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { NodeEditPage } from "./NodeEditPage";
import { ToastHost } from "../components/Toast";
import { TooltipProvider } from "../components/ui/tooltip";
import { NodeSchema } from "../gen/podium/v1/admin_pb";
import { NodeCapacitySchema, NodeStatus } from "../gen/podium/v1/common_pb";

const listNodes = vi.fn();
const setNodeSlots = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    admin: {
      listNodes: (...a: unknown[]) => listNodes(...a),
      setNodeSlots: (...a: unknown[]) => setNodeSlots(...a),
    },
  };
});

// The capacity is a real message rather than a plain init object: an override spread over it
// can itself be a whole Node, and tsc then holds the nested field to the message type too.
const capacity = create(NodeCapacitySchema, { maxTasks: 4, cpuCores: 8, memoryMb: 16384n });

function node(over: MessageInitShape<typeof NodeSchema> = {}) {
  return create(NodeSchema, {
    id: "node_01abc",
    name: "worker-3",
    status: NodeStatus.ONLINE,
    capacity,
    runningTasks: 1,
    freeSlots: 3,
    ...over,
  });
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <ToastHost>
          <MemoryRouter initialEntries={["/nodes/node_01abc"]}>
            <Routes>
              <Route path="/nodes/:id" element={<NodeEditPage />} />
            </Routes>
          </MemoryRouter>
        </ToastHost>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

function slotInput() {
  return screen.getByLabelText("Tasks at once");
}

describe("NodeEditPage", () => {
  beforeEach(() => {
    listNodes.mockReset();
    setNodeSlots.mockReset();
  });

  it("seeds the slot count from the node's own max_tasks and saves a new one", async () => {
    listNodes.mockResolvedValue({ nodes: [node()] });
    setNodeSlots.mockResolvedValue({ node: node({ maxTasksOverride: 8 }) });
    mount();

    await waitFor(() => expect(slotInput()).toHaveValue(4));
    // Nothing has changed yet, so there is nothing to save.
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();

    await userEvent.clear(slotInput());
    await userEvent.type(slotInput(), "8");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(setNodeSlots).toHaveBeenCalledWith({ nodeId: "node_01abc", maxTasks: 8 });
  });

  it("shows the node's own number beside an override, and clears it with zero", async () => {
    listNodes.mockResolvedValue({ nodes: [node({ maxTasksOverride: 2 })] });
    setNodeSlots.mockResolvedValue({ node: node() });
    mount();

    await waitFor(() => expect(slotInput()).toHaveValue(2));
    expect(screen.getByText(/The node's own configuration says 4/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Use the node's 4/ }));
    expect(setNodeSlots).toHaveBeenCalledWith({ nodeId: "node_01abc", maxTasks: 0 });
  });

  it("offers no reset when the node is on its own configuration", async () => {
    listNodes.mockResolvedValue({ nodes: [node()] });
    mount();

    await waitFor(() => expect(slotInput()).toHaveValue(4));
    expect(screen.queryByRole("button", { name: /Use the node's/ })).not.toBeInTheDocument();
  });

  it("refuses a count outside the range rather than sending it", async () => {
    listNodes.mockResolvedValue({ nodes: [node()] });
    mount();

    await waitFor(() => expect(slotInput()).toHaveValue(4));
    await userEvent.clear(slotInput());
    await userEvent.type(slotInput(), "900");

    expect(screen.getByText(/whole number between 1 and 256/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(setNodeSlots).not.toHaveBeenCalled();
  });

  it("says a lower count takes nothing down when the node is over it", async () => {
    listNodes.mockResolvedValue({
      nodes: [node({ maxTasksOverride: 1, runningTasks: 3, freeSlots: 0 })],
    });
    mount();

    await waitFor(() => expect(slotInput()).toHaveValue(1));
    expect(screen.getByText(/worker-3 is running more than 1/)).toBeInTheDocument();
  });

  it("says the change will be applied on reconnect for an offline node", async () => {
    listNodes.mockResolvedValue({ nodes: [node({ status: NodeStatus.OFFLINE })] });
    mount();

    await waitFor(() => expect(slotInput()).toHaveValue(4));
    expect(screen.getByText(/applied when it next reconnects/)).toBeInTheDocument();
    // Slot counts come off the live stream, so there is nothing honest to report for them.
    expect(screen.getAllByText("not reporting").length).toBeGreaterThan(0);
  });

  it("surfaces the server's refusal", async () => {
    listNodes.mockResolvedValue({ nodes: [node()] });
    setNodeSlots.mockRejectedValue(
      new ConnectError("set slots: max_tasks is 900", Code.InvalidArgument),
    );
    mount();

    await waitFor(() => expect(slotInput()).toHaveValue(4));
    await userEvent.clear(slotInput());
    await userEvent.type(slotInput(), "8");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText(/max_tasks is 900/)).toBeInTheDocument();
  });

  it("says so plainly when nothing is enrolled under that ID", async () => {
    listNodes.mockResolvedValue({ nodes: [] });
    mount();

    expect(await screen.findByText("No such node")).toBeInTheDocument();
  });
});
