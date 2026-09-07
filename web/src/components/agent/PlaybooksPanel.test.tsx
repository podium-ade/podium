import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ToastHost } from "../Toast";
import { PlaybooksPanel } from "./PlaybooksPanel";

const getProfile = vi.fn();
const createPlaybook = vi.fn();
const updatePlaybook = vi.fn();
const deletePlaybook = vi.fn();
const listSecrets = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      getProfile: (...a: unknown[]) => getProfile(...a),
      createPlaybook: (...a: unknown[]) => createPlaybook(...a),
      updatePlaybook: (...a: unknown[]) => updatePlaybook(...a),
      deletePlaybook: (...a: unknown[]) => deletePlaybook(...a),
    },
    secrets: { listSecrets: (...a: unknown[]) => listSecrets(...a) },
  };
});

/** A playbook as GetProfile reports it, with the fields a row reads. */
function playbook(over: Record<string, unknown> = {}) {
  return {
    name: "general",
    image: "podium-agent-runtime:dev",
    systemPrompt: "# The general playbook\n\nAnswer the question in the thread.",
    allowedTools: ["read", "grep"],
    maxTurns: 50,
    timeout: "30m",
    model: "",
    labels: [],
    secrets: [],
    repos: [],
    slackChannels: [],
    linear: false,
    env: {},
    origin: "file",
    editable: false,
    shadowed: false,
    updatedBy: "",
    ...over,
  };
}

const baseProfile = {
  name: "podium",
  displayName: "Podium",
  model: "claude-opus-5",
  defaultPlaybook: "general",
  chatDefaultPlaybook: "",
  profileDir: "/etc/podium/agent",
  fileDisplayName: "Podium",
  fileModel: "claude-opus-5",
  fileDefaultPlaybook: "general",
  fileChatDefaultPlaybook: "",
  overridden: [],
  updatedBy: "",
};

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter>
          <PlaybooksPanel />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("PlaybooksPanel", () => {
  beforeEach(() => {
    getProfile.mockReset();
    createPlaybook.mockReset();
    updatePlaybook.mockReset();
    deletePlaybook.mockReset();
    listSecrets.mockReset();
    listSecrets.mockResolvedValue({
      secrets: [{ name: "podium.agent.github_token", version: 1 }],
    });
    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [playbook()],
      staleReason: "",
    });
  });

  it("leads with the image, because that is the unit of capability", async () => {
    mount();
    expect(await screen.findByTestId("playbook-image")).toHaveTextContent(
      "podium-agent-runtime:dev",
    );
    expect(screen.getByText("/general")).toBeInTheDocument();
    // The heading of the prompt says less than the line under it.
    expect(screen.getByText("Answer the question in the thread.")).toBeInTheDocument();
  });

  it("renders a file-defined playbook read-only and says where it lives", async () => {
    mount();
    await screen.findByTestId("playbook-row");
    expect(screen.getByText("file · read-only")).toBeInTheDocument();
    expect(screen.getByText(/playbooks\/general\.yaml/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit general" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete general" })).toBeNull();
  });

  it("deletes a stored playbook from the editor, and offers no delete in the list", async () => {
    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [playbook({ name: "reporter", origin: "stored", editable: true })],
      staleReason: "",
    });
    deletePlaybook.mockResolvedValue({});
    mount();
    // The list edits. Deleting is a decision taken with the definition on the screen.
    expect(await screen.findByRole("button", { name: "Edit reporter" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete reporter" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Edit reporter" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete reporter" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm deleting reporter" }));
    await waitFor(() => expect(deletePlaybook).toHaveBeenCalledWith({ name: "reporter" }));
    // A delete closes the editor; the list is what comes back.
    await waitFor(() => expect(screen.queryByTestId("playbook-editor")).toBeNull());
  });

  it("keeps the operator in the editor and shows why a delete was refused", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [playbook({ name: "reporter", origin: "stored", editable: true })],
      staleReason: "",
    });
    deletePlaybook.mockRejectedValue(
      new ConnectError('default_playbook "reporter" names no playbook', Code.FailedPrecondition),
    );
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit reporter" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete reporter" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm deleting reporter" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("names no playbook");
    expect(screen.getByTestId("playbook-editor")).toBeInTheDocument();
  });

  it("says a shadowed playbook never runs, and deletes it through the editor", async () => {
    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [
        playbook(),
        playbook({ name: "general", origin: "stored", editable: true, shadowed: true }),
      ],
      staleReason: "",
    });
    deletePlaybook.mockResolvedValue({});
    mount();
    const row = await screen.findByTestId("playbook-shadowed");
    expect(row).toHaveTextContent("never runs");
    expect(row).toHaveTextContent("the file wins");

    await userEvent.click(screen.getByRole("button", { name: "Review general" }));
    // A shadowed playbook cannot be written over, so the editor offers no save at all.
    expect(screen.queryByRole("button", { name: "Save playbook" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Delete general" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm deleting general" }));
    await waitFor(() => expect(deletePlaybook).toHaveBeenCalledWith({ name: "general" }));
  });

  it("creates a playbook through the RPC with the secret it names", async () => {
    createPlaybook.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByTestId("playbook-new"));

    await userEvent.type(screen.getByLabelText("Playbook name"), "reporter");
    await userEvent.type(screen.getByLabelText("Image"), "ghcr.io/example/reporter:v1");
    await userEvent.type(screen.getByLabelText("System prompt"), "Write the report.");
    await userEvent.type(screen.getByLabelText("Allowed tools"), "read");
    await userEvent.click(screen.getByRole("button", { name: "Add a secret" }));
    await userEvent.type(
      screen.getByLabelText("Secret name 1"),
      "podium.agent.github_token",
    );
    await userEvent.type(screen.getByLabelText("Secret key 1"), "GITHUB_TOKEN");
    await userEvent.click(screen.getByRole("button", { name: "Create playbook" }));

    await waitFor(() => expect(createPlaybook).toHaveBeenCalledTimes(1));
    expect(createPlaybook.mock.calls[0][0].playbook).toMatchObject({
      name: "reporter",
      image: "ghcr.io/example/reporter:v1",
      allowedTools: ["read"],
      secrets: [
        { name: "podium.agent.github_token", target: "env", key: "GITHUB_TOKEN" },
      ],
    });
  });

  it("keeps the operator in the form and shows why the server refused", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    createPlaybook.mockRejectedValue(
      new ConnectError('playbook "reporter": image is required', Code.InvalidArgument),
    );
    mount();
    await userEvent.click(await screen.findByTestId("playbook-new"));
    await userEvent.type(screen.getByLabelText("Playbook name"), "reporter");
    await userEvent.type(screen.getByLabelText("Image"), "x");
    await userEvent.type(screen.getByLabelText("System prompt"), "hi");
    await userEvent.type(screen.getByLabelText("Allowed tools"), "read");
    await userEvent.click(screen.getByRole("button", { name: "Create playbook" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("image is required");
    expect(screen.getByTestId("playbook-editor")).toBeInTheDocument();
  });

  it("warns when the conductor is running an older profile than the database holds", async () => {
    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [playbook()],
      staleReason: "agent profile: default_playbook \"gone\" names no playbook",
    });
    mount();
    expect(await screen.findByText(/running an older profile/)).toBeInTheDocument();
  });

  it("says a change needs no restart, and that a file playbook still does", async () => {
    mount();
    await screen.findByTestId("playbook-row");
    expect(screen.getByText(/next turn, with no restart/)).toBeInTheDocument();
    expect(screen.getByText(/still needs a restart/)).toBeInTheDocument();
  });
});
