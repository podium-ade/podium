import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { ToastHost } from "../Toast";
import { SkillsPanel } from "./SkillsPanel";

const listSkills = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listSkills: (...a: unknown[]) => listSkills(...a),
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
    expect(
      screen.getByText(/runs in the turn's container with the turn's credentials/),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("skill-new")).toBeNull();
  });

  it("lists a skill with its size, digest and the playbooks that name it", async () => {
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
    expect(within(row).getByText("a313660c2de7…")).toBeInTheDocument();
    expect(within(row).getByText("/coder")).toBeInTheDocument();
    expect(within(row).getByText("host directory")).toBeInTheDocument();
    expect(within(row).queryByTestId("skill-delete")).toBeNull();
    expect(within(row).queryByLabelText("pr-review is enabled")).toBeNull();
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
    expect(screen.queryByTestId("skill-new")).toBeNull();
  });
});
