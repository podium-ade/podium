import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../lib/identity";
import { RoleAdmin, RoleMember, RoleOwner } from "../lib/rbac";
import { catalogue } from "../test/agents";
import { AgentPage } from "./AgentPage";
import { ToastHost } from "../components/Toast";

const KEY = "sk-ant-not-a-real-key-abcd";

const getSettings = vi.fn();
const setProviderKey = vi.fn();
const clearProviderKey = vi.fn();
const listSessions = vi.fn();
const listTurns = vi.fn();
const listChats = vi.fn();
const listPlaybooks = vi.fn();
const streamChat = vi.fn();
const getProfile = vi.fn();
const listAgents = vi.fn();
const startProviderOAuth = vi.fn();
const pollProviderOAuth = vi.fn();
const updateProfile = vi.fn();
const listSecrets = vi.fn();
const listSkills = vi.fn();
const listPersonalities = vi.fn();
const createPersonality = vi.fn();
const updatePersonality = vi.fn();
const deletePersonality = vi.fn();
const getProfileFile = vi.fn();
const updateProfileFile = vi.fn();
const listUsers = vi.fn();
const getConnections = vi.fn();

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
      listChats: (...a: unknown[]) => listChats(...a),
      listMemories: () => Promise.resolve({ items: [], nextCursor: "" }),
      listPlaybooks: (...a: unknown[]) => listPlaybooks(...a),
      streamChat: (...a: unknown[]) => streamChat(...a),
      getProfile: (...a: unknown[]) => getProfile(...a),
      listAgents: (...a: unknown[]) => listAgents(...a),
      startProviderOAuth: (...a: unknown[]) => startProviderOAuth(...a),
      pollProviderOAuth: (...a: unknown[]) => pollProviderOAuth(...a),
      updateProfile: (...a: unknown[]) => updateProfile(...a),
      listSkills: (...a: unknown[]) => listSkills(...a),
      listPersonalities: (...a: unknown[]) => listPersonalities(...a),
      createPersonality: (...a: unknown[]) => createPersonality(...a),
      updatePersonality: (...a: unknown[]) => updatePersonality(...a),
      deletePersonality: (...a: unknown[]) => deletePersonality(...a),
      listMcpServers: () => Promise.resolve({ servers: [] }),
      reloadProfileDir: () => Promise.resolve({}),
      getProfileFile: (...a: unknown[]) => getProfileFile(...a),
      updateProfileFile: (...a: unknown[]) => updateProfileFile(...a),
      getConnections: (...a: unknown[]) => getConnections(...a),
      getGitHubAccount: () => Promise.resolve({ account: { available: true, connected: false } }),
      setSlackConnection: () => Promise.resolve({}),
      clearSlackConnection: () => Promise.resolve({}),
      setGitHubConnection: () => Promise.resolve({}),
      clearGitHubConnection: () => Promise.resolve({}),
    },
    secrets: { listSecrets: (...a: unknown[]) => listSecrets(...a) },
    users: { listUsers: (...a: unknown[]) => listUsers(...a), setUserRole: vi.fn() },
  };
});

/** The profile the tab tests read: one playbook, nothing overridden. */
const profileResponse = {
  profile: {
    name: "podium",
    displayName: "Podium",
    model: "claude-opus-5",
    defaultPlaybook: "general",
    profileDir: "/etc/podium/agent",
    fileDisplayName: "Podium",
    fileModel: "claude-opus-5",
    fileDefaultPlaybook: "general",
    systemPrompt: "you are Podium",
    fileSystemPrompt: "you are Podium",
    agent: "claude",
    fileAgent: "claude",
    timeout: "15m0s",
    overridden: [] as string[],
    updatedBy: "",
  },
  playbooks: [
    {
      name: "general",
      image: "podium-agent-runtime:dev",
      systemPrompt: "Answer the question in the thread.",
      allowedTools: ["read"],
      maxTurns: 50,
      timeout: "30m",
      model: "",
      labels: [],
      secrets: [],
      repos: [],
      slackChannels: [],
      linear: false,
      env: {},
      updatedBy: "",
    },
  ],
  staleReason: "",
};

const viewer: Viewer = {
  login: "dev",
  displayName: "",
  kind: IdentityKind.LOCAL_TOKEN,
  agentEnabled: true,
  serverVersion: "dev",
  roles: [],
  claimed: false,
  hostedDomain: "",
  canClaim: false,
  googleAuthEnabled: false,
  claimDomain: "",
  pictureUrl: "",
};

