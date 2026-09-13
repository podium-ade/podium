import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { Code, ConnectError } from "@connectrpc/connect";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ToastHost } from "../Toast";
import { McpPanel } from "./McpPanel";

const listMcpServers = vi.fn();
const createMcpServer = vi.fn();
const updateMcpServer = vi.fn();
const deleteMcpServer = vi.fn();
const setMcpServerToken = vi.fn();
const clearMcpServerToken = vi.fn();
const startMcpOAuth = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listMcpServers: (...a: unknown[]) => listMcpServers(...a),
      createMcpServer: (...a: unknown[]) => createMcpServer(...a),
      updateMcpServer: (...a: unknown[]) => updateMcpServer(...a),
      deleteMcpServer: (...a: unknown[]) => deleteMcpServer(...a),
      setMcpServerToken: (...a: unknown[]) => setMcpServerToken(...a),
      clearMcpServerToken: (...a: unknown[]) => clearMcpServerToken(...a),
      startMcpOAuth: (...a: unknown[]) => startMcpOAuth(...a),
    },
  };
});

function server(over: Record<string, unknown> = {}) {
  return {
    name: "linear",
    url: "https://mcp.linear.app/mcp",
    description: "Issues and projects.",
    enabled: true,
    tokenSet: true,
    tokenHint: "9xQ2",
    tokenSetBy: "alice",
    tokenSetAt: undefined,
    playbooks: ["coder"],
    createdBy: "alice",
    updatedBy: "alice",
    updatedAt: undefined,
    tokenEnv: "PODIUM_MCP_LINEAR_TOKEN",
    tokenSecret: "podium.agent.mcp.linear_token",
    authKind: "token",
    account: "",
    expiresAt: undefined,
    refreshable: false,
    oauthSupported: false,
    ...over,
  };
}

