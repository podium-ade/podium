import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ToastHost } from "../Toast";
import { SkillsPanel } from "./SkillsPanel";

const getProfile = vi.fn();
const createSkill = vi.fn();
const updateSkill = vi.fn();
const deleteSkill = vi.fn();
const listSecrets = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      getProfile: (...a: unknown[]) => getProfile(...a),
      createSkill: (...a: unknown[]) => createSkill(...a),
      updateSkill: (...a: unknown[]) => updateSkill(...a),
      deleteSkill: (...a: unknown[]) => deleteSkill(...a),
    },
    secrets: { listSecrets: (...a: unknown[]) => listSecrets(...a) },
  };
});

/** A skill as GetProfile reports it, with the fields a row reads. */
function skill(over: Record<string, unknown> = {}) {
  return {
    name: "general",
    image: "podium-agent-runtime:dev",
    systemPrompt: "# The general skill\n\nAnswer the question in the thread.",
    allowedTools: ["Read", "Grep"],
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
    // Both halves are editable: saving a file skill rewrites its YAML. Only a shadowed
    // stored skill is not, and that test sets it explicitly.
    editable: true,
    shadowed: false,
    updatedBy: "",
    ...over,
  };
}

const baseProfile = {
  name: "podium",
  displayName: "Podium",
  model: "claude-opus-5",
  defaultSkill: "general",
  chatDefaultSkill: "",
  profileDir: "/etc/podium/agent",
  fileDisplayName: "Podium",
  fileModel: "claude-opus-5",
  fileDefaultSkill: "general",
  fileChatDefaultSkill: "",
  overridden: [],
  updatedBy: "",
};

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter>
          <SkillsPanel />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("SkillsPanel", () => {
  beforeEach(() => {
    getProfile.mockReset();
    createSkill.mockReset();
    updateSkill.mockReset();
    deleteSkill.mockReset();
    listSecrets.mockReset();
    listSecrets.mockResolvedValue({
      secrets: [{ name: "podium.agent.github_token", version: 1 }],
    });
    getProfile.mockResolvedValue({
      profile: baseProfile,
      skills: [skill()],
      staleReason: "",
    });
  });

  it("leads with the image, because that is the unit of capability", async () => {
    mount();
    expect(await screen.findByTestId("skill-image")).toHaveTextContent(
      "podium-agent-runtime:dev",
    );
    expect(screen.getByText("/general")).toBeInTheDocument();
    // The heading of the prompt says less than the line under it.
    expect(screen.getByText("Answer the question in the thread.")).toBeInTheDocument();
  });

  // A file skill is editable now: saving one rewrites its YAML. The row says where it
  // lives rather than that it is locked, because it no longer is.
  it("says where a file-defined skill lives, and opens it for editing", async () => {
    mount();
    await screen.findByTestId("skill-row");
    expect(screen.getByText("skills/general.yaml")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit general" })).toBeInTheDocument();
    // Delete is still only inside the editor, next to the definition it destroys.
    expect(screen.queryByRole("button", { name: "Delete general" })).toBeNull();
  });

  it("deletes a stored skill from the editor, and offers no delete in the list", async () => {
    getProfile.mockResolvedValue({
      profile: baseProfile,
      skills: [skill({ name: "reporter", origin: "stored", editable: true })],
      staleReason: "",
    });
    deleteSkill.mockResolvedValue({});
    mount();
    // The list edits. Deleting is a decision taken with the definition on the screen.
    expect(await screen.findByRole("button", { name: "Edit reporter" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete reporter" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Edit reporter" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete reporter" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm deleting reporter" }));
    await waitFor(() => expect(deleteSkill).toHaveBeenCalledWith({ name: "reporter" }));
    // A delete closes the editor; the list is what comes back.
    await waitFor(() => expect(screen.queryByTestId("skill-editor")).toBeNull());
  });

  it("keeps the operator in the editor and shows why a delete was refused", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    getProfile.mockResolvedValue({
      profile: baseProfile,
      skills: [skill({ name: "reporter", origin: "stored", editable: true })],
      staleReason: "",
    });
    deleteSkill.mockRejectedValue(
      new ConnectError('default_skill "reporter" names no skill', Code.FailedPrecondition),
    );
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Edit reporter" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete reporter" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm deleting reporter" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("names no skill");
    expect(screen.getByTestId("skill-editor")).toBeInTheDocument();
  });

  it("says a shadowed skill never runs, and deletes it through the editor", async () => {
    getProfile.mockResolvedValue({
      profile: baseProfile,
      skills: [
        skill(),
        skill({ name: "general", origin: "stored", editable: true, shadowed: true }),
      ],
      staleReason: "",
    });
    deleteSkill.mockResolvedValue({});
    mount();
    const row = await screen.findByTestId("skill-shadowed");
    expect(row).toHaveTextContent("never runs");
    expect(row).toHaveTextContent("the file wins");

    await userEvent.click(screen.getByRole("button", { name: "Review general" }));
    // A shadowed skill cannot be written over, so the editor offers no save at all.
    expect(screen.queryByRole("button", { name: "Save skill" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Delete general" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm deleting general" }));
    await waitFor(() => expect(deleteSkill).toHaveBeenCalledWith({ name: "general" }));
  });

  it("creates a skill through the RPC with the secret it names", async () => {
    createSkill.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByTestId("skill-new"));

    await userEvent.type(screen.getByLabelText("Skill name"), "reporter");
    await userEvent.type(screen.getByLabelText("Image"), "ghcr.io/example/reporter:v1");
    await userEvent.type(screen.getByLabelText("System prompt"), "Write the report.");
    await userEvent.type(screen.getByLabelText("Allowed tools"), "Read");
    await userEvent.click(screen.getByRole("button", { name: "Add a secret" }));
    await userEvent.type(
      screen.getByLabelText("Secret name 1"),
      "podium.agent.github_token",
    );
    await userEvent.type(screen.getByLabelText("Secret key 1"), "GITHUB_TOKEN");
    await userEvent.click(screen.getByRole("button", { name: "Create skill" }));

    await waitFor(() => expect(createSkill).toHaveBeenCalledTimes(1));
    expect(createSkill.mock.calls[0][0].skill).toMatchObject({
      name: "reporter",
      image: "ghcr.io/example/reporter:v1",
      allowedTools: ["Read"],
      secrets: [
        { name: "podium.agent.github_token", target: "env", key: "GITHUB_TOKEN" },
      ],
    });
  });

  it("keeps the operator in the form and shows why the server refused", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    createSkill.mockRejectedValue(
      new ConnectError('skill "reporter": image is required', Code.InvalidArgument),
    );
    mount();
    await userEvent.click(await screen.findByTestId("skill-new"));
    await userEvent.type(screen.getByLabelText("Skill name"), "reporter");
    await userEvent.type(screen.getByLabelText("Image"), "x");
    await userEvent.type(screen.getByLabelText("System prompt"), "hi");
    await userEvent.type(screen.getByLabelText("Allowed tools"), "Read");
    await userEvent.click(screen.getByRole("button", { name: "Create skill" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("image is required");
    expect(screen.getByTestId("skill-editor")).toBeInTheDocument();
  });

  it("warns when the conductor is running an older profile than the database holds", async () => {
    getProfile.mockResolvedValue({
      profile: baseProfile,
      skills: [skill()],
      staleReason: "agent profile: default_skill \"gone\" names no skill",
    });
    mount();
    expect(await screen.findByText(/running an older profile/)).toBeInTheDocument();
  });

  it("says a change needs no restart, and that a file skill still does", async () => {
    mount();
    await screen.findByTestId("skill-row");
    expect(screen.getByText(/next turn, with no restart/)).toBeInTheDocument();
    expect(screen.getByText(/still needs a restart/)).toBeInTheDocument();
  });
});
