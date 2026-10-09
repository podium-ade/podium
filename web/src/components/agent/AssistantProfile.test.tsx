import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { PageFrame } from "../PageHeader";
import { AssistantForm } from "./AssistantProfile";
import type { AssistantDraft, AssistantSection } from "../../lib/assistantProfile";

const saved: AssistantDraft = {
  id: "ast_1",
  name: "podium",
  displayName: "Podium",
  systemPrompt: "you are Podium",
  agent: "claude",
  model: "claude-opus-5",
  effort: "",
  skills: ["review"],
  mcpServers: [],
  maxTurns: 0,
  timeout: "15m0s",
  gitName: "",
  gitEmail: "",
};

function mount(draft: AssistantDraft, section: AssistantSection = "identity") {
  const onChange = vi.fn();
  const onSave = vi.fn();
  render(
    <MemoryRouter>
      <PageFrame title="Assistant">
        <AssistantForm
          draft={draft}
          saved={saved}
          section={section}
          agents={[]}
          skills={[{ name: "review", detail: "Turned off. A turn that names it fails." }]}
          servers={[]}
          saving={false}
          onChange={onChange}
          onSave={onSave}
        />
      </PageFrame>
    </MemoryRouter>,
  );
  return { onChange, onSave };
}

describe("AssistantForm", () => {
  it("shows the display name and does not mention a file", () => {
    mount(saved);
    expect(screen.queryByLabelText("Name", { selector: "#assistant-name" })).toBeNull();
    expect(screen.getByLabelText("Name", { selector: "#assistant-git-name" })).toBeInTheDocument();
    expect(screen.getByLabelText("Display name")).toHaveValue("Podium");
    expect(screen.queryByText(/profile\.yaml/)).toBeNull();
    expect(screen.queryByRole("link", { name: "Source" })).toBeNull();
    expect(screen.queryByText(/\.podium/)).toBeNull();
  });

  it("saves the whole definition when the display name changes", async () => {
    const { onSave } = mount({ ...saved, displayName: "Night" });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "ast_1",
        name: "podium",
        displayName: "Night",
        systemPrompt: "you are Podium",
        model: "claude-opus-5",
      }),
    );
  });

  it("refuses a half-written git persona", () => {
    mount({ ...saved, gitName: "Only" });
    expect(screen.getByText("Git needs both a name and an email, or neither.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("starts a step cap at 50 and treats 15m0s as the 15 minute preset", async () => {
    const { onChange } = mount(saved, "limits");
    expect(screen.getByRole("radio", { name: "No cap" })).toBeChecked();
    expect(screen.getByRole("button", { name: "15 min" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("radio", { name: "Cap" }));
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ maxTurns: 50 }));
  });

  it("keeps a selected skill that is not installed, and warns when one is off", () => {
    mount({ ...saved, skills: ["review", "missing"] }, "reach");
    expect(screen.getByRole("checkbox", { name: /review/ })).toBeChecked();
    expect(screen.getByText("Turned off. A turn that names it fails.")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: /missing/ })).toBeChecked();
    expect(screen.getByText("Not installed. A turn that names it fails.")).toBeInTheDocument();
  });
});
