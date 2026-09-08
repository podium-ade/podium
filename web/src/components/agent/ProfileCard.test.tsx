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
      playbooks={["analyst", "general"]}
      agents={catalogue()}
      onSave={onSave}
    />,
  );
}

describe("ProfileCard", () => {
  it("shows the file's value beside every field and leaves the inputs empty when nothing overrides it", () => {
    mount();
    expect(screen.getByLabelText("Display name")).toHaveValue("");
    expect(screen.getByLabelText("Default playbook")).toHaveTextContent("the file's value (general)");
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

  it("picks a default playbook from the loaded playbooks rather than free text", async () => {
    mount();
    await userEvent.click(screen.getByLabelText("Default playbook"));
    await userEvent.click(await screen.findByRole("option", { name: "analyst" }));
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

  it("says so when the assistant has no skills", () => {
    mount();
    expect(screen.getByText("no skills")).toBeInTheDocument();
  });
});
