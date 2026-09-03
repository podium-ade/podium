import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../lib/identity";
import { AgentPage } from "./AgentPage";
import { ToastHost } from "../components/Toast";

const KEY = "sk-ant-not-a-real-key-abcd";

const getSettings = vi.fn();
const setProviderKey = vi.fn();
const clearProviderKey = vi.fn();
const listSessions = vi.fn();
const listTurns = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    agent: {
      getSettings: (...a: unknown[]) => getSettings(...a),
      setProviderKey: (...a: unknown[]) => setProviderKey(...a),
      clearProviderKey: (...a: unknown[]) => clearProviderKey(...a),
      listSessions: (...a: unknown[]) => listSessions(...a),
      listTurns: (...a: unknown[]) => listTurns(...a),
    },
  };
});

const viewer: Viewer = {
  login: "dev",
  displayName: "",
  kind: IdentityKind.DEV_TOKEN,
  agentEnabled: true,
  serverVersion: "dev",
};

function mount(path = "/agent/settings", who: Viewer | undefined = viewer) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <ViewerContext value={who}>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route path="/agent/*" element={<AgentPage />} />
            </Routes>
          </MemoryRouter>
        </ViewerContext>
      </ToastHost>
    </QueryClientProvider>,
  );
}

const notSet = { provider: { provider: "anthropic", keySet: false, model: "claude-opus-5" } };
const connected = {
  provider: {
    provider: "anthropic",
    keySet: true,
    keyHint: "abcd",
    model: "claude-opus-5",
    setBy: "dev",
  },
};

describe("AgentPage", () => {
  beforeEach(() => {
    getSettings.mockReset();
    setProviderKey.mockReset();
    clearProviderKey.mockReset();
    listSessions.mockReset();
    listTurns.mockReset();
    getSettings.mockResolvedValue(notSet);
    listSessions.mockResolvedValue({ sessions: [], nextCursor: "" });
  });

  it("says plainly that there is no conductor when the server has none", () => {
    mount("/agent/settings", { ...viewer, agentEnabled: false });
    expect(
      screen.getByText("The conductor is not configured on this control plane"),
    ).toBeInTheDocument();
    expect(screen.getByText(/PODIUM_AGENT_URL/)).toBeInTheDocument();
    expect(getSettings).not.toHaveBeenCalled();
  });

  it("redirects /agent to the settings tab", async () => {
    mount("/agent");
    expect(await screen.findByText("Anthropic")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("has one tab per screen and each is a real route", async () => {
    mount("/agent/sessions");
    expect(await screen.findByText("No sessions yet.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sessions" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    // The three tabs this build ships. 21 adds one more line to the same array.
    expect(screen.getAllByRole("link").map((a) => a.textContent)).toEqual([
      "Settings",
      "Sessions",
      "Memory",
    ]);

    await userEvent.click(screen.getByRole("link", { name: "Settings" }));
    expect(await screen.findByText("Anthropic")).toBeInTheDocument();
  });

  it("saves a key through the RPC and shows it as set afterwards", async () => {
    setProviderKey.mockResolvedValue({
      provider: connected.provider,
      models: ["claude-opus-5"],
      status: "",
    });
    mount();
    await screen.findByText("Not set");
    getSettings.mockResolvedValue(connected);

    await userEvent.type(screen.getByTestId("provider-key-input"), KEY);
    await userEvent.click(screen.getByTestId("provider-key-save"));

    await waitFor(() =>
      expect(setProviderKey).toHaveBeenCalledWith({ provider: "anthropic", key: KEY }),
    );
    expect(await screen.findByText("Connected")).toBeInTheDocument();
  });

  it("removes a key and returns to the empty state", async () => {
    getSettings.mockResolvedValue(connected);
    clearProviderKey.mockResolvedValue({});
    mount();
    await screen.findByText("Connected");
    getSettings.mockResolvedValue(notSet);

    await userEvent.click(screen.getByTestId("provider-key-remove"));
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() =>
      expect(clearProviderKey).toHaveBeenCalledWith({ provider: "anthropic" }),
    );
    expect(clearProviderKey).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("Not set")).toBeInTheDocument();
    expect(screen.getByText(/encrypted at rest by podium-server/i)).toBeInTheDocument();
  });

  it("keeps the card and warns when the conductor itself is down", async () => {
    getSettings.mockRejectedValue(
      new ConnectError("podium-agent is not reachable", Code.Unavailable),
    );
    mount();
    expect(await screen.findByText(/podium-agent is not reachable/)).toBeInTheDocument();
    // Not a blank card: the input is still there to try again with.
    expect(screen.getByTestId("provider-key-input")).toBeInTheDocument();
  });

  it("reports any other failure to read the settings as a failure", async () => {
    getSettings.mockRejectedValue(new ConnectError("boom", Code.Internal));
    mount();
    expect(await screen.findByText("Could not read the agent settings")).toBeInTheDocument();
    expect(screen.queryByTestId("provider-key-input")).toBeNull();
  });
});
