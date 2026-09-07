import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { PlaybookSchema } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT } from "../../lib/agents";
import { catalogue } from "../../test/agents";
import { ChatComposer } from "./ChatComposer";

const playbooks = [
  create(PlaybookSchema, {
    name: "analyst",
    image: "local/agent-warehouse:dev",
    hint: "Answer questions about the data warehouse.",
    chatDefault: true,
  }),
  create(PlaybookSchema, { name: "coder", image: "local/agent-browser:dev", hint: "Write the change." }),
  create(PlaybookSchema, { name: "general", image: "podium-agent-runtime:dev", hint: "Answer the question." }),
];

function mount(over: Partial<Parameters<typeof ChatComposer>[0]> = {}) {
  const onSend = vi.fn();
  const onPlaybookChange = vi.fn();
  render(
    <ChatComposer
      playbooks={playbooks}
      playbook="analyst"
      onPlaybookChange={onPlaybookChange}
      disabled={false}
      agents={catalogue()}
      choice={INHERIT}
      onChoiceChange={vi.fn()}
      onSend={onSend}
      {...over}
    />,
  );
  return { onSend, onPlaybookChange };
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
    expect(screen.getByTestId("chat-playbook")).toBeDisabled();
  });

  it("opens a menu of playbooks and picks one", async () => {
    const { onPlaybookChange } = mount();
    const chip = screen.getByTestId("chat-playbook");
    expect(chip).toHaveTextContent("/analyst");
    await userEvent.click(chip);
    const menu = await screen.findByTestId("chat-playbook-menu");
    expect(menu).toHaveTextContent("/coder");
    expect(menu).toHaveTextContent("/general");
    await userEvent.click(screen.getByRole("button", { name: /\/coder/ }));
    expect(onPlaybookChange).toHaveBeenCalledWith("coder");
  });

  it("marks the current playbook in the menu", async () => {
    mount({ playbook: "general" });
    await userEvent.click(screen.getByTestId("chat-playbook"));
    const selected = await screen.findByRole("option", { selected: true });
    expect(selected).toHaveTextContent("/general");
  });

  it("shows the first playbook when the chosen one is not there", () => {
    mount({ playbook: "gone" });
    expect(screen.getByTestId("chat-playbook")).toHaveTextContent("/analyst");
  });

  it("has no chip at all with no playbooks, and no cycle with one", () => {
    const { unmount } = render(
      <ChatComposer
        playbooks={[]}
        playbook=""
        onPlaybookChange={vi.fn()}
        disabled={false}
        agents={catalogue()}
        choice={INHERIT}
        onChoiceChange={vi.fn()}
        onSend={vi.fn()}
      />,
    );
    expect(screen.queryByTestId("chat-playbook")).toBeNull();
    unmount();

    render(
      <ChatComposer
        playbooks={[playbooks[0]]}
        playbook="analyst"
        onPlaybookChange={vi.fn()}
        disabled={false}
        agents={catalogue()}
        choice={INHERIT}
        onChoiceChange={vi.fn()}
        onSend={vi.fn()}
      />,
    );
    expect(screen.getByTestId("chat-playbook")).toBeDisabled();
  });

  it("locks the chip once the chat has a playbook, and ignores a typed prefix", async () => {
    const { onPlaybookChange } = mount({ playbook: "analyst", playbookLocked: true });
    const chip = screen.getByTestId("chat-playbook");
    expect(chip).toBeDisabled();
    expect(chip).toHaveAccessibleName(/keeps the playbook it started with/);
    await userEvent.click(chip);
    expect(onPlaybookChange).not.toHaveBeenCalled();
    await userEvent.type(screen.getByTestId("chat-composer"), "/coder ");
    expect(onPlaybookChange).not.toHaveBeenCalled();
  });

  it("follows a typed /playbook prefix", async () => {
    const { onPlaybookChange } = mount();
    await userEvent.type(screen.getByTestId("chat-composer"), "/coder ");
    expect(onPlaybookChange).toHaveBeenCalledWith("coder");
  });

  it("ignores a /prefix that is not a playbook", async () => {
    const { onPlaybookChange } = mount();
    await userEvent.type(screen.getByTestId("chat-composer"), "/shrug what now");
    expect(onPlaybookChange).not.toHaveBeenCalled();
  });

  it("blurs on Escape", async () => {
    mount();
    const box = screen.getByTestId("chat-composer");
    await userEvent.click(box);
    expect(box).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    expect(box).not.toHaveFocus();
  });

  // The point of the picker: one playbook, asked on whichever model, without a second playbook
  // that differs from the first by a single field.
  it("sends the chosen model with the message, leaving the playbook alone", async () => {
    const onSend = vi.fn();
    const onPlaybookChange = vi.fn();
    mount({ onSend, onPlaybookChange, choice: { agent: "grok", model: "grok-4.6", effort: "" } });

    await userEvent.type(screen.getByTestId("chat-composer"), "who is on call{Enter}");
    expect(onSend).toHaveBeenCalledWith("who is on call", {
      agent: "grok",
      model: "grok-4.6",
      effort: "",
    });
    // The playbook chip is untouched by the model choice: they are two decisions.
    expect(onPlaybookChange).not.toHaveBeenCalled();
  });

  it("offers the playbook's own model as the default, named", async () => {
    mount();
    // The picker is folded into a popover, but the closed control still says what will run,
    // so a human can see what "the playbook's" means before opening anything.
    const trigger = screen.getByTestId("chat-run-config");
    expect(trigger).toHaveTextContent("The playbook's model");

    await userEvent.click(trigger);
    // One click, not a menu inside a menu: the catalogue is the popover.
    const list = await screen.findByRole("listbox", { name: "This message: models" });
    expect(list).toHaveTextContent("Inherit");
    expect(list).toHaveTextContent("grok-4.6");
  });
});
