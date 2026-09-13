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
    ...over,
  };
}

const baseProfile = {
  name: "podium",
  displayName: "Podium",
  model: "claude-opus-5",
  defaultPlaybook: "general",
  profileDir: "/etc/podium/agent",
  fileDisplayName: "Podium",
  fileModel: "claude-opus-5",
  fileDefaultPlaybook: "general",
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
    expect(screen.getByText("/general")).toBeInTheDocument();
    expect(screen.getByText("Answer the question in the thread.")).toBeInTheDocument();
  });

  it("renders a playbook as a file and says where it lives", async () => {
    mount();
    await screen.findByTestId("playbook-row");
    expect(screen.getByText("file")).toBeInTheDocument();
    expect(screen.getByText(/playbooks\/general\.yaml/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit general" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete general" })).toBeNull();
    expect(screen.queryByTestId("playbook-new")).toBeNull();
  });

  it("opens a playbook read-only", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "View general" }));
    expect(screen.getByTestId("playbook-editor")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save playbook" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Create playbook" })).toBeNull();
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

  it("says playbooks are files and points at the re-read", async () => {
    mount();
    await screen.findByTestId("playbook-row");
    expect(screen.getByText(/Playbooks are files on the conductor's host/)).toBeInTheDocument();
    expect(screen.getByTestId("reload-profile-dir")).toBeInTheDocument();
  });

  it("re-reads the profile directory and shows what the files now hold", async () => {
    reloadProfileDir.mockResolvedValue({});
    mount();
    await screen.findByTestId("playbook-row");
    expect(screen.queryByText("/reporter")).toBeNull();

    getProfile.mockResolvedValue({
      profile: baseProfile,
      playbooks: [playbook(), playbook({ name: "reporter" })],
      staleReason: "",
    });
    await userEvent.click(screen.getByTestId("reload-profile-dir"));

    expect(reloadProfileDir).toHaveBeenCalled();
    expect(await screen.findByText("/reporter")).toBeInTheDocument();
  });

  it("reports a profile directory that does not load and keeps the current one", async () => {
    reloadProfileDir.mockRejectedValue(new Error("playbooks/broken.yaml: line 1: bad YAML"));
    mount();
    await screen.findByTestId("playbook-row");

    await userEvent.click(screen.getByTestId("reload-profile-dir"));
    expect(await screen.findByText(/playbooks\/broken.yaml/)).toBeInTheDocument();
    expect(screen.getByTestId("playbook-row")).toBeInTheDocument();
  });
});
