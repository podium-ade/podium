import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ToastHost } from "../Toast";
import { PlaybooksPanel } from "./PlaybooksPanel";

const getProfile = vi.fn();
const listSecrets = vi.fn();
const listSkills = vi.fn();
const reloadProfileDir = vi.fn();
const createPlaybook = vi.fn();
const updatePlaybook = vi.fn();
const deletePlaybook = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      getProfile: (...a: unknown[]) => getProfile(...a),
      listSkills: (...a: unknown[]) => listSkills(...a),
      listMcpServers: () => Promise.resolve({ servers: [] }),
      listAgents: () => Promise.resolve({ agents: [], defaultAgent: "claude" }),
      reloadProfileDir: (...a: unknown[]) => reloadProfileDir(...a),
      createPlaybook: (...a: unknown[]) => createPlaybook(...a),
      updatePlaybook: (...a: unknown[]) => updatePlaybook(...a),
      deletePlaybook: (...a: unknown[]) => deletePlaybook(...a),
    },
    secrets: { listSecrets: (...a: unknown[]) => listSecrets(...a) },
  };
});

function playbook(over: Record<string, unknown> = {}) {
  return {
    name: "reporter",
    image: "podium-agent-runtime:dev",
    systemPrompt: "# The reporter\n\nWrite the weekly report.",
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
    editable: true,
    ...over,
  };
}

const baseProfile = {
  name: "podium",
  displayName: "Podium",
  model: "claude-opus-5",
  defaultPlaybook: "",
  profileDir: "/etc/podium/agent",
  fileDisplayName: "Podium",
  fileModel: "claude-opus-5",
  fileDefaultPlaybook: "",
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
    listSecrets.mockReset();
    createPlaybook.mockReset();
    updatePlaybook.mockReset();
    deletePlaybook.mockReset();
    listSkills.mockResolvedValue({ skills: [], skillsDir: "" });
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
    expect(screen.getByText("/reporter")).toBeInTheDocument();
    expect(screen.getByText("Write the weekly report.")).toBeInTheDocument();
  });

  it("uses the same empty state as the other screens", async () => {
    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [],
      staleReason: "",
    });
    mount();
    expect(await screen.findByText("No playbooks")).toBeInTheDocument();
    expect(screen.getByText("Nothing for the assistant to start yet.")).toBeInTheDocument();
    expect(screen.getByTestId("playbook-new-empty")).toBeInTheDocument();
    expect(screen.queryByText("Select a playbook, or create one.")).toBeNull();
  });

  it("offers New playbook and opens the editor", async () => {
    mount();
    expect(await screen.findByTestId("playbook-new")).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("playbook-new"));
    expect(screen.getByTestId("playbook-editor")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create playbook" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "YAML" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Form" })).toBeInTheDocument();
  });

  it("opens an existing playbook for edit, not view-only", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit reporter" }));
    expect(screen.getByTestId("playbook-editor")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save playbook" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create playbook" })).toBeNull();
  });

  it("deletes the open playbook after confirm, without saving", async () => {
    deletePlaybook.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit reporter" }));
    await userEvent.click(screen.getByTestId("playbook-delete"));
    expect(deletePlaybook).not.toHaveBeenCalled();
    expect(updatePlaybook).not.toHaveBeenCalled();

    await userEvent.click(screen.getByTestId("playbook-delete-confirm"));
    expect(deletePlaybook).toHaveBeenCalledTimes(1);
    expect(deletePlaybook).toHaveBeenCalledWith({ name: "reporter" });
    expect(updatePlaybook).not.toHaveBeenCalled();
  });

  it("warns when the conductor is running an older profile than the overrides would produce", async () => {
    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [playbook()],
      staleReason: "agent profile: default_playbook \"gone\" names no playbook",
    });
    mount();
    expect(await screen.findByText(/running an older profile/)).toBeInTheDocument();
  });

  it("re-reads the profile directory and shows what the files now hold", async () => {
    reloadProfileDir.mockResolvedValue({});
    mount();
    await screen.findByTestId("playbook-row");
    expect(screen.queryByText("/analyst")).toBeNull();

    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [playbook(), playbook({ name: "analyst" })],
      staleReason: "",
    });
    await userEvent.click(screen.getByTestId("reload-profile-dir"));

    expect(reloadProfileDir).toHaveBeenCalled();
    expect(await screen.findByText("/analyst")).toBeInTheDocument();
  });
});
