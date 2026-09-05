import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { SkillSchema } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT } from "../../lib/agents";
import { catalogue } from "../../test/agents";
import { ChatComposer } from "./ChatComposer";

const skills = [
  create(SkillSchema, {
    name: "analyst",
    image: "local/agent-warehouse:dev",
    hint: "Answer questions about the data warehouse.",
    chatDefault: true,
  }),
  create(SkillSchema, { name: "coder", image: "local/agent-browser:dev", hint: "Write the change." }),
  create(SkillSchema, { name: "general", image: "podium-agent-runtime:dev", hint: "Answer the question." }),
];

function mount(over: Partial<Parameters<typeof ChatComposer>[0]> = {}) {
  const onSend = vi.fn();
  const onSkillChange = vi.fn();
  render(
    <ChatComposer
      skills={skills}
      skill="analyst"
      onSkillChange={onSkillChange}
      disabled={false}
      agents={catalogue()}
      choice={INHERIT}
      onChoiceChange={vi.fn()}
      onSend={onSend}
      {...over}
    />,
  );
  return { onSend, onSkillChange };
}

describe("ChatComposer", () => {
  it("sends on Enter", async () => {
    const { onSend } = mount();
    await userEvent.type(screen.getByTestId("chat-composer"), "how many accounts{Enter}");
    // The choice rides with the text: what runs the message is a per-message decision.
    expect(onSend).toHaveBeenCalledWith("how many accounts", INHERIT);
    // And the box is empty again, so the next question starts clean.
    expect(screen.getByTestId("chat-composer")).toHaveValue("");
  });

  it("does not send on Shift+Enter, and makes a newline instead", async () => {
    const { onSend } = mount();
    const box = screen.getByTestId("chat-composer");
    await userEvent.type(box, "line one{Shift>}{Enter}{/Shift}line two");
    expect(onSend).not.toHaveBeenCalled();
    expect(box).toHaveValue("line one\nline two");
  });

  it("sends on the send button and refuses an empty message", async () => {
    const { onSend } = mount();
    expect(screen.getByTestId("chat-send")).toBeDisabled();

    await userEvent.type(screen.getByTestId("chat-composer"), "   ");
    expect(screen.getByTestId("chat-send")).toBeDisabled();
    await userEvent.type(screen.getByTestId("chat-composer"), "{Enter}");
    expect(onSend).not.toHaveBeenCalled();

    await userEvent.type(screen.getByTestId("chat-composer"), "a question");
    await userEvent.click(screen.getByTestId("chat-send"));
    expect(onSend).toHaveBeenCalledWith("a question", INHERIT);
  });

  it("is disabled while a turn runs and says what it is doing", () => {
    mount({ disabled: true });
    const box = screen.getByTestId("chat-composer");
    expect(box).toBeDisabled();
    expect(box).toHaveAttribute("placeholder", "Working…");
    expect(screen.getByTestId("chat-send")).toBeDisabled();
    expect(screen.getByTestId("chat-skill")).toBeDisabled();
  });

  it("cycles the skill chip on click", async () => {
    const { onSkillChange } = mount();
    const chip = screen.getByTestId("chat-skill");
    expect(chip).toHaveTextContent("/analyst");
    await userEvent.click(chip);
    expect(onSkillChange).toHaveBeenCalledWith("coder");
  });

  it("wraps round the end of the skill list", async () => {
    const { onSkillChange } = mount({ skill: "general" });
    await userEvent.click(screen.getByTestId("chat-skill"));
    expect(onSkillChange).toHaveBeenCalledWith("analyst");
  });

  it("shows the first skill when the chosen one is not there", () => {
    mount({ skill: "gone" });
    expect(screen.getByTestId("chat-skill")).toHaveTextContent("/analyst");
  });

  it("has no chip at all with no skills, and no cycle with one", () => {
    const { unmount } = render(
      <ChatComposer
        skills={[]}
        skill=""
        onSkillChange={vi.fn()}
        disabled={false}
        agents={catalogue()}
        choice={INHERIT}
        onChoiceChange={vi.fn()}
        onSend={vi.fn()}
      />,
    );
    expect(screen.queryByTestId("chat-skill")).toBeNull();
    unmount();

    render(
      <ChatComposer
        skills={[skills[0]]}
        skill="analyst"
        onSkillChange={vi.fn()}
        disabled={false}
        agents={catalogue()}
        choice={INHERIT}
        onChoiceChange={vi.fn()}
        onSend={vi.fn()}
      />,
    );
    expect(screen.getByTestId("chat-skill")).toBeDisabled();
  });

  it("follows a typed /skill prefix", async () => {
    const { onSkillChange } = mount();
    await userEvent.type(screen.getByTestId("chat-composer"), "/coder ");
    expect(onSkillChange).toHaveBeenCalledWith("coder");
  });

  it("ignores a /prefix that is not a skill", async () => {
    const { onSkillChange } = mount();
    await userEvent.type(screen.getByTestId("chat-composer"), "/shrug what now");
    expect(onSkillChange).not.toHaveBeenCalled();
  });

  it("blurs on Escape", async () => {
    mount();
    const box = screen.getByTestId("chat-composer");
    await userEvent.click(box);
    expect(box).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    expect(box).not.toHaveFocus();
  });

  // The point of the picker: one skill, asked on whichever model, without a second skill
  // that differs from the first by a single field.
  it("sends the chosen model with the message, leaving the skill alone", async () => {
    const onSend = vi.fn();
    const onSkillChange = vi.fn();
    mount({ onSend, onSkillChange, choice: { agent: "grok", model: "grok-4.6", effort: "" } });

    await userEvent.type(screen.getByTestId("chat-composer"), "who is on call{Enter}");
    expect(onSend).toHaveBeenCalledWith("who is on call", {
      agent: "grok",
      model: "grok-4.6",
      effort: "",
    });
    // The skill chip is untouched by the model choice: they are two decisions.
    expect(onSkillChange).not.toHaveBeenCalled();
  });

  it("offers the skill's own model as the default, named", () => {
    mount();
    // "analyst" in the fixture resolves to a model; the closed picker says which, so a
    // human can see what "the skill's" means before choosing anything else.
    expect(screen.getByTestId("agent-picker-trigger")).toHaveTextContent("The skill's model");
  });
});
