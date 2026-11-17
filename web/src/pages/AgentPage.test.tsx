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
import { OrgSettingsPage } from "./OrgSettingsPage";
import { SandboxesPage } from "./SandboxesPage";
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
const getRunsOn = vi.fn();
const setRunsOn = vi.fn();
const listSandboxProviders = vi.fn();

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
      getRunsOn: (...a: unknown[]) => getRunsOn(...a),
      setRunsOn: (...a: unknown[]) => setRunsOn(...a),
      getGitHubAccount: () => Promise.resolve({ account: { available: true, connected: false } }),
      setSlackConnection: () => Promise.resolve({}),
      clearSlackConnection: () => Promise.resolve({}),
      setGitHubConnection: () => Promise.resolve({}),
      clearGitHubConnection: () => Promise.resolve({}),
    },
    secrets: { listSecrets: (...a: unknown[]) => listSecrets(...a) },
    users: { listUsers: (...a: unknown[]) => listUsers(...a), setUserRole: vi.fn() },
    sandboxProviders: { listSandboxProviders: (...a: unknown[]) => listSandboxProviders(...a) },
    organization: {
      getOrganization: () => Promise.resolve({ organization: { organizationName: "", deploymentName: "" } }),
      updateOrganization: vi.fn(),
    },
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
              <Route path="/org/*" element={<OrgSettingsPage />} />
              <Route path="/sandboxes/*" element={<SandboxesPage />} />
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
const googleNotSet = { provider: "google", keySet: false, model: "claude-opus-5" };
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
const notSet = { provider: anthropicNotSet, providers: [anthropicNotSet, xaiNotSet, openaiNotSet, googleNotSet] };
const connected = {
  provider: anthropicConnected,
  providers: [anthropicConnected, xaiNotSet, openaiNotSet, googleNotSet],
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
      linear: { available: true, configured: false, polling: false },
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

  it("offers Google sign-in on Your account and does not ask for a local token", async () => {
    mount("/agent/settings/account", { ...viewer, googleAuthEnabled: true });
    expect(await screen.findByTestId("identity-card")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Your account" })).toBeInTheDocument();
    // The former settings sections are sidebar pages now, not a tab row on this one.
    expect(screen.queryByRole("navigation", { name: "Settings sections" })).toBeNull();
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
    expect(screen.getByRole("heading", { name: "Podium" })).toBeInTheDocument();
    expect(screen.getByText("Built-in")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Assistants" })).toHaveAttribute("href", "/agent/assistants");
    expect(screen.queryByRole("navigation", { name: "Assistants" })).toBeNull();
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
        timeout: "15m",
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
    expect(screen.getByRole("heading", { name: "New assistant" })).toBeInTheDocument();
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
    expect(await screen.findByRole("heading", { name: "Night Owl" })).toBeInTheDocument();
  });

  it("lists Podium and your own assistants in a table, each opening its own page", async () => {
    listPersonalities.mockResolvedValue({
      personalities: [
        { id: "pers_01", name: "night-owl", displayName: "Night Owl", instructions: "Speak briefly.", agent: "grok", model: "grok-4.6", effort: "" },
      ],
    });
    const user = userEvent.setup();
    mount("/agent/assistants");
    const rows = await screen.findAllByTestId("assistant-row");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByRole("link", { name: "Podium" })).toHaveAttribute("href", "/agent/assistants/podium");
    expect(within(rows[0]).getByText("Built-in")).toBeInTheDocument();
    expect(within(rows[1]).getByText("Speak briefly.")).toBeInTheDocument();
    expect(within(rows[1]).getByText("grok-4.6")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New assistant" })).toHaveAttribute("href", "/agent/assistants/new");
    // A side-by-side editor is gone: the list carries no form.
    expect(screen.queryByLabelText("Display name")).toBeNull();

    await user.click(within(rows[1]).getByText("Speak briefly."));
    expect(await screen.findByRole("heading", { name: "Night Owl" })).toBeInTheDocument();
    expect(screen.getByLabelText("Display name")).toHaveValue("Night Owl");
  });

  it("offers Podium's row Talk but not Delete, and a personal row both", async () => {
    listPersonalities.mockResolvedValue({
      personalities: [{ id: "pers_01", name: "night-owl", displayName: "Night Owl", instructions: "", agent: "", model: "", effort: "" }],
    });
    const user = userEvent.setup();
    mount("/agent/assistants");
    await user.click(await screen.findByRole("button", { name: "Actions for Podium" }));
    expect(await screen.findByRole("menuitem", { name: "Talk to it" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Delete assistant" })).toBeNull();
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Actions for Night Owl" }));
    expect(await screen.findByRole("menuitem", { name: "Delete assistant" })).toBeInTheDocument();
  });

  it("invites a first assistant of your own when there is none", async () => {
    mount("/agent/assistants");
    expect(await screen.findByText("No assistants of your own yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add one" })).toHaveAttribute("href", "/agent/assistants/new");
  });

  it("sends an unknown personal section back to the list", async () => {
    mount("/agent/assistants/new/reach");
    expect(await screen.findAllByTestId("assistant-row")).toHaveLength(1);
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

  it("chooses where turns run among your nodes and the providers set up on Sandboxes", async () => {
    getRunsOn.mockResolvedValue({ runsOn: { runsOn: "nodes" } });
    setRunsOn.mockResolvedValue({ runsOn: { runsOn: "modal" } });
    listSandboxProviders.mockResolvedValue({
      providers: [
        { name: "boat", hasSecret: true, settings: {}, inUse: 1, maxConcurrent: 2, accountLimit: 0 },
        { name: "modal", hasSecret: true, settings: { runtime: "gvisor" }, inUse: 0, maxConcurrent: 4, accountLimit: 0 },
      ],
    });
    getProfile.mockResolvedValue({
      ...profileResponse,
      playbooks: [
        { ...profileResponse.playbooks[0], name: "coder", docker: true },
        { ...profileResponse.playbooks[0], name: "viewer", browser: true },
        { ...profileResponse.playbooks[0], name: "vmviewer", browser: true, sandbox: { modal: { runtime: "vm" } } },
        { ...profileResponse.playbooks[0], name: "pinned", docker: true, runsOn: "nodes" },
      ],
    });
    // Backend is the Default sandbox section of Sandboxes › Providers now, and its old
    // address lands there.
    mount("/agent/settings/runs-on");
    const select = await screen.findByRole("combobox", { name: "Default sandbox" });
    expect(select).toHaveTextContent("Your nodes");
    const meter = within(await screen.findByTestId("capacity-boat")).getByRole("meter");
    expect(meter).toHaveAttribute("aria-valuenow", "1");
    expect(meter).toHaveAttribute("aria-valuemax", "2");
    expect(screen.queryByText("These playbooks' sidecars cannot run on the default")).toBeNull();

    // Once Modal is the default, a playbook whose sidecars it cannot run is named.
    getRunsOn.mockResolvedValue({ runsOn: { runsOn: "modal" } });
    await userEvent.click(select);
    await userEvent.click(await screen.findByRole("option", { name: "Modal" }));
    await waitFor(() => expect(setRunsOn).toHaveBeenCalledWith({ runsOn: "modal" }));
    const alert = (await screen.findByText("These playbooks' sidecars cannot run on the default")).closest(
      "[data-slot=alert]",
    );
    expect(alert).toHaveTextContent("coder: Modal runs sidecars only under the vm runtime");
    expect(alert).toHaveTextContent("viewer: Modal runs sidecars only under the vm runtime");
    expect(alert).not.toHaveTextContent("vmviewer");
    expect(alert).not.toHaveTextContent("pinned");
    expect(within(screen.getByTestId("provider-modal")).getByText("Default")).toBeInTheDocument();
  });

  it("has no Backend page without a conductor", async () => {
    // With no conductor there are no turns to place, so Sandboxes opens on its providers.
    mount("/agent/settings/runs-on", { ...viewer, agentEnabled: false, googleAuthEnabled: false });
    expect(await screen.findByRole("region", { name: "Sandbox providers" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Default sandbox" })).toBeNull();
  });

  it("opens connections beside the other settings sections", async () => {
    mount("/agent/settings/connections");
    expect(await screen.findByText("GitHub")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Connections" })).toBeInTheDocument();
    expect(screen.getByText("Linear")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit Linear" })).toBeInTheDocument();
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
    expect(await screen.findByRole("heading", { name: "Models" })).toBeInTheDocument();
    expect(getConnections).not.toHaveBeenCalled();

    cleanup();
    mount("/agent/settings/connections", human);
    expect(await screen.findByRole("region", { name: "General" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "GitHub" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Connections" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Connections" })).toBeNull();
    expect(getConnections).not.toHaveBeenCalled();

    cleanup();
    // A member connects their own account on Your account, which is not the App's configuration.
    mount("/agent/settings/account", human);
    expect(await screen.findByRole("heading", { name: "Your account" })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: /connect github/i })).toBeInTheDocument();

    cleanup();
    mount("/agent/settings/connections", { ...human, login: "cara@acme.com", roles: [RoleAdmin] });
    expect(await screen.findByText("GitHub")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Connections" })).toBeInTheDocument();

    cleanup();
    mount("/agent/settings/connections", { ...human, login: "alice@acme.com", roles: [RoleOwner] });
    expect(await screen.findByRole("region", { name: "Connections" })).toBeInTheDocument();
  });

  it("renders settings models without the talk tabs", async () => {
    mount("/agent/settings/models");
    expect(await screen.findByRole("heading", { name: "Models" })).toBeInTheDocument();
    expect(screen.getByText("Anthropic")).toBeInTheDocument();
    expect(screen.getByText("OpenAI")).toBeInTheDocument();
    expect(screen.getByText("Google")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Chat" })).toBeNull();
    // Status lives on each card. A summary row of "Claude · not set" chips above the list
    // is gone: it repeated the cards and read as a second, empty catalogue.
    expect(screen.queryByText(/Claude · not set/i)).toBeNull();
    expect(screen.queryByText(/Grok · not set/i)).toBeNull();
    expect(screen.queryByText(/GPT[- ]not set/i)).toBeNull();
  });

  it("renders users as the Members page", async () => {
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
    expect(await screen.findByRole("region", { name: "Members" })).toBeInTheDocument();
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
      expect(setProviderKey).toHaveBeenCalledWith({ provider: "anthropic", key: KEY, global: true }),
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
      expect(clearProviderKey).toHaveBeenCalledWith({ provider: "anthropic", global: true }),
    );
    expect(clearProviderKey).toHaveBeenCalledTimes(1);
    expect(
      await within(screen.getByTestId("provider-card-anthropic")).findByText("Not set"),
    ).toBeInTheDocument();
  });

  it("gives a signed-in member their own keys and shows the global ones read-only", async () => {
    const member: Viewer = {
      ...viewer,
      login: "bob@acme.com",
      kind: IdentityKind.USER,
      roles: [RoleMember],
      claimed: true,
      googleAuthEnabled: true,
      hostedDomain: "acme.com",
    };
    getSettings.mockResolvedValue({
      ...connected,
      personal: [anthropicNotSet, xaiNotSet, openaiNotSet, googleNotSet],
    });
    setProviderKey.mockResolvedValue({
      provider: { ...anthropicConnected, global: false },
      models: ["claude-opus-5"],
      status: "",
    });
    mount("/agent/settings/models", member);

    // Yours is where a person lands, with the same Yours / Global tabs as Secrets and MCP.
    const yours = await screen.findByTestId("models-yours");
    expect(screen.getByRole("tab", { name: /Yours/ })).toHaveAttribute("aria-selected", "true");
    const card = within(yours).getByTestId("provider-card-anthropic");
    expect(await within(card).findByText("Not set")).toBeInTheDocument();
    // The company key is what their chats run on until they save their own.
    expect(within(card).getByTestId("provider-fallback-anthropic")).toHaveTextContent(
      "Using the global key",
    );

    await userEvent.click(within(card).getByRole("button", { name: "Connect" }));
    await userEvent.type(screen.getByTestId("provider-key-input-anthropic"), KEY);
    await userEvent.click(screen.getByTestId("provider-key-save-anthropic"));
    await waitFor(() =>
      expect(setProviderKey).toHaveBeenCalledWith({ provider: "anthropic", key: KEY, global: false }),
    );

    await userEvent.click(screen.getByRole("tab", { name: /Global/ }));
    const global = await screen.findByTestId("models-global");
    expect(screen.getByText(/Only an admin or owner can change them/)).toBeInTheDocument();
    const globalCard = within(global).getByTestId("provider-card-anthropic");
    expect(within(globalCard).getByText("Connected")).toBeInTheDocument();
    expect(within(globalCard).queryByRole("button", { name: "Rotate key" })).toBeNull();
    expect(within(globalCard).queryByTestId("provider-key-remove-anthropic")).toBeNull();
  });

  it("lets an admin sign a subscription in for everyone", async () => {
    const admin: Viewer = {
      ...viewer,
      login: "ada@acme.com",
      kind: IdentityKind.USER,
      roles: [RoleAdmin],
      claimed: true,
    };
    getSettings.mockResolvedValue({
      ...notSet,
      personal: [anthropicNotSet, xaiNotSet, openaiNotSet, googleNotSet],
    });
    startProviderOAuth.mockResolvedValue({
      flowId: "flow-1",
      userCode: "WDJB-MJHT",
      verificationUri: "https://accounts.x.ai/device",
      verificationUriComplete: "",
      interval: 5,
      codePaste: false,
    });
    pollProviderOAuth.mockResolvedValue({ state: "pending", interval: 5 });
    mount("/agent/settings/models", admin);

    await userEvent.click(await screen.findByRole("tab", { name: /Global/ }));
    const global = await screen.findByTestId("models-global");
    const card = within(global).getByTestId("provider-card-xai");
    await userEvent.click(await within(card).findByRole("button", { name: "Connect" }));
    await userEvent.click(screen.getByTestId("provider-mode-subscription-xai"));
    await userEvent.click(screen.getByTestId("provider-oauth-start-xai"));
    await waitFor(() =>
      expect(startProviderOAuth).toHaveBeenCalledWith({ provider: "xai", global: true }),
    );
  });

  it("keeps a global Claude subscription on a row of its own beside the key", async () => {
    const admin: Viewer = {
      ...viewer,
      login: "ada@acme.com",
      kind: IdentityKind.USER,
      roles: [RoleAdmin],
      claimed: true,
    };
    getSettings.mockResolvedValue({
      ...connected,
      personal: [anthropicNotSet, xaiNotSet, openaiNotSet, googleNotSet],
      claudeSubscription: {
        provider: "anthropic",
        keySet: true,
        authKind: "oauth",
        account: "team@acme.com",
        refreshable: true,
        global: true,
      },
    });
    clearProviderKey.mockResolvedValue({});
    mount("/agent/settings/models", admin);

    // A person with no Anthropic credential of their own chats on the subscription.
    const yours = await screen.findByTestId("models-yours");
    expect(
      await within(yours).findByTestId("provider-fallback-anthropic"),
    ).toHaveTextContent("Using the global subscription");

    await userEvent.click(screen.getByRole("tab", { name: /Global, 2 connected/ }));
    const global = await screen.findByTestId("models-global");
    const sub = within(global).getByTestId("provider-card-claude-subscription");
    expect(within(sub).getByText("Connected")).toBeInTheDocument();
    expect(within(sub).getByText(/team@acme\.com/)).toBeInTheDocument();
    // The key is still its own row, and the Anthropic key row offers no sign-in.
    const key = within(global).getByTestId("provider-card-anthropic");
    expect(within(key).getByText(/abcd/)).toBeInTheDocument();

    await userEvent.click(within(sub).getByRole("button", { name: "Sign out" }));
    expect(screen.getByText(/go back to the global API key/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(clearProviderKey).toHaveBeenCalledWith({
        provider: "anthropic",
        global: true,
        claudeSubscription: true,
      }),
    );
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
