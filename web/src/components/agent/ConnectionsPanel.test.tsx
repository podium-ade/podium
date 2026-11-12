import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ConnectionsPanel } from "./ConnectionsPanel";
import { ToastHost } from "../Toast";

const getConnections = vi.fn();
const setSlackConnection = vi.fn();
const clearSlackConnection = vi.fn();
const setGitHubConnection = vi.fn();
const clearGitHubConnection = vi.fn();
const setGitHubWebhookFilters = vi.fn();
const setLinearConnection = vi.fn();
const clearLinearConnection = vi.fn();

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
      setGitHubWebhookFilters: (...a: unknown[]) => setGitHubWebhookFilters(...a),
      setLinearConnection: (...a: unknown[]) => setLinearConnection(...a),
      clearLinearConnection: (...a: unknown[]) => clearLinearConnection(...a),
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
  linear: {
    available: true,
    configured: false,
    source: "",
    clientId: "",
    clientSecretSet: false,
    webhookSecretSet: false,
    webhookSecretHint: "",
    webhookListen: "",
    appUserId: "",
    appUserName: "",
    organizationName: "",
    organizationUrlKey: "",
    setBy: "",
    restartRequired: false,
    polling: false,
  },
};

const savedLinear = {
  ...empty.linear,
  configured: true,
  source: "saved",
  clientId: "client-1",
  clientSecretSet: true,
  webhookSecretSet: true,
  webhookSecretHint: "8765",
  webhookListen: "0.0.0.0:8091",
  appUserId: "app-user-1",
  appUserName: "Podium",
  organizationName: "Acme",
  organizationUrlKey: "acme",
  setBy: "ada",
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
    setGitHubWebhookFilters.mockReset();
    setLinearConnection.mockReset();
    clearLinearConnection.mockReset();
    getConnections.mockResolvedValue(empty);
  });

  it("shows GitHub, Slack and Linear forms", async () => {
    mount();
    expect(await screen.findByText("GitHub")).toBeInTheDocument();
    expect(screen.getByText("Slack")).toBeInTheDocument();
    expect(screen.getByText("Linear")).toBeInTheDocument();
    expect(screen.queryByText("Coming soon")).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove saved Slack" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Edit GitHub" }));
    expect(screen.getByLabelText("App ID")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    await userEvent.click(screen.getByRole("button", { name: "Edit Slack" }));
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
    await userEvent.click(await screen.findByRole("button", { name: "Edit Slack" }));
    expect(screen.getByText(/Ending in 1234/)).toBeInTheDocument();
    expect(screen.getByText(/Restart the conductor/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove saved Slack" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Replace bot token" }));
    await userEvent.type(screen.getByLabelText("Bot token"), "xoxb-new");
    await userEvent.click(screen.getByRole("button", { name: "Save Slack" }));
    expect(setSlackConnection).toHaveBeenCalledWith({ appToken: "", botToken: "xoxb-new" });
  });

  it("says when Linear is still polling with the environment's key", async () => {
    getConnections.mockResolvedValue({ ...empty, linear: { ...empty.linear, polling: true } });
    mount();
    expect(await screen.findByText(/Polling with the environment's API key/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit Linear" }));
    expect(screen.getByText(/Polling is on/)).toBeInTheDocument();
    expect(screen.getByText(/Saving an app here turns polling off/)).toBeInTheDocument();
  });

  it("says in plain words when Linear revoked the app", async () => {
    const revokedAt = timestampFromDate(new Date(Date.now() - 3_600_000));
    getConnections.mockResolvedValue({ ...empty, linear: { ...savedLinear, revokedAt } });
    mount();
    expect(await screen.findByText(/Linear revoked this app. Authorize it again in Linear/)).toBeInTheDocument();
    expect(screen.getByText("Revoked")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit Linear" }));
    expect(screen.getByText(/so nothing starts from Linear/)).toBeInTheDocument();
  });

  it("does not say the app is revoked when it is not", async () => {
    getConnections.mockResolvedValue({ ...empty, linear: savedLinear });
    mount();
    expect(await screen.findByText("Connected as Podium in Acme")).toBeInTheDocument();
    expect(screen.queryByText("Revoked")).toBeNull();
  });

  it("says what a delegation and a mention each run", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit Linear" }));
    expect(screen.getByText(/A delegated issue runs the\s+playbook marked linear: true/)).toBeInTheDocument();
    expect(screen.getByText(/A mention is answered by the assistant, or by that\s+playbook when this conductor has no host runtime/)).toBeInTheDocument();
  });

  it("saves a Linear app with setup steps beside it", async () => {
    setLinearConnection.mockResolvedValue({ linear: savedLinear });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit Linear" }));
    expect(screen.getByText(/Settings → API → OAuth applications → New/)).toBeInTheDocument();
    expect(screen.getByText(/Turn on Client credentials/)).toBeInTheDocument();
    expect(screen.getByText(/Agent session events and Permission changes/)).toBeInTheDocument();
    expect(screen.getByText(/Leave Inbox notifications off/)).toBeInTheDocument();
    expect(screen.getByText("/webhooks/linear")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove saved Linear" })).toBeNull();

    await userEvent.type(screen.getByLabelText("Client ID"), "client-1");
    await userEvent.type(screen.getByLabelText("Client secret"), "lsec");
    await userEvent.type(screen.getByLabelText("Webhook signing secret"), "lin_wh_x");
    await userEvent.type(screen.getByLabelText("Webhook listen"), "0.0.0.0:8091");
    await userEvent.click(screen.getByRole("button", { name: "Save Linear" }));
    expect(setLinearConnection).toHaveBeenCalledWith({
      clientId: "client-1",
      clientSecret: "lsec",
      webhookSecret: "lin_wh_x",
      webhookListen: "0.0.0.0:8091",
    });
    expect(await screen.findByText("Linear saved. It signs in as Podium in Acme.")).toBeInTheDocument();
  });

  it("shows who a saved Linear app is and keeps its secrets on a save", async () => {
    getConnections.mockResolvedValue({ ...empty, linear: savedLinear });
    setLinearConnection.mockResolvedValue({ linear: savedLinear });
    mount();
    expect(await screen.findByText("Connected as Podium in Acme")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit Linear" }));
    expect(screen.getByText(/saved by ada/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove saved Linear" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /^Webhooks/ }));
    // A saved secret is shown as saved, and none of it is shown.
    expect(screen.queryByText(/ending in/)).toBeNull();
    await userEvent.clear(screen.getByLabelText("Webhook listen"));
    await userEvent.type(screen.getByLabelText("Webhook listen"), "0.0.0.0:9000");
    await userEvent.click(screen.getByRole("button", { name: "Save Linear" }));
    expect(setLinearConnection).toHaveBeenCalledWith({
      clientId: "client-1",
      clientSecret: "",
      webhookSecret: "",
      webhookListen: "0.0.0.0:9000",
    });
  });

  it("asks for new secrets when the Linear client id changes", async () => {
    getConnections.mockResolvedValue({ ...empty, linear: savedLinear });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit Linear" }));
    await userEvent.click(screen.getByRole("button", { name: /^OAuth application/ }));
    await userEvent.clear(screen.getByLabelText("Client ID"));
    await userEvent.type(screen.getByLabelText("Client ID"), "client-2");
    expect(screen.getByText(/A new client ID is a new app. Paste its client secret/)).toBeInTheDocument();
    expect(screen.getByLabelText("Client secret")).toBeInTheDocument();
  });

  it("removes a saved Linear app", async () => {
    getConnections.mockResolvedValue({ ...empty, linear: savedLinear });
    clearLinearConnection.mockResolvedValue({ linear: empty.linear });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit Linear" }));
    await userEvent.click(screen.getByRole("button", { name: "Remove saved Linear" }));
    expect(clearLinearConnection).toHaveBeenCalledWith({});
  });

  it("saves the triggers on their own", async () => {
    getConnections.mockResolvedValue({
      ...empty,
      github: {
        ...empty.github,
        filters: {
          allowedActors: ["alice"],
          ignoredActors: [],
          ignoreBots: false,
          triggerPhrases: [],
          replyMode: "bot_threads",
          autoReview: true,
          conversationReplies: false,
          excludedBranches: ["dev"],
        },
      },
    });
    setGitHubWebhookFilters.mockResolvedValue({ github: empty.github });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit GitHub" }));
    expect(screen.getByText("1 allowed, @mention.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Triggers/ }));

    expect(screen.getByLabelText("Allowed people")).toHaveValue("alice");
    expect(screen.getByRole("radio", { name: /Threads the App is in/ })).toBeChecked();

    await userEvent.type(screen.getByLabelText("Allowed people"), "\nbob, carol");
    await userEvent.type(screen.getByLabelText("Trigger phrases"), "/review{enter}take a look");
    expect(screen.getByLabelText("Excluded branches")).toHaveValue("dev");
    await userEvent.type(screen.getByLabelText("Excluded branches"), "{enter}feature/*{enter}/^v{{1,2}$/");
    await userEvent.click(screen.getByRole("checkbox", { name: /Ignore bot accounts/ }));
    await userEvent.click(screen.getByRole("radio", { name: /Never/ }));
    await userEvent.click(screen.getByRole("checkbox", { name: /Review new pull requests/ }));
    await userEvent.click(screen.getByRole("checkbox", { name: /Answer a reply on the conversation tab/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save triggers" }));

    expect(setGitHubWebhookFilters).toHaveBeenCalledWith({
      filters: {
        allowedActors: ["alice", "bob", "carol"],
        ignoredActors: [],
        ignoreBots: true,
        triggerPhrases: ["/review", "take a look"],
        replyMode: "always",
        autoReview: false,
        conversationReplies: true,
        excludedBranches: ["dev", "feature/*", "/^v{1,2}$/"],
      },
    });
    expect(setGitHubConnection).not.toHaveBeenCalled();
  });
});
