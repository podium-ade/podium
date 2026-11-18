import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IdentityKind } from "../../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../../lib/identity";
import { LINEAR_MCP_HINT_KEY, LinearMcpHint } from "./LinearMcpHint";

const listMcpServers = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listMcpServers: (...a: unknown[]) => listMcpServers(...a),
    },
  };
});

function viewer(kind = IdentityKind.USER): Viewer {
  return {
    login: "ada@acme.com",
    displayName: "Ada",
    kind,
    agentEnabled: true,
    serverVersion: "test",
    roles: ["member"],
    claimed: true,
    hostedDomain: "acme.com",
    canClaim: false,
    googleAuthEnabled: true,
    claimDomain: "acme.com",
    pictureUrl: "",
  };
}

const linear = (over: Record<string, unknown> = {}) => ({
  name: "linear",
  url: "https://mcp.linear.app/mcp",
  enabled: true,
  owner: "ada@acme.com",
  ...over,
});

function mount(v: Viewer | undefined = viewer()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <ViewerContext.Provider value={v}>
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <LinearMcpHint />
        </MemoryRouter>
      </QueryClientProvider>
    </ViewerContext.Provider>,
  );
}

describe("LinearMcpHint", () => {
  beforeEach(() => {
    listMcpServers.mockReset();
    localStorage.removeItem(LINEAR_MCP_HINT_KEY);
  });

  it("offers a person with no Linear server of their own to connect one, on the Linear preset", async () => {
    listMcpServers.mockResolvedValue({ servers: [linear({ owner: "" }), { name: "notion", url: "https://mcp.notion.com/mcp", enabled: true, owner: "ada@acme.com" }] });
    mount();
    const link = await screen.findByRole("link", { name: "Connect your Linear MCP server" });
    expect(link).toHaveAttribute("href", "/agent/mcp?add=linear");
  });

  it("says nothing to a person who has one", async () => {
    listMcpServers.mockResolvedValue({ servers: [linear()] });
    mount();
    await waitFor(() => expect(listMcpServers).toHaveBeenCalled());
    expect(screen.queryByTestId("linear-mcp-hint")).not.toBeInTheDocument();
  });

  it("offers it when theirs has another name, which no turn asks for", async () => {
    listMcpServers.mockResolvedValue({ servers: [linear({ name: "my-linear" })] });
    mount();
    expect(await screen.findByTestId("linear-mcp-hint")).toBeInTheDocument();
  });

  it("still offers it when theirs is switched off", async () => {
    listMcpServers.mockResolvedValue({ servers: [linear({ enabled: false })] });
    mount();
    expect(await screen.findByTestId("linear-mcp-hint")).toBeInTheDocument();
  });

  it("says nothing to the local token, which has no servers of its own", () => {
    listMcpServers.mockResolvedValue({ servers: [] });
    mount(viewer(IdentityKind.LOCAL_TOKEN));
    expect(screen.queryByTestId("linear-mcp-hint")).not.toBeInTheDocument();
    expect(listMcpServers).not.toHaveBeenCalled();
  });

  it("goes away when dismissed, and stays away in this browser", async () => {
    listMcpServers.mockResolvedValue({ servers: [] });
    const first = mount();
    await userEvent.click(await screen.findByTestId("linear-mcp-hint-dismiss"));
    expect(screen.queryByTestId("linear-mcp-hint")).not.toBeInTheDocument();
    expect(localStorage.getItem(LINEAR_MCP_HINT_KEY)).toBe("1");
    first.unmount();

    listMcpServers.mockClear();
    mount();
    expect(screen.queryByTestId("linear-mcp-hint")).not.toBeInTheDocument();
    expect(listMcpServers).not.toHaveBeenCalled();
  });
});
