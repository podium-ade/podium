import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { ToastHost } from "../Toast";
import { SkillPage, SkillsPanel } from "./SkillsPanel";

const listSkills = vi.fn();
const deleteSkill = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listSkills: (...a: unknown[]) => listSkills(...a),
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
    problem: "",
    playbooks: ["coder"],
    ...over,
  };
}

function mount(path = "/agent/skills") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/agent/skills" element={<SkillsPanel />} />
            <Route path="/agent/skills/new" element={<SkillPage />} />
            <Route path="/agent/skills/:name" element={<SkillPage />} />
          </Routes>
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("SkillsPanel", () => {
  beforeEach(() => {
    listSkills.mockReset();
    deleteSkill.mockReset();
    listSkills.mockResolvedValue({
      skills: [],
      skillsDir: "",
      maxBytes: 131072n,
      maxFiles: 64,
      maxPerPlaybook: 8,
    });
  });

  it("says what a skill is and where it runs, before any are installed", async () => {
    mount();
    expect(await screen.findByText("No skills")).toBeInTheDocument();
    expect(screen.getByText("Nothing for a turn to pick up yet.")).toBeInTheDocument();
    expect(await screen.findByTestId("skill-new")).toBeInTheDocument();
    expect(screen.queryByText(/PODIUM_AGENT_SKILLS_DIR/)).toBeNull();
    expect(screen.queryByText(/turn's credentials/)).toBeNull();
  });

  it("opens a markdown editor from New skill, not a zip picker", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("skill-new"));
    expect(await screen.findByTestId("skill-editor")).toBeInTheDocument();
    expect(screen.getByLabelText("SKILL.md")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Choose a file" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Choose a folder" })).toBeInTheDocument();
    expect(screen.queryByText(/zip/i)).toBeNull();
  });

  it("lists a skill in a table with its size and the playbooks that name it", async () => {
    listSkills.mockResolvedValue({
      skills: [skill()],
      skillsDir: "/etc/podium/skills",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    mount();

    const row = await screen.findByTestId("skill-row");
    expect(within(row).getByText("pr-review")).toBeInTheDocument();
    expect(within(row).getByText("Use when reviewing a diff.")).toBeInTheDocument();
    expect(within(row).getByText("2.0 KB")).toBeInTheDocument();
    expect(within(row).getByText("2 files")).toBeInTheDocument();
    expect(within(row).getByText("/coder")).toBeInTheDocument();
    expect(within(row).getByRole("link", { name: "Edit pr-review" })).toBeInTheDocument();
    expect(within(row).queryByLabelText("pr-review is enabled")).toBeNull();

    await userEvent.click(row);
    expect(await screen.findByTestId("skill-editor")).toBeInTheDocument();
    // The page bar names the skill and carries its digest; the editor does not repeat either.
    expect(screen.getByRole("heading", { name: "pr-review" })).toBeInTheDocument();
    expect(screen.getByText("a313660c2de7…")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Skills/ })).toBeInTheDocument();
  });

  it("has a row menu: view details, copy name, then delete after confirm", async () => {
    listSkills.mockResolvedValue({ skills: [skill()], skillsDir: "", maxBytes: 131072n, maxFiles: 64 });
    deleteSkill.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByTestId("skill-actions"));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([
      "View details",
      "Copy name",
      "Delete skill",
    ]);
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete skill" }));
    expect(screen.getByText(/\/coder names it and will fail its next turn/)).toBeInTheDocument();
    expect(deleteSkill).not.toHaveBeenCalled();
    await userEvent.click(screen.getByTestId("skill-row-delete-confirm"));
    expect(deleteSkill).toHaveBeenCalledWith({ name: "pr-review" });
    expect(screen.queryByTestId("skill-editor")).toBeNull();
  });

  it("asks before deleting from the skill's page", async () => {
    listSkills.mockResolvedValue({
      skills: [skill({ fileCount: 1, markdown: "---\nname: pr-review\n---\n" })],
      skillsDir: "",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    deleteSkill.mockResolvedValue({});
    mount("/agent/skills/pr-review");
    await userEvent.click(await screen.findByTestId("skill-delete"));
    expect(deleteSkill).not.toHaveBeenCalled();
    await userEvent.click(screen.getByTestId("skill-delete-confirm"));
    expect(deleteSkill).toHaveBeenCalledWith({ name: "pr-review" });
  });

  it("filters the table", async () => {
    listSkills.mockResolvedValue({
      skills: [skill(), skill({ name: "sql-tuning", description: "Make queries fast.", playbooks: [] })],
      skillsDir: "",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    mount();
    expect(await screen.findAllByTestId("skill-row")).toHaveLength(2);
    await userEvent.type(screen.getByLabelText("Filter skills"), "queries");
    expect(screen.getAllByTestId("skill-row")).toHaveLength(1);
    expect(screen.getByText("sql-tuning")).toBeInTheDocument();
  });

  it("says so when the skill in the address does not exist", async () => {
    mount("/agent/skills/gone");
    expect(await screen.findByText("No such skill")).toBeInTheDocument();
  });

  it("opens a one-file skill in the markdown editor", async () => {
    listSkills.mockResolvedValue({
      skills: [skill({ fileCount: 1, markdown: "---\nname: pr-review\n---\n\nReview it.\n" })],
      skillsDir: "/etc/podium/skills",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    mount();
    await userEvent.click(await screen.findByRole("link", { name: "Edit pr-review" }));
    expect(await screen.findByTestId("skill-editor")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "pr-review" })).toBeInTheDocument();
    expect(screen.getByLabelText("SKILL.md")).toHaveValue("---\nname: pr-review\n---\n\nReview it.\n");
    expect(screen.getByRole("button", { name: "Save skill" })).toBeInTheDocument();
    expect(screen.getByTestId("skill-actions").className).toContain("bg-panel");
    expect(screen.getByTestId("skill-file-tree")).toBeInTheDocument();
    expect(screen.queryByTestId("skill-folder-note")).toBeNull();
  });

  it("does not offer to save a multi-file skill as SKILL.md alone", async () => {
    listSkills.mockResolvedValue({
      skills: [
        skill({
          fileCount: 3,
          markdown: "---\nname: pr-review\n---\n\nReview it.\n",
          files: {
            "SKILL.md": "---\nname: pr-review\n---\n\nReview it.\n",
            "reference/checklist.md": "- read the diff\n",
            "scripts/run.sh": "#!/bin/sh\necho hi\n",
          },
        }),
      ],
      skillsDir: "/etc/podium/skills",
      maxBytes: 131072n,
      maxFiles: 64,
    });
    mount();
    await userEvent.click(await screen.findByRole("link", { name: "Edit pr-review" }));
    expect(await screen.findByTestId("skill-folder-note")).toHaveTextContent("folder of 3 files");
    expect(screen.getByLabelText("SKILL.md")).toHaveValue("---\nname: pr-review\n---\n\nReview it.\n");
    expect(screen.getByRole("button", { name: "Open reference/checklist.md" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open scripts/run.sh" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Open scripts/run.sh" }));
    expect(screen.getByTestId("skill-file-body")).toHaveTextContent("echo hi");
    expect(screen.getByRole("button", { name: "Choose a folder" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save skill" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Choose a file" })).toBeNull();
  });

  it("reports a directory that will not load rather than hiding it", async () => {
    listSkills.mockResolvedValue({
      skills: [
        skill({
          sha256: "",
          description: "",
          problem: "skill \"pr-review\": /etc/podium/skills/pr-review has no SKILL.md",
        }),
      ],
      skillsDir: "/etc/podium/skills",
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
    expect(await screen.findByTestId("skill-new")).toBeInTheDocument();
  });
});
