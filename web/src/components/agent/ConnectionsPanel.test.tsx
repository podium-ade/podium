import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConnectionsPanel } from "./ConnectionsPanel";
import { ToastHost } from "../Toast";

const getConnections = vi.fn();
const setSlackConnection = vi.fn();
const clearSlackConnection = vi.fn();
const setGitHubConnection = vi.fn();
const clearGitHubConnection = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      getConnections: (...a: unknown[]) => getConnections(...a),
      setSlackConnection: (...a: unknown[]) => setSlackConnection(...a),
      clearSlackConnection: (...a: unknown[]) => clearSlackConnection(...a),
      setGitHubConnection: (...a: unknown[]) => setGitHubConnection(...a),
      clearGitHubConnection: (...a: unknown[]) => clearGitHubConnection(...a),
    },
  };
});

const empty = {
  slack: { configured: false, source: "", appTokenHint: "", botTokenHint: "", restartRequired: false },
  github: {
    configured: false,
    reviews: false,
    source: "",
    appId: "",
    privateKeySet: false,
    webhookListen: "",
    webhookSecretSet: false,
    restartRequired: false,
  },
  linear: { available: false, configured: false },
};

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <ConnectionsPanel />
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("ConnectionsPanel", () => {
  beforeEach(() => {
    getConnections.mockReset();
    setSlackConnection.mockReset();
    clearSlackConnection.mockReset();
    setGitHubConnection.mockReset();
    clearGitHubConnection.mockReset();
    getConnections.mockResolvedValue(empty);
  });

  it("shows GitHub and Slack forms and Linear as coming soon", async () => {
    mount();
    expect(await screen.findByRole("heading", { name: "GitHub" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Slack" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Linear" })).toBeInTheDocument();
    expect(screen.getByText("Coming soon")).toBeInTheDocument();
    expect(screen.getByLabelText("App ID")).toBeInTheDocument();
    expect(screen.getByLabelText("App-level token")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove saved Slack" })).toBeNull();
  });

  it("saves a Slack pair and keeps a blank field when one is already saved", async () => {
    getConnections.mockResolvedValue({
      ...empty,
      slack: {
        configured: true,
        source: "saved",
        appTokenHint: "1234",
        botTokenHint: "5678",
        restartRequired: true,
        setBy: "dev",
      },
    });
    setSlackConnection.mockResolvedValue({
      slack: { configured: true, source: "saved", restartRequired: true, appTokenHint: "1234", botTokenHint: "9999" },
    });
    mount();
    expect(await screen.findByText(/Ending in 1234/)).toBeInTheDocument();
    expect(screen.getByText(/Restart the conductor/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove saved Slack" })).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Bot token"), "xoxb-new");
    await userEvent.click(screen.getByRole("button", { name: "Save Slack" }));
    expect(setSlackConnection).toHaveBeenCalledWith({ appToken: "", botToken: "xoxb-new" });
  });

  it("says when Linear is still coming from the environment", async () => {
    getConnections.mockResolvedValue({ ...empty, linear: { available: false, configured: true } });
    mount();
    expect(await screen.findByText(/still using the Linear key/)).toBeInTheDocument();
    expect(screen.getByText("Coming soon")).toBeInTheDocument();
  });
});
