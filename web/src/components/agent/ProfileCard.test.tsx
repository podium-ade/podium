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
    defaultPlaybook: "general",
    profileDir: "/etc/podium/agent",
    fileDisplayName: "Podium",
    fileModel: "claude-opus-5",
    fileDefaultPlaybook: "general",
    ...fields,
  });
}

function mount(p = profile()) {
  onSave.mockReset();
  return render(
    <ProfileCard
      profile={p}
      agents={catalogue()}
      onSave={onSave}
    />,
  );
}

describe("ProfileCard", () => {
  it("shows the file's value beside every field and leaves the inputs empty when nothing overrides it", () => {
    mount();
    expect(screen.getByLabelText("Display name")).toHaveValue("");
    expect(screen.queryByLabelText("Default playbook")).toBeNull();
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
      defaultPlaybook: "",
    });
  });

  it("sends a stored default-playbook override back unchanged, without offering it as a setting", async () => {
    mount(profile({ defaultPlaybook: "analyst", overridden: ["default_playbook"] }));
    expect(screen.queryByLabelText("Default playbook")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Save profile" }));

    expect(onSave).toHaveBeenCalledWith(
      expect.objectContaining({ defaultPlaybook: "analyst" }),
    );
  });

  it("says the name and the prompt are not editable here", () => {
    mount();
    expect(screen.getByText(/name and its prompt come from/i)).toBeInTheDocument();
    expect(screen.queryByLabelText("Name")).toBeNull();
  });

  // What the thing running beside the master key may execute is a repository decision, so
  // the card reports it and offers no way to change it.
  it("shows the assistant's skills and turn cap without letting a browser edit them", () => {
    mount(profile({ skills: ["validate-pr"], maxTurns: 12 }));
    expect(screen.getByText("validate-pr")).toBeInTheDocument();
    expect(screen.getByText("12")).toBeInTheDocument();
    expect(screen.getByText(/profile.yaml only/i)).toBeInTheDocument();
  });

  it("says so when the assistant has no skills and no turn cap", () => {
    mount();
    expect(screen.getByText("no skills")).toBeInTheDocument();
    // Unset is a decision, not a blank: the assistant delegates, so the cap worth having is
    // on the container it starts.
    expect(screen.getByText("no turn limit")).toBeInTheDocument();
  });
});
