import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { ToastHost } from "../Toast";
import { SkillsPanel } from "./SkillsPanel";

const listSkills = vi.fn();
const uploadSkill = vi.fn();
const setSkillEnabled = vi.fn();
const deleteSkill = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listSkills: (...a: unknown[]) => listSkills(...a),
      uploadSkill: (...a: unknown[]) => uploadSkill(...a),
      setSkillEnabled: (...a: unknown[]) => setSkillEnabled(...a),
      deleteSkill: (...a: unknown[]) => deleteSkill(...a),
    },
  };
});

function skill(over: Record<string, unknown> = {}) {
  return {
    name: "pr-review",
    description: "Use when reviewing a diff.",
    sha256: "a313660c2de79175aa0b3c5f0dd2e8b1c4f6a7d8e9b0c1d2e3f4a5b6c7d8e9f0",
    sizeBytes: 2048n,
    fileCount: 2,
    enabled: true,
    origin: "stored",
    editable: true,
    shadowed: false,
    uploadedBy: "alice",
    uploadedAt: undefined,
    problem: "",
    playbooks: ["coder"],
    ...over,
  };
}

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
    listSkills.mockReset();
    uploadSkill.mockReset();
    setSkillEnabled.mockReset();
    deleteSkill.mockReset();
    listSkills.mockResolvedValue({
      skills: [],
      skillsDir: "",
      maxBytes: 131072n,
      maxFiles: 64,
      maxPerPlaybook: 8,
    });
  });

  it("says what a skill is and where it runs, before anything is uploaded", async () => {
    mount();
    expect(await screen.findByText("No skills")).toBeInTheDocument();
    // The one thing this screen has to carry: a skill is somebody else's code, in the turn's
    // container, with the turn's credentials.
    expect(
      screen.getByText(/runs in the turn's container with the turn's credentials/),
    ).toBeInTheDocument();
  });

  it("lists a skill with its size, digest and the playbooks that name it", async () => {
    listSkills.mockResolvedValue({
      skills: [skill()],
      skillsDir: "",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    mount();

    const row = await screen.findByTestId("skill-row");
    expect(within(row).getByText("pr-review")).toBeInTheDocument();
    expect(within(row).getByText("Use when reviewing a diff.")).toBeInTheDocument();
    expect(within(row).getByText("2.0 KB")).toBeInTheDocument();
    expect(within(row).getByText("2 files")).toBeInTheDocument();
    expect(within(row).getByText("a313660c2de7…")).toBeInTheDocument();
    expect(within(row).getByText("/coder")).toBeInTheDocument();
    expect(within(row).getByText("by alice")).toBeInTheDocument();
  });

  it("uploads a pasted SKILL.md as bytes", async () => {
    uploadSkill.mockResolvedValue({ skill: skill({ name: "release-notes" }), replaced: false });
    mount();
    await screen.findByText("No skills");

    await userEvent.click(screen.getByTestId("skill-new"));
    const body = "---\nname: release-notes\ndescription: Use when writing notes.\n---\n";
    await userEvent.type(screen.getByLabelText("Or paste a SKILL.md"), body);
    await userEvent.click(screen.getByRole("button", { name: "Add skill" }));

    await waitFor(() => expect(uploadSkill).toHaveBeenCalledTimes(1));
    const sent = uploadSkill.mock.calls[0][0] as {
      content: Uint8Array;
      filename: string;
      replace: boolean;
    };
    expect(new TextDecoder().decode(sent.content)).toContain("name: release-notes");
    expect(sent.replace).toBe(false);
    expect(await screen.findByRole("status")).toHaveTextContent("release-notes added");
  });

  it("shows the conductor's refusal verbatim and stays in the dialog", async () => {
    uploadSkill.mockRejectedValue(
      new ConnectError(
        `skill "pr-review" is 70000 bytes once encoded for delivery; the limit is 65536`,
        Code.InvalidArgument,
      ),
    );
    mount();
    await screen.findByText("No skills");

    await userEvent.click(screen.getByTestId("skill-new"));
    await userEvent.type(screen.getByLabelText("Or paste a SKILL.md"), "---\nname: pr-review\n---\n");
    await userEvent.click(screen.getByRole("button", { name: "Add skill" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("the limit is 65536");
    // Still open, so the paste is not lost.
    expect(screen.getByLabelText("Or paste a SKILL.md")).toBeInTheDocument();
  });

  it("turns a skill off through the RPC", async () => {
    listSkills.mockResolvedValue({ skills: [skill()], skillsDir: "", maxBytes: 131072n, maxFiles: 64 });
    setSkillEnabled.mockResolvedValue({ skill: skill({ enabled: false }) });
    mount();

    await userEvent.click(await screen.findByLabelText("pr-review is enabled"));
    await waitFor(() =>
      expect(setSkillEnabled).toHaveBeenCalledWith({ name: "pr-review", enabled: false }),
    );
  });

  // Deleting a skill a playbook names is allowed, and the dialog says which playbooks break.
  it("names the playbooks a delete would break before deleting", async () => {
    listSkills.mockResolvedValue({ skills: [skill()], skillsDir: "", maxBytes: 131072n, maxFiles: 64 });
    deleteSkill.mockResolvedValue({});
    mount();

    await userEvent.click(await screen.findByTestId("skill-delete"));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/\/coder/)).toBeInTheDocument();
    expect(
      within(dialog).getByText(/will fail until the name is taken out of it/),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByTestId("skill-delete-confirm"));
    await waitFor(() => expect(deleteSkill).toHaveBeenCalledWith({ name: "pr-review" }));
  });

  // A directory on the conductor's host is a file, not a row: nothing here writes one.
  it("offers no delete and no toggle for a skill from the host directory", async () => {
    listSkills.mockResolvedValue({
      skills: [skill({ origin: "dir", editable: false, sha256: "", uploadedBy: "" })],
      skillsDir: "/etc/podium/agent/skills",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    mount();

    const row = await screen.findByTestId("skill-row");
    expect(within(row).getByText("host directory")).toBeInTheDocument();
    expect(within(row).queryByTestId("skill-delete")).toBeNull();
    expect(within(row).queryByLabelText("pr-review is enabled")).toBeNull();
    expect(screen.getAllByText(/\/etc\/podium\/agent\/skills/).length).toBeGreaterThan(0);
  });

  // A stored skill the host directory has since claimed never runs. It is reported so it can
  // be deleted, which is the only way to make the list honest.
  it("separates a shadowed upload from what actually runs", async () => {
    listSkills.mockResolvedValue({
      skills: [
        skill({ origin: "dir", editable: false, sha256: "", uploadedBy: "" }),
        skill({ shadowed: true }),
      ],
      skillsDir: "/etc/podium/agent/skills",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    mount();

    await screen.findByText("Shadowed");
    const rows = screen.getAllByTestId("skill-row");
    expect(rows).toHaveLength(2);
    expect(within(rows[1]).getByText("shadowed")).toBeInTheDocument();
    // Shadowed rows are deletable and nothing else.
    expect(within(rows[1]).getByTestId("skill-delete")).toBeInTheDocument();
    expect(within(rows[1]).queryByLabelText("pr-review is enabled")).toBeNull();
  });

  it("reports a directory that will not load rather than hiding it", async () => {
    listSkills.mockResolvedValue({
      skills: [
        skill({
          origin: "dir",
          editable: false,
          sha256: "",
          uploadedBy: "",
          description: "",
          problem: "skill \"pr-review\": /etc/podium/agent/skills/pr-review has no SKILL.md",
        }),
      ],
      skillsDir: "/etc/podium/agent/skills",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    mount();
    expect(await screen.findByText(/has no SKILL.md/)).toBeInTheDocument();
  });

  it("says the conductor is unreachable without hiding the screen", async () => {
    listSkills.mockRejectedValue(
      new ConnectError("podium-agent is not reachable", Code.Unavailable),
    );
    mount();
    expect(await screen.findByText(/The skills could not be read/)).toBeInTheDocument();
    expect(screen.getByTestId("skill-new")).toBeInTheDocument();
  });
});