function sectionLabels(): string[] {
  return within(screen.getByRole("navigation", { name: "Assistant" }))
    .getAllByRole("link")
    .map((link) => (link.textContent ?? "").replace(/, unsaved$/, ""));
}

function mount(path = "/agent/settings/models", who: Viewer | undefined = viewer) {
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

const anthropicNotSet = { provider: "anthropic", keySet: false, model: "claude-opus-5" };
const xaiNotSet = { provider: "xai", keySet: false, model: "claude-opus-5" };
const openaiNotSet = { provider: "openai", keySet: false, model: "claude-opus-5" };
const anthropicConnected = {
  provider: "anthropic",
  keySet: true,
  keyHint: "abcd",
  model: "claude-opus-5",
  setBy: "dev",
  authKind: "api_key",
};

// The settings screen reads `providers`; `provider` stays the Anthropic row for the clients
// that only ever knew about one.
const notSet = { provider: anthropicNotSet, providers: [anthropicNotSet, xaiNotSet, openaiNotSet] };
const connected = {
  provider: anthropicConnected,
  providers: [anthropicConnected, xaiNotSet, openaiNotSet],
};

describe("AgentPage", () => {
  beforeEach(() => {
    getSettings.mockReset();
    setProviderKey.mockReset();
    clearProviderKey.mockReset();
    listSessions.mockReset();
    listTurns.mockReset();
    listChats.mockReset();
    listPlaybooks.mockReset();
    streamChat.mockReset();
    getProfile.mockReset();
    listAgents.mockReset();
    startProviderOAuth.mockReset();
    pollProviderOAuth.mockReset();
    updateProfile.mockReset();
    listSecrets.mockReset();
    getProfileFile.mockReset();
    updateProfileFile.mockReset();
    getConnections.mockReset();
    getConnections.mockResolvedValue({
      slack: { configured: false, source: "" },
      github: { configured: false, source: "", appId: "", webhookListen: "" },
      linear: { available: false, configured: false },
    });
    getProfileFile.mockResolvedValue({
      content: "name: podium\n",
      path: "/etc/podium/agent/profile.yaml",
    });
    listUsers.mockReset();
    listUsers.mockResolvedValue({ users: [] });
    getProfile.mockResolvedValue(profileResponse);
    listSecrets.mockResolvedValue({ secrets: [] });
    getSettings.mockResolvedValue(notSet);
    listAgents.mockResolvedValue({ agents: catalogue(), defaultAgent: "claude" });
    listSessions.mockResolvedValue({ sessions: [], nextCursor: "" });
    listChats.mockResolvedValue({ chats: [], nextCursor: "" });
    listPlaybooks.mockResolvedValue({ playbooks: [], profileDisplayName: "Podium" });
    listSkills.mockResolvedValue({ skills: [], skillsDir: "", maxBytes: 131072n, maxFiles: 64 });
    listPersonalities.mockReset();
    createPersonality.mockReset();
    updatePersonality.mockReset();
    deletePersonality.mockReset();
    listPersonalities.mockResolvedValue({ personalities: [] });
  });

  it("says plainly that there is no conductor when the server has none", () => {
    mount("/agent/chat", { ...viewer, agentEnabled: false });
    expect(
      screen.getByText("The conductor is not configured on this control plane"),
    ).toBeInTheDocument();
    expect(screen.getByText(/PODIUM_AGENT_URL/)).toBeInTheDocument();
    expect(getSettings).not.toHaveBeenCalled();
  });

  it("offers Google sign-in on Settings and does not ask for a local token", async () => {
    mount("/agent/settings/account", { ...viewer, googleAuthEnabled: true });
    expect(await screen.findByTestId("identity-card")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Account" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Users" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Backend" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Connections" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Models" })).toBeInTheDocument();
    expect(screen.queryByTestId("provider-card-anthropic")).toBeNull();
    expect(screen.getByRole("link", { name: "Sign in with Google Workspace" })).toHaveAttribute(
      "href",
      "/auth/google/start",
    );
    expect(screen.queryByLabelText("Dev token")).toBeNull();
  });

  it("redirects /agent to the chat screen", async () => {
    mount("/agent");
    expect(await screen.findByTestId("chat-new")).toBeInTheDocument();
  });

  it("opens Memory on its own bar, not the chat screen", async () => {
    mount("/agent/memory");
    expect(await screen.findByRole("heading", { name: "Memory" })).toBeInTheDocument();
    expect(await screen.findByTestId("memory-search")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-new")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Chat" })).toBeNull();
  });

  it("renders each assistant screen on its own route, without a tab row", async () => {
    mount("/agent/sessions");
    expect(await screen.findByText("No sessions yet.")).toBeInTheDocument();
    // Chat, Sessions, Memory and Assistant are in the app sidebar, not this page.
    expect(screen.queryByRole("link", { name: "Chat" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Sessions" })).toBeNull();
  });

  it("shows the assistant and the playbooks on their own routes", async () => {
    mount("/agent/profile");
    expect(await screen.findByTestId("profile-card")).toBeInTheDocument();
    expect(screen.getByLabelText("Display name")).toHaveValue("Podium");
    expect(screen.getByRole("link", { name: "Identity" })).toHaveAttribute("aria-current", "page");
    expect(sectionLabels()).toEqual(["Identity", "Instructions", "Model", "Reach", "Limits"]);
    const assistants = screen.getByRole("navigation", { name: "Assistants" });
    expect(within(assistants).getByRole("link", { name: /Podium/ })).toHaveAttribute("aria-current", "page");
    expect(within(assistants).getByText("Built-in")).toBeInTheDocument();
    expect(within(assistants).getByRole("link", { name: "New" })).toHaveAttribute("href", "/agent/assistants/new");
    expect(screen.queryByLabelText("Name", { selector: "#assistant-name" })).toBeNull();
    expect(screen.getByLabelText("Name", { selector: "#assistant-git-name" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Source" })).toBeNull();
    expect(screen.queryByText(/profile\.yaml/)).toBeNull();
  });

  it("saves the whole assistant when the display name changes", async () => {
    updateProfile.mockResolvedValue({});
    mount("/agent/profile");
    const box = await screen.findByLabelText("Display name");
    await userEvent.clear(box);
    await userEvent.type(box, "Night");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(updateProfile).toHaveBeenCalled());
    expect(updateProfile).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "podium",
        displayName: "Night",
        systemPrompt: "you are Podium",
        agent: "claude",
        model: "claude-opus-5",
        timeout: "15m0s",
        skills: [],
        mcpServers: [],
        maxTurns: 0,
      }),
    );
  });

  it("opens instructions from the address and refuses an unknown section", async () => {
    const first = mount("/agent/profile/instructions");
    expect(await screen.findByLabelText("System prompt")).toHaveValue("you are Podium");
    expect(screen.getByRole("link", { name: "Instructions" })).toHaveAttribute("aria-current", "page");
    first.unmount();

    mount("/agent/profile/nope");
    expect(await screen.findByLabelText("Display name")).toHaveValue("Podium");
  });

  it("lets a member read Podium and an admin save it", async () => {
    const human: Viewer = {
      ...viewer,
      login: "bob@acme.com",
      kind: IdentityKind.USER,
      roles: [RoleMember],
      claimed: true,
      googleAuthEnabled: true,
      hostedDomain: "acme.com",
    };
    const member = mount("/agent/profile", human);
    expect(await screen.findByLabelText("Display name")).toBeDisabled();
    expect(screen.getByText("Owners set this assistant.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    expect(screen.queryByTestId("chat-new")).toBeNull();
    expect(getProfile).toHaveBeenCalled();
    member.unmount();

    getProfile.mockClear();
    mount("/agent/profile", { ...human, roles: [RoleAdmin] });
    expect(await screen.findByTestId("profile-card")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeInTheDocument();
    expect(screen.queryByText("Owners set this assistant.")).toBeNull();
    expect(getProfile).toHaveBeenCalled();
  });

  it("creates a personal assistant and leaves it selected", async () => {
    const created = {
      id: "pers_01",
      name: "night-owl",
      displayName: "Night Owl",
      instructions: "Speak briefly.",
      agent: "grok",
      model: "grok-4.6",
      effort: "",
    };
    createPersonality.mockImplementation(async () => {
      listPersonalities.mockResolvedValue({ personalities: [created] });
      return { personality: created };
    });
    const user = userEvent.setup();
    mount("/agent/assistants/new");
    expect(await screen.findByText(/voice on top of Podium/)).toBeInTheDocument();
    expect(sectionLabels()).toEqual(["Identity", "Instructions", "Model"]);
    expect(screen.queryByLabelText("Name", { selector: "#personality-name" })).toBeNull();
    expect(screen.queryByLabelText("Name", { selector: "#assistant-git-name" })).toBeNull();
    expect(screen.getByRole("link", { name: "Model" })).toHaveAttribute("href", "/agent/assistants/new/model");
    await user.type(screen.getByLabelText("Display name"), "Night Owl");
    await user.click(screen.getByRole("link", { name: "Model" }));
    expect(await screen.findByText(/leave it following Podium/)).toBeInTheDocument();
    const picker = await screen.findByRole("button", { name: "Assistant: agent and model" });
    expect(picker).toBeEnabled();
    expect(picker).toHaveTextContent("Podium's model");
    await user.click(picker);
    await user.click(await screen.findByText("grok-4.6"));
    const assistants = screen.getByRole("navigation", { name: "Assistants" });
    expect(within(assistants).getByRole("link", { name: "New" })).toHaveAttribute("aria-current", "page");
    await user.click(screen.getByRole("link", { name: "Instructions" }));
    await user.type(screen.getByLabelText("Instructions"), "Speak briefly.");
    const save = screen.getByRole("button", { name: "Save" });
    await waitFor(() => expect(save).toBeEnabled());
    await user.click(save);
    await waitFor(() =>
      expect(createPersonality).toHaveBeenCalledWith({
        name: "",
        displayName: "Night Owl",
        instructions: "Speak briefly.",
        agent: "grok",
        model: "grok-4.6",
        effort: "",
      }),
    );
    expect(await screen.findByRole("link", { name: "Talk to it" })).toHaveAttribute(
      "href",
      "/agent/chat/a/pers_01",
    );
    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Assistants" })).getByRole("link", { name: "Night Owl" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("sends an unknown personal section back to Podium", async () => {
    mount("/agent/assistants/new/reach");
    expect(await screen.findByLabelText("Display name")).toHaveValue("Podium");
  });

  it("sends an old source address back to the assistant", async () => {
    mount("/agent/profile/source");
    expect(await screen.findByLabelText("Display name")).toHaveValue("Podium");
    expect(screen.queryByLabelText("profile.yaml")).toBeNull();
  });

  it("renders playbooks without the talk tabs", async () => {
    mount("/agent/playbooks");
    // The image is the headline of a playbook row: it is the unit of capability.
    expect(await screen.findByTestId("playbook-image")).toHaveTextContent(
      "podium-agent-runtime:dev",
    );
    expect(screen.queryByRole("link", { name: "Chat" })).toBeNull();
    expect(screen.queryByRole("navigation", { name: "Agent" })).toBeNull();
  });

  it("shows self-hosted as the backend and the others as coming soon", async () => {
    mount("/agent/settings/backend", { ...viewer, agentEnabled: false, googleAuthEnabled: false });
    expect(await screen.findByRole("link", { name: "Backend" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.queryByRole("link", { name: "Models" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Connections" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Account" })).toBeNull();
    expect(screen.queryByText("Nothing to configure")).toBeNull();

    const backends = screen.getByRole("radiogroup", { name: "Backend" });
    expect(within(backends).getByRole("radio", { name: /Self-hosted/ })).toBeChecked();
    expect(within(backends).getByRole("radio", { name: /Modal/ })).toBeDisabled();
    expect(within(backends).getByRole("radio", { name: /Daytona/ })).toBeDisabled();
    expect(within(backends).getAllByText("Coming soon")).toHaveLength(2);
  });

  it("opens connections beside the other settings sections", async () => {
    mount("/agent/settings/connections");
    expect(await screen.findByRole("heading", { name: "GitHub" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Connections" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("heading", { name: "Linear" })).toBeInTheDocument();
    expect(screen.getByText("Coming soon")).toBeInTheDocument();
  });

  it("hides connections from a member and keeps it for an admin and an owner", async () => {
    const human = {
      ...viewer,
      login: "bob@acme.com",
      kind: IdentityKind.USER,
      roles: [RoleMember],
      claimed: true,
      googleAuthEnabled: true,
      hostedDomain: "acme.com",
    };
    mount("/agent/settings/models", human);
    expect(await screen.findByRole("link", { name: "Models" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Connections" })).toBeNull();
    expect(getConnections).not.toHaveBeenCalled();

    cleanup();
    mount("/agent/settings/connections", human);
    expect(await screen.findByRole("link", { name: "Account" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.queryByRole("heading", { name: "GitHub" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Connections" })).toBeNull();
    expect(getConnections).not.toHaveBeenCalled();
    // A member connects their own account here, which is not the App's configuration.
    expect(await screen.findByRole("button", { name: /connect github/i })).toBeInTheDocument();

    cleanup();
    mount("/agent/settings/connections", { ...human, login: "cara@acme.com", roles: [RoleAdmin] });
    expect(await screen.findByRole("heading", { name: "GitHub" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Connections" })).toHaveAttribute("aria-current", "page");

    cleanup();
    mount("/agent/settings/models", { ...human, login: "alice@acme.com", roles: [RoleOwner] });
    expect(await screen.findByRole("link", { name: "Connections" })).toBeInTheDocument();
  });

  it("renders settings models without the talk tabs", async () => {
    mount("/agent/settings/models");
    expect(await screen.findByRole("heading", { name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Users" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Models" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByText("Anthropic")).toBeInTheDocument();
    expect(screen.getByText("OpenAI")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Chat" })).toBeNull();
    // Status lives on each card. A summary row of "Claude · not set" chips above the list
    // is gone: it repeated the cards and read as a second, empty catalogue.
    expect(screen.queryByText(/Claude · not set/i)).toBeNull();
    expect(screen.queryByText(/Grok · not set/i)).toBeNull();
    expect(screen.queryByText(/GPT[- ]not set/i)).toBeNull();
  });

  it("renders users as a settings category", async () => {
    listUsers.mockResolvedValue({
      users: [
        {
          login: "alice@acme.com",
          displayName: "Alice",
          roles: ["owner"],
          hostedDomain: "acme.com",
          pictureUrl: "",
        },
      ],
    });
    mount("/agent/settings/users");
    expect(await screen.findByRole("link", { name: "Users" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(await screen.findByText("Alice")).toBeInTheDocument();
    expect(screen.getByText("Permission")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Chat" })).toBeNull();
  });

  it("opens a deep link to one chat without an in-page tab row", async () => {
    // /agent/chat/<id> is a real route. The sidebar lights Chat; this page does not.
    mount("/agent/chat/chat_01abc");
    expect(await screen.findByTestId("chat-new")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Chat" })).toBeNull();
  });

  it("saves a key through the RPC and shows it as set afterwards", async () => {
    setProviderKey.mockResolvedValue({
      provider: anthropicConnected,
      models: ["claude-opus-5"],
      status: "",
    });
    mount();
    await within(await screen.findByTestId("provider-card-anthropic")).findByText("Not set");
    getSettings.mockResolvedValue(connected);

    await userEvent.click(
      within(screen.getByTestId("provider-card-anthropic")).getByRole("button", { name: "Connect" }),
    );
    await userEvent.type(screen.getByTestId("provider-key-input-anthropic"), KEY);
    await userEvent.click(screen.getByTestId("provider-key-save-anthropic"));

    await waitFor(() =>
      expect(setProviderKey).toHaveBeenCalledWith({ provider: "anthropic", key: KEY }),
    );
    expect(
      await within(screen.getByTestId("provider-card-anthropic")).findByText("Connected"),
    ).toBeInTheDocument();
  });

  it("removes a key and returns to the empty state", async () => {
    getSettings.mockResolvedValue(connected);
    clearProviderKey.mockResolvedValue({});
    mount();
    await within(await screen.findByTestId("provider-card-anthropic")).findByText("Connected");
    getSettings.mockResolvedValue(notSet);

    await userEvent.click(screen.getByTestId("provider-key-remove-anthropic"));
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() =>
      expect(clearProviderKey).toHaveBeenCalledWith({ provider: "anthropic" }),
    );
    expect(clearProviderKey).toHaveBeenCalledTimes(1);
    expect(
      await within(screen.getByTestId("provider-card-anthropic")).findByText("Not set"),
    ).toBeInTheDocument();
    expect(screen.getByText(/encrypted at rest by podium-server/i)).toBeInTheDocument();
  });

  it("keeps the card and warns when the conductor itself is down", async () => {
    getSettings.mockRejectedValue(
      new ConnectError("podium-agent is not reachable", Code.Unavailable),
    );
    mount();
    expect(await screen.findByText(/podium-agent is not reachable/)).toBeInTheDocument();
    expect(screen.getByTestId("provider-card-anthropic")).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("button", { name: "Connect" })[0]);
    expect(screen.getByTestId("provider-key-input-anthropic")).toBeInTheDocument();
  });

  it("reports any other failure to read the settings as a failure", async () => {
    getSettings.mockRejectedValue(new ConnectError("boom", Code.Internal));
    mount();
    expect(await screen.findByText("Could not read the agent settings")).toBeInTheDocument();
    expect(screen.queryByTestId("provider-key-input-anthropic")).toBeNull();
  });
});
