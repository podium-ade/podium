import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { AssistantSchema } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT } from "../../lib/agents";
import { catalogue } from "../../test/agents";
import { ChatComposer, type QueuedItem } from "./ChatComposer";

const assistant = create(AssistantSchema, {
  displayName: "Podium",
  agent: "claude",
  model: "claude-opus-5",
  effort: "high",
});

function mount(over: Partial<Parameters<typeof ChatComposer>[0]> = {}) {
  const onSend = vi.fn();
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ChatComposer
        disabled={false}
        agents={catalogue()}
        assistant={assistant}
        choice={INHERIT}
        onChoiceChange={vi.fn()}
        onSend={onSend}
        {...over}
      />
    </QueryClientProvider>,
  );
  return { onSend };
}

describe("ChatComposer", () => {
  const item = (id: string, text: string, over: Partial<QueuedItem> = {}): QueuedItem => ({
    id,
    text,
    author: "alice",
    mine: true,
    editable: true,
    ...over,
  });

  it("lists what is queued, and takes a message back into the box to edit it", async () => {
    const onRemoveQueued = vi.fn();
    mount({
      running: true,
      queueing: true,
      queued: [item("1", "first"), item("2", "second")],
      onRemoveQueued,
      onInterrupt: vi.fn(),
    });
    expect(screen.getAllByTestId("chat-pending-item")).toHaveLength(2);
    expect(screen.getByTestId("chat-pending-status")).toHaveTextContent(/^2 queued$/);
    expect(screen.getByTestId("chat-send")).toHaveAttribute("aria-label", "Queue");

    await userEvent.click(screen.getAllByTestId("chat-pending-edit")[1]);
    expect(onRemoveQueued).toHaveBeenCalledWith("2");
    expect(screen.getByTestId("chat-composer")).toHaveValue("second");

    await userEvent.click(screen.getAllByTestId("chat-pending-remove")[0]);
    expect(onRemoveQueued).toHaveBeenCalledWith("1");
  });

  it("interrupts the answer in progress from the queue", async () => {
    const onInterrupt = vi.fn();
    const { onSend } = mount({ running: true, queueing: true, queued: [item("1", "first")], onInterrupt });
    await userEvent.click(screen.getByTestId("chat-pending-interrupt"));
    expect(onInterrupt).toHaveBeenCalled();
    expect(onSend).not.toHaveBeenCalled();
  });

  it("shows everybody's queue in a shared chat, and lets each person touch only their own", () => {
    mount({
      multiplayer: true,
      running: true,
      queueing: true,
      queued: [
        item("1", "@Podium the totals?", { author: "bob", mine: false, editable: false }),
        item("2", "@Podium and the chart", { mine: true }),
        item("3", "from slack", { mine: true, editable: false }),
      ],
      onRemoveQueued: vi.fn(),
    });
    const rows = screen.getAllByTestId("chat-pending-item");
    expect(rows.map((r) => r.dataset.mine)).toEqual(["false", "true", "true"]);
    expect(within(rows[0]).getByTestId("chat-pending-author")).toHaveTextContent("bob");
    expect(within(rows[1]).getByTestId("chat-pending-author")).toHaveTextContent("You");
    expect(within(rows[0]).queryByTestId("chat-pending-remove")).toBeNull();
    expect(within(rows[1]).getByTestId("chat-pending-edit")).toBeInTheDocument();
    // Already in the record: it can be taken back, but sending it again would leave a copy.
    expect(within(rows[2]).queryByTestId("chat-pending-edit")).toBeNull();
    expect(within(rows[2]).getByTestId("chat-pending-remove")).toBeInTheDocument();
    expect(screen.getByTestId("chat-pending-status")).toHaveTextContent("yours is 2nd");
  });

  it("folds a long queue on a phone behind +N more", async () => {
    mount({ running: true, queueing: true, queued: ["1", "2", "3", "4"].map((id) => item(id, `q${id}`)) });
    const rows = screen.getAllByTestId("chat-pending-item");
    // Hidden below the sm breakpoint only; a wide screen shows the lot (max-sm:hidden).
    expect(rows.map((r) => r.className.includes("max-sm:hidden"))).toEqual([false, false, true, true]);
    await userEvent.click(screen.getByTestId("chat-pending-more"));
    expect(screen.getAllByTestId("chat-pending-item").some((r) => r.className.includes("max-sm:hidden"))).toBe(false);
    expect(screen.getByTestId("chat-pending-more")).toHaveTextContent("Show less");
  });

  it("queues only what is put to Podium in a shared chat", async () => {
    mount({ multiplayer: true, running: true, queueing: true });
    const box = screen.getByTestId("chat-composer");
    await userEvent.type(box, "bob, look");
    expect(screen.getByTestId("chat-send")).toHaveAttribute("aria-label", "Send");
    await userEvent.type(box, " @Podium");
    expect(screen.getByTestId("chat-send")).toHaveAttribute("aria-label", "Queue for Podium");
  });

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

  it("is disabled while a send is in flight and says what it is doing", () => {
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
    expect(screen.getByTestId("chat-effort")).toHaveTextContent("Auto");

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

  it("puts an effort menu on the composer for the effective model", () => {
    mount();
    expect(screen.getByTestId("chat-effort")).toHaveTextContent("Auto");
  });

  it("picks an effort from the menu without requiring a model pick", async () => {
    const onChoiceChange = vi.fn();
    mount({ onChoiceChange });
    await userEvent.click(screen.getByTestId("chat-effort"));
    expect(await screen.findByText("Effort")).not.toHaveClass("uppercase");
    await userEvent.click(await screen.findByRole("radio", { name: "Low" }));
    expect(onChoiceChange).toHaveBeenCalledWith({ agent: "", model: "", effort: "low" });
  });

  it("attaches a pasted text file and inlines it on send", async () => {
    const { onSend } = mount();
    const input = screen.getByTestId("chat-attach-input");
    const notes = new File(["a,b\n1,2"], "notes.csv", { type: "text/csv" });
    await userEvent.upload(input, notes);

    expect(await screen.findByTestId("chat-pending-file")).toHaveTextContent("notes.csv");
    await userEvent.type(screen.getByTestId("chat-composer"), "look at this{Enter}");
    expect(onSend).toHaveBeenCalledTimes(1);
    const [text, choice] = onSend.mock.calls[0];
    expect(text).toContain("look at this");
    expect(text).toContain("Attached `notes.csv`");
    expect(text).toContain("a,b\n1,2");
    expect(choice).toEqual(INHERIT);
    expect(screen.queryByTestId("chat-pending-file")).toBeNull();
  });

  it("sends a named image with no other text", async () => {
    const { onSend } = mount();
    const shot = new File([new Uint8Array([137, 80, 78, 71])], "shot.png", { type: "image/png" });
    await userEvent.upload(screen.getByTestId("chat-attach-input"), shot);
    expect(await screen.findByTestId("chat-pending-file")).toHaveTextContent("shot.png");

    await userEvent.click(screen.getByTestId("chat-send"));
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onSend.mock.calls[0][0]).toContain("Attached image `shot.png`");
  });

  it("stays open while a turn runs, and offers to stop it", async () => {
    const onStop = vi.fn();
    mount({ running: true, onStop });
    expect(screen.getByTestId("chat-composer")).toBeEnabled();
    await userEvent.click(screen.getByTestId("chat-stop"));
    expect(onStop).toHaveBeenCalled();
    expect(screen.getByTestId("chat-composer-note")).toHaveTextContent("gets its own turn");
  });

  it("says a shared chat answers only a mention, and suggests @Podium", async () => {
    mount({ multiplayer: true });
    expect(screen.getByTestId("chat-multiplayer-hint")).toHaveTextContent("mention @Podium");
    const box = screen.getByTestId("chat-composer");
    await userEvent.type(box, "hey @Po");
    const option = await screen.findByTestId("chat-mention-option");
    expect(option).toHaveTextContent("Podium");
    await userEvent.type(box, "{Enter}");
    expect(box).toHaveValue("hey @Podium ");
    expect(screen.getByTestId("chat-send")).toHaveAttribute("aria-label", "Send to Podium");
  });

  it("answers to the chat's assistant by name in a shared chat", async () => {
    mount({ multiplayer: true, personality: "Researcher" });
    await userEvent.type(screen.getByTestId("chat-composer"), "@Researcher dig in");
    expect(screen.getByTestId("chat-send")).toHaveAttribute("aria-label", "Send to Podium");
  });

  it("says a mirrored conversation is one-way", () => {
    mount({ mirrored: "github", multiplayer: true });
    expect(screen.getByTestId("chat-composer-note")).toHaveTextContent(
      "Messages here stay in Podium — they aren't posted to GitHub.",
    );
  });

  it("names Linear when a Linear session's chat is written in", () => {
    mount({ mirrored: "linear", multiplayer: true });
    expect(screen.getByTestId("chat-composer-note")).toHaveTextContent(
      "Messages here stay in Podium — they aren't posted to Linear.",
    );
  });
});