// jsdom refuses a real navigation, and the sign-in ends in one. The origin is stubbed with
// it, because that is what the browser sends as its own callback URL.
const assign = vi.fn();
Object.defineProperty(window, "location", {
  value: { origin: "https://podium.example.ts.net", assign },
  configurable: true,
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter>
          <McpPanel />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("McpPanel", () => {
  beforeEach(() => {
    for (const m of [
      listMcpServers,
      createMcpServer,
      updateMcpServer,
      deleteMcpServer,
      setMcpServerToken,
      clearMcpServerToken,
      startMcpOAuth,
    ]) {
      m.mockReset();
    }
    assign.mockReset();
    sessionStorage.clear();
    listMcpServers.mockResolvedValue({ servers: [], maxPerPlaybook: 8 });
  });

  it("offers a way in before anything is registered, without a warning banner", async () => {
    mount();
    expect(await screen.findByText("No MCP servers")).toBeInTheDocument();
    expect(screen.getByTestId("mcp-new")).toBeInTheDocument();
    expect(
      screen.queryByText(/token is spent by every turn of every playbook that names it/),
    ).toBeNull();
  });

  it("lists a server with its address, the four characters of its token, and who names it", async () => {
    listMcpServers.mockResolvedValue({ servers: [server()], maxPerPlaybook: 8 });
    mount();
    const row = await screen.findByTestId("mcp-row");
    expect(within(row).getByText("linear")).toBeInTheDocument();
    expect(within(row).getByText("https://mcp.linear.app/mcp")).toBeInTheDocument();
    expect(within(row).getByText(/9xQ2/)).toBeInTheDocument();
    expect(within(row).getByText("/coder")).toBeInTheDocument();
  });

  it("keeps a long description to one ellipsized line on the card, and the full text in edit", async () => {
    const description =
      "Issues, projects and cycles, plus a great deal more that would wrap the card if it were allowed to.";
    listMcpServers.mockResolvedValue({
      servers: [server({ description })],
      maxPerPlaybook: 8,
    });
    mount();
    const row = await screen.findByTestId("mcp-row");
    const clipped = within(row).getByText(description);
    expect(clipped).toHaveClass("truncate");
    expect(clipped).toHaveAttribute("title", description);

    await userEvent.click(within(row).getByTestId("mcp-edit"));
    expect(screen.getByLabelText("Description")).toHaveValue(description);
  });

  // A registration nothing names is the state right after adding one, and the row is where
  // somebody finds out they are not done.
  it("says so when no playbook names a server", async () => {
    listMcpServers.mockResolvedValue({ servers: [server({ playbooks: [] })], maxPerPlaybook: 8 });
    mount();
    expect(await screen.findByText("no playbook names it")).toBeInTheDocument();
  });

  it("registers a server from a preset, with the token in the same request", async () => {
    createMcpServer.mockResolvedValue({ server: server() });
    mount();
    await userEvent.click(await screen.findByTestId("mcp-new"));
    await userEvent.click(screen.getAllByTestId("mcp-preset")[0]);
    expect(screen.getByPlaceholderText("lin_api_…")).toBeInTheDocument();
    expect(screen.getByText(/starts with lin_api_/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Token"), "lin_api_secret");
    await userEvent.click(screen.getByTestId("mcp-save"));

    await waitFor(() => expect(createMcpServer).toHaveBeenCalled());
    const [req] = createMcpServer.mock.calls[0] as [
      { server: { name: string; url: string }; token: string },
    ];
    expect(req.server.name).toBe("linear");
    expect(req.server.url).toBe("https://mcp.linear.app/mcp");
    expect(req.token).toBe("lin_api_secret");
  });

  it("refuses a name that is already registered without asking the server", async () => {
    listMcpServers.mockResolvedValue({ servers: [server()], maxPerPlaybook: 8 });
    mount();
    await userEvent.click(await screen.findByTestId("mcp-new"));
    await userEvent.click(screen.getByTestId("mcp-preset-custom"));
    await userEvent.type(screen.getByLabelText("Name"), "linear");
    await userEvent.type(screen.getByLabelText("URL"), "https://mcp.linear.app/mcp");
    expect(screen.getByText(/already registered/)).toBeInTheDocument();
    expect(screen.getByTestId("mcp-save")).toBeDisabled();
    expect(createMcpServer).not.toHaveBeenCalled();
  });

  it("offers Custom as its own option, with a generic token hint", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("mcp-new"));
    expect(screen.getByText("Custom")).toBeInTheDocument();
    expect(screen.queryByLabelText("Start from")).toBeNull();

    await userEvent.click(screen.getByTestId("mcp-preset-custom"));
    expect(screen.getByRole("heading", { name: "Add a custom server" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back" })).toBeInTheDocument();
    expect(screen.queryByText(/Choose a different server/)).toBeNull();
    expect(screen.getByLabelText("Name")).toHaveValue("");
    expect(screen.getByLabelText("URL")).toHaveValue("");
    expect(screen.getByLabelText("Token")).toHaveAttribute("placeholder", "");
    expect(screen.getByText(/Leave it empty for a server that needs no credential/)).toBeInTheDocument();
  });

  it("goes back to the product picker from a chosen server", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("mcp-new"));
    await userEvent.click(screen.getAllByTestId("mcp-preset")[0]);
    expect(screen.getByRole("heading", { name: "Add Linear" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByText("Slack")).toBeInTheDocument();
    expect(screen.getByText("Stripe")).toBeInTheDocument();
    expect(screen.getByText("Figma")).toBeInTheDocument();
    expect(screen.queryByLabelText("Name")).toBeNull();
  });

  // The switch sends back the registration with one field flipped, and nothing the conductor
  // owns: the token metadata and the provenance are not this screen's to claim.
  it("disables a server by updating the definition alone", async () => {
    listMcpServers.mockResolvedValue({ servers: [server()], maxPerPlaybook: 8 });
    updateMcpServer.mockResolvedValue({ server: server({ enabled: false }) });
    mount();
    await userEvent.click(await screen.findByLabelText("linear is enabled"));

    await waitFor(() => expect(updateMcpServer).toHaveBeenCalled());
    const [req] = updateMcpServer.mock.calls[0] as [{ server: Record<string, unknown> }];
    expect(req.server).toEqual({
      name: "linear",
      url: "https://mcp.linear.app/mcp",
      description: "Issues and projects.",
      enabled: false,
    });
  });

  it("stores a token on its own, and names the secret and the variable it lands in", async () => {
    listMcpServers.mockResolvedValue({ servers: [server({ tokenSet: false, tokenHint: "" })], maxPerPlaybook: 8 });
    setMcpServerToken.mockResolvedValue({ server: server() });
    mount();
    await userEvent.click(await screen.findByTestId("mcp-token"));
    expect(screen.getByText("podium.agent.mcp.linear_token")).toBeInTheDocument();
    expect(screen.getByText("PODIUM_MCP_LINEAR_TOKEN")).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Token"), "lin_api_secret");
    await userEvent.click(screen.getByTestId("mcp-token-save"));
    await waitFor(() =>
      expect(setMcpServerToken).toHaveBeenCalledWith({ name: "linear", token: "lin_api_secret" }),
    );
  });

  // The browser sends its OWN callback URL, because it is the only party that knows the
  // address this control plane is reached at. That is what lets the conductor register a
  // client dynamically against an install nobody configured in advance.
  it("starts a sign-in with this install's own callback and navigates to the authorize URL", async () => {
    listMcpServers.mockResolvedValue({
      servers: [server({ tokenSet: false, authKind: "", tokenHint: "" })],
      maxPerPlaybook: 8,
    });
    startMcpOAuth.mockResolvedValue({
      flowId: "flow-1",
      authorizeUrl: "https://auth.linear.app/authorize?client_id=abc&state=st-1",
      state: "st-1",
      issuer: "https://auth.linear.app",
      scope: "read write",
    });
    mount();
    await userEvent.click(await screen.findByTestId("mcp-signin"));

    await waitFor(() =>
      expect(startMcpOAuth).toHaveBeenCalledWith({
        name: "linear",
        redirectUri: "https://podium.example.ts.net/agent/mcp/callback",
      }),
    );
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://auth.linear.app/authorize?client_id=abc&state=st-1",
      ),
    );
    // The flow id and the state are parked for the trip away from this origin. Neither is a
    // credential: the verifier that would make them usable never left the conductor.
    const pending = JSON.parse(sessionStorage.getItem("podium.mcp.oauth.pending") ?? "{}");
    expect(pending).toMatchObject({ flowId: "flow-1", state: "st-1", name: "linear" });
  });

  // A server that advertises no OAuth is not a broken server, and the refusal belongs beside
  // the button that was pressed rather than in a toast over a list.
  it("shows the refusal on the row when a server cannot be signed in to", async () => {
    listMcpServers.mockResolvedValue({ servers: [server()], maxPerPlaybook: 8 });
    startMcpOAuth.mockRejectedValue(
      new ConnectError("this MCP server does not advertise an OAuth authorization server", Code.Unimplemented),
    );
    mount();
    await userEvent.click(await screen.findByTestId("mcp-signin"));

    expect(
      await screen.findByText(/does not advertise an OAuth authorization server/),
    ).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
  });

  // An access token has no four characters worth showing, so a sign-in reads as a sign-in.
  it("shows a signed-in server by its account, not by a hint", async () => {
    listMcpServers.mockResolvedValue({
      servers: [
        server({ authKind: "oauth", tokenHint: "", account: "alice@example.test", refreshable: true }),
      ],
      maxPerPlaybook: 8,
    });
    mount();
    const row = await screen.findByTestId("mcp-row");
    expect(within(row).getByText(/signed in · alice@example.test/)).toBeInTheDocument();
    expect(within(row).getByText("signed in by alice")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: /Sign in again/ })).toBeInTheDocument();
  });

  // Without a refresh token the sign-in dies at expiry and a human has to do it again. Worth
  // saying before a turn is the thing that finds out.
  it("warns when a sign-in cannot be refreshed", async () => {
    listMcpServers.mockResolvedValue({
      servers: [server({ authKind: "oauth", tokenHint: "", refreshable: false })],
      maxPerPlaybook: 8,
    });
    mount();
    expect(await screen.findByText("expires, not refreshable")).toBeInTheDocument();
  });

  // Deleting takes the token with it, so the playbooks that would break are named before the
  // button rather than after it.
  it("warns which playbooks a delete would break, and only then deletes", async () => {
    listMcpServers.mockResolvedValue({ servers: [server()], maxPerPlaybook: 8 });
    deleteMcpServer.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByTestId("mcp-delete"));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/\/coder/)).toBeInTheDocument();
    expect(within(dialog).getByText(/will fail until the name is taken out of it/)).toBeInTheDocument();
    expect(deleteMcpServer).not.toHaveBeenCalled();

    await userEvent.click(screen.getByTestId("mcp-delete-confirm"));
    await waitFor(() => expect(deleteMcpServer).toHaveBeenCalledWith({ name: "linear" }));
  });
});
