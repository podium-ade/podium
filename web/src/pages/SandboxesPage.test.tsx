import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { SandboxesPage } from "./SandboxesPage";
import { ToastHost } from "../components/Toast";
import { NodeStatus } from "../gen/podium/v1/common_pb";

const listSandboxProviders = vi.fn();
const setSandboxProvider = vi.fn();
const deleteSandboxProvider = vi.fn();
const listNodes = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    sandboxProviders: {
      listSandboxProviders: (...a: unknown[]) => listSandboxProviders(...a),
      setSandboxProvider: (...a: unknown[]) => setSandboxProvider(...a),
      deleteSandboxProvider: (...a: unknown[]) => deleteSandboxProvider(...a),
    },
    admin: { listNodes: (...a: unknown[]) => listNodes(...a) },
  };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ToastHost>
          <SandboxesPage />
        </ToastHost>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const boat = {
  name: "boat",
  settings: { template: "ci-base", default_type: "small" },
  hasSecret: true,
  keyId: "k_01",
  createdBy: "alice@example.com",
  updatedAt: timestampFromDate(new Date(Date.now() - 60_000)),
  maxConcurrent: 0,
  accountLimit: 2,
  inUse: 1,
};

describe("SandboxesPage", () => {
  beforeEach(() => {
    for (const f of [listSandboxProviders, setSandboxProvider, deleteSandboxProvider, listNodes]) f.mockReset();
    listSandboxProviders.mockResolvedValue({ providers: [boat] });
    listNodes.mockResolvedValue({
      nodes: [
        { id: "n1", labels: ["backend=sandbox", "sandbox.provider=boat"], status: NodeStatus.ONLINE },
        { id: "n2", labels: ["backend=sandbox", "sandbox.provider=modal"], status: NodeStatus.OFFLINE },
      ],
    });
  });

  it("shows each provider's settings, never its key, and no node to run", async () => {
    mount();
    const row = await screen.findByTestId("provider-boat");
    expect(await within(row).findByText("configured")).toBeInTheDocument();
    expect(within(row).getByText("ci-base")).toBeInTheDocument();
    expect(within(row).getByText("stored, hidden")).toBeInTheDocument();
    expect(within(row).getByTestId("capacity-boat")).toHaveTextContent("1 of account limit 2");
    expect(within(row).getByText(/Podium runs boat.dev sandboxes itself/)).toBeInTheDocument();
    expect(within(row).queryByText(/node/)).toBeNull();

    const modal = screen.getByTestId("provider-modal");
    expect(within(modal).getByText("not set up")).toBeInTheDocument();
    expect(within(modal).getByText(/There is nothing to install/)).toBeInTheDocument();
  });

  it("offers a pool of ready sandboxes on every provider", async () => {
    mount();
    await screen.findByTestId("provider-modal");
    await userEvent.click(screen.getByRole("button", { name: "Set up Modal" }));
    expect(screen.getByLabelText("Ready sandboxes")).toBeInTheDocument();
    expect(screen.getByLabelText("Ready sandbox image")).toBeInTheDocument();
  });

  it("needs a key to set a provider up", async () => {
    setSandboxProvider.mockResolvedValue({ provider: { name: "modal" } });
    mount();
    await screen.findByTestId("provider-modal");
    await userEvent.click(screen.getByRole("button", { name: "Set up Modal" }));
    const save = screen.getByRole("button", { name: "Save Modal" });
    expect(save).toBeDisabled();

    await userEvent.type(screen.getByLabelText("Token secret"), "as-secret");
    await userEvent.type(screen.getByLabelText("Token ID"), "ak-123");
    await userEvent.selectOptions(screen.getByLabelText("Runtime"), "vm");
    expect(save).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/Max concurrent sandboxes/), "3");
    await userEvent.click(save);

    await waitFor(() => expect(setSandboxProvider).toHaveBeenCalledTimes(1));
    const sent = setSandboxProvider.mock.calls[0][0] as {
      name: string;
      settings: Record<string, string>;
      secret: Uint8Array;
      maxConcurrent: number;
    };
    expect(sent.name).toBe("modal");
    expect(sent.settings).toEqual({ token_id: "ak-123", runtime: "vm" });
    expect(sent.maxConcurrent).toBe(3);
    expect(new TextDecoder().decode(sent.secret)).toBe("as-secret");
  });

  it("edits settings and keeps the stored key when none is typed", async () => {
    setSandboxProvider.mockResolvedValue({ provider: boat });
    mount();
    await screen.findByTestId("provider-boat");
    await userEvent.click(await screen.findByRole("button", { name: "Edit boat.dev" }));
    expect(screen.getByLabelText("Template")).toHaveValue("ci-base");
    expect(screen.getByLabelText("API key")).toHaveValue("");
    expect(screen.getByLabelText("API key")).toHaveAttribute("type", "password");

    await userEvent.clear(screen.getByLabelText("Template"));
    await userEvent.type(screen.getByLabelText("Template"), "ci-v2");
    await userEvent.click(screen.getByRole("button", { name: "Save boat.dev" }));

    await waitFor(() => expect(setSandboxProvider).toHaveBeenCalledTimes(1));
    const sent = setSandboxProvider.mock.calls[0][0] as { settings: Record<string, string>; secret: Uint8Array };
    expect(sent.settings).toEqual({ template: "ci-v2", default_type: "small" });
    expect(sent.secret).toHaveLength(0);
  });

  it("warns when boat's cap is above what the account allows", async () => {
    setSandboxProvider.mockResolvedValue({ provider: boat });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit boat.dev" }));
    const cap = screen.getByLabelText(/Max concurrent sandboxes/);
    expect(screen.getByText(/The account allows 2 at once/)).toBeInTheDocument();

    await userEvent.type(cap, "2");
    expect(screen.queryByText(/Higher than the boat.dev account allows/)).not.toBeInTheDocument();
    await userEvent.clear(cap);
    await userEvent.type(cap, "5");
    expect(screen.getByText(/Higher than the boat.dev account allows/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save boat.dev" }));
    await waitFor(() => expect(setSandboxProvider).toHaveBeenCalledTimes(1));
    expect((setSandboxProvider.mock.calls[0][0] as { maxConcurrent: number }).maxConcurrent).toBe(5);
  });

  it("removes a provider after confirming", async () => {
    deleteSandboxProvider.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Remove boat.dev" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove boat.dev", hidden: false }));
    await waitFor(() => expect(deleteSandboxProvider).toHaveBeenCalledWith({ name: "boat" }));
  });
});
