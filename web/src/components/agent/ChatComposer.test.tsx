import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { AssistantSchema } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT } from "../../lib/agents";
import { catalogue } from "../../test/agents";
import { ChatComposer } from "./ChatComposer";

const assistant = create(AssistantSchema, {
  displayName: "Podium",
  agent: "claude",
  model: "claude-opus-5",
  effort: "high",
});

function mount(over: Partial<Parameters<typeof ChatComposer>[0]> = {}) {
  const onSend = vi.fn();
  render(
    <ChatComposer
      disabled={false}
      agents={catalogue()}
      assistant={assistant}
      choice={INHERIT}
      onChoiceChange={vi.fn()}
      onSend={onSend}
      {...over}
    />,
  );
  return { onSend };
}

describe("ChatComposer", () => {
  it("sends on Enter", async () => {
    const { onSend } = mount();
    await userEvent.type(screen.getByTestId("chat-composer"), "how many accounts{Enter}");
    // The choice rides with the text: which model answers is a per-message decision.
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
    expect(screen.getByTestId("chat-run-config")).toBeDisabled();
  });

  // A conversation runs no playbook, so there is nothing here to pick one with: the turn
  // chooses a playbook for each task it delegates, and may choose several.
  it("offers no playbook control at all", () => {
    mount();
    expect(screen.queryByTestId("chat-playbook")).toBeNull();
  });

  it("treats a leading /word as text rather than eating the first word", async () => {
    const { onSend } = mount();
    await userEvent.type(screen.getByTestId("chat-composer"), "/coder fix it{Enter}");
    expect(onSend).toHaveBeenCalledWith("/coder fix it", INHERIT);
  });

  it("blurs on Escape", async () => {
    mount();
    const box = screen.getByTestId("chat-composer");
    await userEvent.click(box);
    expect(box).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    expect(box).not.toHaveFocus();
  });

  // The point of the picker: ask the same assistant on whichever model, without touching
  // what the tasks it starts will run on.
  it("sends the chosen model with the message", async () => {
    const onSend = vi.fn();
    mount({ onSend, choice: { agent: "grok", model: "grok-4.6", effort: "" } });

    await userEvent.type(screen.getByTestId("chat-composer"), "who is on call{Enter}");
    expect(onSend).toHaveBeenCalledWith("who is on call", {
      agent: "grok",
      model: "grok-4.6",
      effort: "",
    });
  });

  it("shows the assistant's own model as the default, and says tasks keep theirs", async () => {
    mount();
    // The picker is folded into a popover, but the closed control still names the model
    // that will answer, so a human can see what they are about to override.
    const trigger = screen.getByTestId("chat-run-config");
    expect(trigger).toHaveTextContent("claude-opus-5");
    expect(trigger).toHaveTextContent("high");

    await userEvent.click(trigger);
    expect(
      await screen.findByText(/Tasks Podium starts keep their own playbook's model/),
    ).toBeInTheDocument();
    // One click, not a menu inside a menu: the catalogue is the popover.
    const list = await screen.findByRole("listbox", { name: "This message: models" });
    expect(list).toHaveTextContent("Inherit");
    expect(list).toHaveTextContent("grok-4.6");
  });

  it("falls back to a plain default before the assistant has loaded", () => {
    mount({ assistant: undefined });
    expect(screen.getByTestId("chat-run-config")).toHaveTextContent("Default model");
  });
});
