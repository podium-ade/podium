import { describe, expect, it, vi } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AgentProfileSchema } from "../../gen/podium/agent/v1/agent_pb";
import { catalogue } from "../../test/agents";
import { ProfileCard } from "./ProfileCard";

const onSave = vi.fn();

function profile(fields: MessageInitShape<typeof AgentProfileSchema> = {}) {
  return create(AgentProfileSchema, {
    name: "podium",
    displayName: "Podium",
    model: "claude-opus-5",
    defaultSkill: "general",
    profileDir: "/etc/podium/agent",
    fileDisplayName: "Podium",
    fileModel: "claude-opus-5",
    fileDefaultSkill: "general",
    ...fields,
  });
}

function mount(p = profile()) {
  onSave.mockReset();
  return render(
    <ProfileCard
      profile={p}
      skills={["analyst", "general"]}
      agents={catalogue()}
      onSave={onSave}
    />,
  );
}

describe("ProfileCard", () => {
  it("shows the file's value beside every field and leaves the inputs empty when nothing overrides it", () => {
    mount();
    expect(screen.getByLabelText("Display name")).toHaveValue("");
    expect(screen.getByLabelText("Default skill")).toHaveValue("");
    // The model is a picker now, and with nothing overriding it it offers the file's value.
    expect(screen.getByTestId("agent-picker-trigger")).toHaveTextContent("Use profile.yaml's");
    // The file is what is in force, so it has to be on the screen.
    expect(screen.getAllByText("Podium").length).toBeGreaterThan(0);
    expect(screen.getByText("/etc/podium/agent")).toBeInTheDocument();
  });

  it("seeds a field from the override that is in force and marks it", () => {
    mount(profile({ displayName: "Nightshift", overridden: ["display_name"] }));
    expect(screen.getByLabelText("Display name")).toHaveValue("Nightshift");
    expect(screen.getByText("overriding the file")).toBeInTheDocument();
  });

  it("clears an override by sending an empty string", async () => {
    mount(profile({ displayName: "Nightshift", overridden: ["display_name"] }));
    await userEvent.click(screen.getByRole("button", { name: "Use the file value" }));
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));

    expect(onSave).toHaveBeenCalledWith({
      displayName: "",
      model: "",
      agent: "",
      effort: "",
      defaultSkill: "",
      chatDefaultSkill: "",
    });
  });

  it("picks a default skill from the loaded skills rather than free text", async () => {
    mount();
    const select = screen.getByLabelText("Default skill");
    await userEvent.selectOptions(select, "analyst");
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));

    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({ defaultSkill: "analyst" }),
    );
  });

  it("says the name and the prompt are not editable here", () => {
    mount();
    expect(screen.getByText(/name and system prompt come from/i)).toBeInTheDocument();
    expect(screen.queryByLabelText("Name")).toBeNull();
  });
});
