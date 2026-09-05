import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { catalogue } from "../../test/agents";
import { AgentPicker } from "./AgentPicker";

const onChange = vi.fn();

function mount(value: AgentChoice = INHERIT, props: Partial<Parameters<typeof AgentPicker>[0]> = {}) {
  onChange.mockReset();
  return render(
    <AgentPicker
      label="Skill"
      value={value}
      onChange={onChange}
      agents={catalogue()}
      inherit={{ label: "Inherit from the profile", hint: "whatever the profile is set to" }}
      inherited={{ agent: "claude", model: "claude-opus-5", effort: "" }}
      {...props}
    />,
  );
}

async function open() {
  await userEvent.click(screen.getByTestId("agent-picker-trigger"));
}

/**
 * Controlled mounts the picker the way a form does — its own state, fed back in — which is
 * the only way to test typing: a value prop that never changes would swallow every
 * keystroke but the last.
 */
function Controlled({ initial = INHERIT }: { initial?: AgentChoice }) {
  const [value, setValue] = useState(initial);
  return (
    <AgentPicker
      label="Skill"
      value={value}
      onChange={setValue}
      agents={catalogue()}
      inherit={{ label: "Inherit from the profile", hint: "whatever the profile is set to" }}
      inherited={{ agent: "claude", model: "claude-opus-5", effort: "" }}
    />
  );
}

describe("AgentPicker", () => {
  it("shows what is inherited rather than an empty control", () => {
    mount();
    const trigger = screen.getByTestId("agent-picker-trigger");
    expect(trigger).toHaveTextContent("Inherit from the profile");
    // The effective model is on the closed control, because "inherit" on its own does not
    // tell an operator what will actually run.
    expect(trigger).toHaveTextContent("claude-opus-5");
  });

  it("groups the models by backend and says which ones have a credential", async () => {
    mount();
    await open();
    const list = screen.getByRole("listbox", { name: "Skill: models" });
    expect(within(list).getByText("Claude")).toBeInTheDocument();
    expect(within(list).getByText("Grok")).toBeInTheDocument();
    expect(within(list).getByText("credential set")).toBeInTheDocument();
    expect(within(list).getByText("no credential")).toBeInTheDocument();
  });

  it("picking a model picks its backend too", async () => {
    mount();
    await open();
    await userEvent.click(screen.getByText("grok-4.6"));
    expect(onChange).toHaveBeenCalledWith({ agent: "grok", model: "grok-4.6", effort: "" });
  });

  it("drops an effort the newly chosen model does not accept", async () => {
    // max is an Anthropic level; no Grok model takes one, so switching must not carry it.
    mount({ agent: "claude", model: "claude-opus-5", effort: "max" });
    await open();
    await userEvent.click(screen.getByText("grok-4.6"));
    expect(onChange).toHaveBeenCalledWith({ agent: "grok", model: "grok-4.6", effort: "" });
  });

  it("keeps an effort both models accept", async () => {
    mount({ agent: "claude", model: "claude-opus-5", effort: "high" });
    await open();
    await userEvent.click(screen.getByText("grok-4.6"));
    expect(onChange).toHaveBeenCalledWith({ agent: "grok", model: "grok-4.6", effort: "high" });
  });

  it("offers only the levels the chosen model accepts", () => {
    mount({ agent: "grok", model: "grok-4.5", effort: "" });
    const strip = screen.getByTestId("effort-strip");
    expect(within(strip).getByRole("radio", { name: "low" })).toBeInTheDocument();
    expect(within(strip).getByRole("radio", { name: "high" })).toBeInTheDocument();
    // grok-4.5 treats xhigh as high, so offering it would be offering a lie.
    expect(within(strip).queryByRole("radio", { name: "xhigh" })).toBeNull();
    expect(within(strip).queryByRole("radio", { name: "max" })).toBeNull();
  });

  it("has no effort strip until a model is chosen", () => {
    mount();
    expect(screen.queryByTestId("effort-strip")).toBeNull();
  });

  it("sets the effort without touching the model", async () => {
    mount({ agent: "grok", model: "grok-4.6", effort: "" });
    await userEvent.click(within(screen.getByTestId("effort-strip")).getByRole("radio", { name: "xhigh" }));
    expect(onChange).toHaveBeenCalledWith({ agent: "grok", model: "grok-4.6", effort: "xhigh" });
  });

  it("filters as you type and keeps the backend heading on what is left", async () => {
    mount();
    await open();
    await userEvent.type(screen.getByLabelText("Skill: search models"), "sonnet");
    const list = screen.getByRole("listbox", { name: "Skill: models" });
    expect(within(list).getByText("claude-sonnet-5")).toBeInTheDocument();
    expect(within(list).queryByText("grok-4.6")).toBeNull();
    // The heading survives the filter, so a filtered row still says what it runs on.
    expect(within(list).getByText("Claude")).toBeInTheDocument();
  });

  it("chooses with the keyboard", async () => {
    mount();
    await open();
    // The first row is "Inherit"; one step down is the first Claude model.
    await userEvent.keyboard("{ArrowDown}{Enter}");
    expect(onChange).toHaveBeenCalledWith({
      agent: "claude",
      model: "claude-opus-5",
      effort: "",
    });
  });

  it("closes on Escape without choosing anything", async () => {
    mount();
    await open();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("listbox", { name: "Skill: models" })).toBeNull();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("takes a model id it has never heard of", async () => {
    render(<Controlled />);
    await open();
    await userEvent.click(screen.getByTestId("agent-picker-custom"));
    await userEvent.selectOptions(screen.getByLabelText("Skill: backend"), "grok");
    await userEvent.type(screen.getByLabelText("Skill: model id"), "grok-5");

    expect(screen.getByLabelText("Skill: model id")).toHaveValue("grok-5");
    const trigger = screen.getByTestId("agent-picker-trigger");
    expect(trigger).toHaveTextContent("grok-5");
    expect(trigger).toHaveTextContent("not in the catalogue");
  });

  it("shows a model that is not in the catalogue rather than silently dropping it", () => {
    mount({ agent: "grok", model: "grok-99", effort: "" });
    expect(screen.getByTestId("agent-picker-trigger")).toHaveTextContent("grok-99");
    expect(screen.getByLabelText("Skill: model id")).toHaveValue("grok-99");
  });

  it("still offers an effort for a model it has never heard of", () => {
    // The backend's own levels stand in. Losing the control because a model is new would be
    // a worse answer than offering a level the provider might refuse.
    mount({ agent: "grok", model: "grok-99", effort: "" });
    const strip = screen.getByTestId("effort-strip");
    expect(within(strip).getByRole("radio", { name: "xhigh" })).toBeInTheDocument();
    // Still no max: no Grok model has one, so the backend's union has none either.
    expect(within(strip).queryByRole("radio", { name: "max" })).toBeNull();
  });

  it("warns when the chosen backend has no credential, and still allows the choice", () => {
    mount({ agent: "grok", model: "grok-4.6", effort: "" });
    expect(screen.getByTestId("agent-picker-unready")).toHaveTextContent(
      /No xAI credential is stored/,
    );
  });

  it("says nothing about credentials for a backend that has one", () => {
    mount({ agent: "claude", model: "claude-opus-5", effort: "" });
    expect(screen.queryByTestId("agent-picker-unready")).toBeNull();
  });

  it("offers no inherit row when the level above has nothing to inherit from", async () => {
    mount(INHERIT, { inherit: undefined, inherited: undefined });
    await open();
    const list = screen.getByRole("listbox", { name: "Skill: models" });
    expect(within(list).queryByText("Inherit")).toBeNull();
  });
});
