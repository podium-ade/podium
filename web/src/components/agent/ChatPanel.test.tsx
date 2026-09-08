import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  ChatFrameSchema,
  ChatMessageSchema,
  ChatSchema,
  AssistantSchema,
  PlaybookSchema,
  type ChatFrame,
} from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { ChatPanel } from "./ChatPanel";

const listChats = vi.fn();
const listPlaybooks = vi.fn();
const createChat = vi.fn();
const renameChat = vi.fn();
const deleteChat = vi.fn();
const sendChatMessage = vi.fn();
const streamChat = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listChats: (...a: unknown[]) => listChats(...a),
      listPlaybooks: (...a: unknown[]) => listPlaybooks(...a),
      createChat: (...a: unknown[]) => createChat(...a),
      renameChat: (...a: unknown[]) => renameChat(...a),
      deleteChat: (...a: unknown[]) => deleteChat(...a),
      sendChatMessage: (...a: unknown[]) => sendChatMessage(...a),
      streamChat: (...a: unknown[]) => streamChat(...a),
    },
  };
});

/** live is an async iterable a test can push frames into and never closes on its own,
 *  which is exactly how StreamChat behaves. */
function live() {
  const queued: ChatFrame[] = [];
  let wake: (() => void) | undefined;
  let closed = false;
  return {
    push(frame: ChatFrame) {
      queued.push(frame);
      wake?.();
    },
    close() {
      closed = true;
      wake?.();
    },
    async *[Symbol.asyncIterator]() {
      for (;;) {
        while (queued.length > 0) yield queued.shift() as ChatFrame;
        if (closed) return;
        await new Promise<void>((resolve) => {
          wake = resolve;
        });
      }
    },
  };
}

function message(
  seq: number,
  role: string,
  text: string,
  attachments: unknown[] = [],
  taskId = "",
) {
  return create(ChatFrameSchema, {
    frame: {
      case: "message",
      value: create(ChatMessageSchema, {
        chatId: "chat_01abc",
        seq: BigInt(seq),
        role,
        text,
        ts: timestampFromDate(new Date()),
        taskId,
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        attachments: attachments as any,
      }),
    },
  });
}

// said is message() for a MIRRORED conversation: the same row with somebody's name on it.
function said(seq: number, role: string, text: string, author: string) {
  const frame = message(seq, role, text);
  if (frame.frame.case === "message") {
    frame.frame.value.author = author;
  }
  return frame;
}

// chatRow is the frame StreamChat opens with, which is where origin and participants arrive.
const chatRow = (over: Record<string, unknown>) =>
  create(ChatFrameSchema, {
    frame: { case: "chat", value: create(ChatSchema, { ...chat, ...over }) },
  });

const progress = (text: string) =>
  create(ChatFrameSchema, { frame: { case: "progress", value: text } });

const status = (state: string, taskId = "") =>
  create(ChatFrameSchema, { frame: { case: "status", value: { state, taskId } } });

const pullRequests = (...numbers: number[]) =>
  create(ChatFrameSchema, {
    frame: {
      case: "pullRequests",
      value: {
        pullRequests: numbers.map((n) => ({
          url: `https://github.com/acme/api/pull/${n}`,
          owner: "acme",
          repo: "api",
          number: n,
          source: "turn",
          createdAt: timestampFromDate(new Date()),
        })),
      },
    },
  });

const chat = {
  id: "chat_01abc",
  title: "August numbers",
  createdAt: timestampFromDate(new Date(Date.now() - 60_000)),
  lastMessageAt: timestampFromDate(new Date(Date.now() - 30_000)),
  preview: "how many active accounts",
  turnRunning: false,
};

const playbooks = [
  create(PlaybookSchema, { name: "analyst", image: "data:dev", hint: "Ask the warehouse." }),
  create(PlaybookSchema, { name: "general", image: "runtime:dev", hint: "Answer." }),
];

const assistant = create(AssistantSchema, {
  displayName: "Podium",
  agent: "claude",
  model: "claude-opus-5",


});

function mount(path = "/agent/chat") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/agent/chat/*" element={<ChatPanel />} />
          </Routes>
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("ChatPanel", () => {
  beforeEach(() => {
    listChats.mockReset();
    listPlaybooks.mockReset();
    createChat.mockReset();
    renameChat.mockReset();
    deleteChat.mockReset();
    sendChatMessage.mockReset();
    streamChat.mockReset();
    listChats.mockResolvedValue({ chats: [], nextCursor: "" });
    listPlaybooks.mockResolvedValue({ playbooks, assistant });
    streamChat.mockImplementation(() => live());
  });

  it("says there is nothing yet and offers a new chat", async () => {
    mount();
    expect(await screen.findByText("No chats yet")).toBeInTheDocument();
    expect(screen.getByTestId("chat-new")).toBeEnabled();
    expect(streamChat).not.toHaveBeenCalled();
  });

  it("lists the caller's chats with a preview", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    mount();
    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(list).toHaveTextContent("August numbers"));
    expect(list).toHaveTextContent("how many active accounts");
  });

  it("creates a chat and opens it", async () => {
    createChat.mockResolvedValue({ chat });
    mount();
    await userEvent.click(await screen.findByTestId("chat-new"));
    await waitFor(() => expect(createChat).toHaveBeenCalledWith({ title: "" }));
    // Opening it is what starts the stream.
    await waitFor(() =>
      expect(streamChat).toHaveBeenCalledWith(
        { chatId: "chat_01abc", fromSeq: 0n },
        expect.anything(),
      ),
    );
  });

  it("replays the conversation and follows it live", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "how many active accounts last month"));
    stream.push(message(2, "assistant", "**4,812** in August."));

    const bubbles = await waitFor(() => {
      const found = screen.getAllByTestId("chat-message");
      expect(found).toHaveLength(2);
      return found;
    });
    expect(bubbles[0]).toHaveAttribute("data-role", "user");
    expect(bubbles[1]).toHaveAttribute("data-role", "assistant");
    // The answer goes through the markdown subset, so the bold is an element.
    expect(bubbles[1].querySelector("strong")?.textContent).toBe("4,812");
    // The bot's name labels its run of bubbles.
    expect(screen.getByText("Podium")).toBeInTheDocument();
  });

  it("shows the pull requests a turn produced without opening anything", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "fix the nil dereference and open a PR"));
    stream.push(message(2, "assistant", "Opened https://github.com/acme/api/pull/41."));
    stream.push(pullRequests(41));

    // No click, no tab, no scrolling back through the answer: the link is on the screen.
    const link = await screen.findByTestId("chat-pull-request");
    expect(link).toHaveTextContent("acme/api#41");
    expect(link).toHaveAttribute("href", "https://github.com/acme/api/pull/41");
  });

  it("opens an existing conversation at the bottom", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    // jsdom does no layout; the first message mounts the scroller so the test can
    // give it a viewport shorter than the transcript before the rest of the replay.
    stream.push(message(1, "user", "how many active accounts last month"));
    await screen.findByTestId("chat-scroller");
    const scroller = screen.getByTestId("chat-scroller");
    Object.defineProperty(scroller, "clientHeight", { value: 400, configurable: true });
    Object.defineProperty(scroller, "scrollHeight", { value: 8000, configurable: true });

    stream.push(message(2, "assistant", "**4,812** in August."));
    stream.push(message(3, "user", "and before that"));
    stream.push(message(4, "assistant", "3,901 in July."));

    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(4));
    expect(scroller.scrollTop).toBe(8000);
  });

  it("stops following when the human scrolls up, and the jump control restores it", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "hello"));
    await screen.findByTestId("chat-scroller");
    const scroller = screen.getByTestId("chat-scroller");
    Object.defineProperty(scroller, "clientHeight", { value: 400, configurable: true });
    Object.defineProperty(scroller, "scrollHeight", { value: 8000, configurable: true });
    stream.push(message(2, "assistant", "hi"));
    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(2));

    scroller.scrollTop = 0;
    fireEvent.scroll(scroller);
    expect(await screen.findByRole("button", { name: /Jump to latest/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Jump to latest/ }));
    expect(scroller.scrollTop).toBe(8000);
    expect(screen.queryByRole("button", { name: /Jump to latest/ })).toBeNull();
  });

  it("does not unpin when the transcript grows in place", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "chart it"));
    await screen.findByTestId("chat-scroller");
    const scroller = screen.getByTestId("chat-scroller");
    Object.defineProperty(scroller, "clientHeight", { value: 400, configurable: true });
    Object.defineProperty(scroller, "scrollHeight", { value: 8000, configurable: true });
    stream.push(message(2, "assistant", "here"));
    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(2));
    expect(scroller.scrollTop).toBe(8000);

    // An image decoding grows the column without the human moving the scrollbar.
    Object.defineProperty(scroller, "scrollHeight", { value: 12000, configurable: true });
    fireEvent.scroll(scroller);
    expect(screen.queryByRole("button", { name: /Jump to latest/ })).toBeNull();
  });

  it("puts what the task said in the transcript, under its own name", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "chart it"));
    stream.push(status("started", "task_01xyz"));
    stream.push(message(2, "progress", "I'll read the schema first.", [], "task_01xyz"));
    stream.push(message(3, "progress", "Now the query.", [], "task_01xyz"));
    stream.push(message(4, "assistant", "Here it is.", [], "task_01xyz"));
    stream.push(status("finished"));

    const bubbles = await waitFor(() => {
      const found = screen.getAllByTestId("chat-message");
      expect(found).toHaveLength(4);
      return found;
    });
    expect(bubbles.map((b) => b.getAttribute("data-role"))).toEqual([
      "user",
      "progress",
      "progress",
      "assistant",
    ]);
    // Two voices, two names — and one name per run, not one per message.
    expect(screen.getAllByText("task")).toHaveLength(1);
    // The answer is the bot's, whatever machine produced it, and carries the task as a link.
    expect(screen.getByText("Podium")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "task_01xyz" })).toHaveLength(2);
    // It stays after the turn ends: it is the conversation, not a live view of one.
    await waitFor(() => expect(screen.queryByTestId("chat-progress")).toBeNull());
    expect(screen.getByText("I'll read the schema first.")).toBeInTheDocument();
  });

  // The bug this fixes: the assistant thinks out loud on the host and its lines are stored
  // under the same `progress` role a task's are, so role alone credited the assistant's own
  // words to a container it had not started yet.
  it("credits the assistant's own thinking to the assistant, not to a task", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "shrink the settings text"));
    stream.push(message(2, "progress", "Delegating this to the podium playbook."));
    stream.push(message(3, "progress", "Working on this in a `podium` task", [], "task_01aaa"));
    stream.push(status("finished"));

    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(3));
    // One "task" heading — the delegated one — and the assistant's line is the bot's.
    expect(screen.getAllByText("task")).toHaveLength(1);
    expect(screen.getByText("Podium")).toBeInTheDocument();
  });

  // Two tasks answering one conversation are two answers, and used to render as one run
  // because the grouping was by role.
  it("tells two tasks apart", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "twice please"));
    stream.push(message(2, "progress", "first task working", [], "task_01aaa"));
    stream.push(message(3, "progress", "second task working", [], "task_01bbb"));
    stream.push(status("finished"));

    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(3));
    expect(screen.getAllByText("task")).toHaveLength(2);
    expect(screen.getByRole("link", { name: "task_01aaa" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "task_01bbb" })).toBeInTheDocument();
  });

  it("shows the running state while a turn runs and drops it with the answer", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "chart it"));
    stream.push(status("started", "task_01xyz"));
    stream.push(progress("⏳ reading the schema"));

    expect(await screen.findByTestId("chat-progress")).toHaveTextContent("reading the schema");
    // The composer is disabled in between, which is the UI half of one turn at a time.
    await waitFor(() => expect(screen.getByTestId("chat-composer")).toBeDisabled());

    stream.push(message(2, "assistant", "Here it is."));
    stream.push(status("finished"));

    await waitFor(() => expect(screen.queryByTestId("chat-progress")).toBeNull());
    expect(screen.getByTestId("chat-composer")).toBeEnabled();
  });

  it("waits at the end of the transcript, not above it", async () => {
    // The running state used to be a pill stuck to the top of the transcript. It took its
    // own line in the flow, so starting a turn pushed the whole conversation down. Ordering
    // is the assertion because that is the property that regressed: last, where the answer
    // lands, costs no layout above it.
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "chart it"));
    stream.push(status("started", "task_01xyz"));

    const waiting = await screen.findByTestId("chat-progress");
    const messages = screen.getAllByTestId("chat-message");
    const last = messages[messages.length - 1];
    expect(last.compareDocumentPosition(waiting) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("names the bot and links the task it is waiting on", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "user", "chart it"));
    stream.push(status("started", "task_01xyz"));

    const waiting = await screen.findByTestId("chat-progress");
    // Before a task has said anything there is still something to show, and it is the wait
    // itself rather than an empty line.
    expect(waiting).toHaveTextContent("Thinking");
    expect(within(waiting).getByRole("link", { name: "task_01xyz" })).toHaveAttribute(
      "href",
      "/tasks/task_01xyz",
    );
  });

  it("replaces a message when its attachments arrive on the same seq", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(message(1, "assistant", "See report.csv."));
    await waitFor(() => expect(screen.getAllByTestId("chat-message")).toHaveLength(1));

    stream.push(
      message(1, "assistant", "See report.csv.", [
        { artifactId: "art_01", name: "report.csv", contentType: "text/csv", sizeBytes: 2048n },
      ]),
    );
    // One bubble, not two: the same seq replaces rather than appends.
    await waitFor(() => expect(screen.getByTestId("chat-attachment")).toBeInTheDocument());
    expect(screen.getAllByTestId("chat-message")).toHaveLength(1);
    expect(screen.getByTestId("chat-attachment")).toHaveTextContent("report.csv");
    expect(screen.getByTestId("chat-attachment")).toHaveTextContent("2.0 KB");
  });

  // A model is picked once per conversation: the chat row remembers it, so a reload and a
  // second tab both open on what this chat was last asked for.
  it("opens on the model the chat was last answered on", async () => {
    listChats.mockResolvedValue({
      chats: [{ ...chat, agent: "grok", model: "grok-4.6", effort: "high" }],
      nextCursor: "",
    });
    sendChatMessage.mockResolvedValue({ message: {} });
    mount("/agent/chat/chat_01abc");

    const trigger = await screen.findByTestId("chat-run-config");
    await waitFor(() => expect(trigger).toHaveTextContent("grok-4.6"));
    expect(trigger).toHaveTextContent("high");

    // And it rides with the next message without anybody touching the picker.
    await userEvent.type(await screen.findByTestId("chat-composer"), "again{Enter}");
    await waitFor(() =>
      expect(sendChatMessage).toHaveBeenCalledWith({
        chatId: "chat_01abc",
        text: "again",
        agent: "grok",
        model: "grok-4.6",
        effort: "high",
      }),
    );
  });

  // All three empty is "the assistant's own" and must read as nothing stored, or the picker
  // would show a pinned model where the profile's default belongs.
  it("falls back to the assistant's model when the chat remembers none", async () => {
    listChats.mockResolvedValue({
      chats: [{ ...chat, agent: "", model: "", effort: "" }],
      nextCursor: "",
    });
    mount("/agent/chat/chat_01abc");
    const trigger = await screen.findByTestId("chat-run-config");
    await waitFor(() => expect(trigger).toHaveTextContent("claude-opus-5"));
  });

  // A conversation names no playbook, anywhere: not on the composer, not on the wire, and
  // not on its row in the list. The playbooks are what the turn delegates to.
  it("sends a message with no playbook and shows none", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    sendChatMessage.mockResolvedValue({ message: {} });
    mount("/agent/chat/chat_01abc");

    const box = await screen.findByTestId("chat-composer");
    expect(screen.queryByTestId("chat-playbook")).toBeNull();
    expect(screen.getByTestId("chat-list")).not.toHaveTextContent("/analyst");
    await userEvent.type(box, "how many active accounts{Enter}");

    await waitFor(() =>
      expect(sendChatMessage).toHaveBeenCalledWith({
        chatId: "chat_01abc",
        text: "how many active accounts",
        // Empty means "the assistant's own", which is what the server reads them as.
        agent: "",
        model: "",
        effort: "",
      }),
    );
  });

  it("says so plainly when the server refuses a concurrent send", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    sendChatMessage.mockRejectedValue(
      new ConnectError("a turn is already running for this chat", Code.FailedPrecondition),
    );
    mount("/agent/chat/chat_01abc");

    await userEvent.type(await screen.findByTestId("chat-composer"), "again{Enter}");
    expect(await screen.findByText(/A turn is already running in this chat/)).toBeInTheDocument();
  });

  it("closes the stream when the chat changes", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const signals: AbortSignal[] = [];
    streamChat.mockImplementation((_req: unknown, opts: { signal: AbortSignal }) => {
      signals.push(opts.signal);
      return live();
    });
    const { unmount } = mount("/agent/chat/chat_01abc");
    await waitFor(() => expect(signals).toHaveLength(1));
    expect(signals[0].aborted).toBe(false);

    // Switching chats through the rail: the first stream must be cancelled, not left open.
    createChat.mockResolvedValue({ chat: { ...chat, id: "chat_02def" } });
    await userEvent.click(screen.getByTestId("chat-new"));
    await waitFor(() => expect(signals[0].aborted).toBe(true));
    await waitFor(() => expect(signals).toHaveLength(2));

    // And unmounting closes whatever is open.
    unmount();
    await waitFor(() => expect(signals[1].aborted).toBe(true));
  });

  it("renders an untrusted answer as text, never as markup", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(
      message(1, "assistant", "<img src=x onerror=alert(1)> and [x](javascript:alert(1))"),
    );
    const bubble = await waitFor(() => screen.getByTestId("chat-message"));
    expect(bubble.querySelectorAll("img")).toHaveLength(0);
    expect(bubble.querySelectorAll("a")).toHaveLength(0);
    expect(bubble.textContent).toContain("<img src=x onerror=alert(1)>");
    expect(bubble.textContent).toContain("[x](javascript:alert(1))");
  });

  it("renames a chat from the rail", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    renameChat.mockResolvedValue({ chat: { ...chat, title: "Q3 forecast" } });
    mount();

    await userEvent.click(await screen.findByRole("button", { name: "Rename August numbers" }));
    const box = await screen.findByTestId("chat-title-input");
    await userEvent.clear(box);
    await userEvent.type(box, "Q3 forecast{Enter}");

    await waitFor(() =>
      expect(renameChat).toHaveBeenCalledWith({ chatId: "chat_01abc", title: "Q3 forecast" }),
    );
  });

  it("does not rename when the title is left empty or unchanged", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    mount();

    await userEvent.click(await screen.findByRole("button", { name: "Rename August numbers" }));
    const box = await screen.findByTestId("chat-title-input");
    await userEvent.clear(box);
    await userEvent.type(box, "{Enter}");
    expect(renameChat).not.toHaveBeenCalled();

    await userEvent.click(await screen.findByRole("button", { name: "Rename August numbers" }));
    await userEvent.type(await screen.findByTestId("chat-title-input"), "{Enter}");
    expect(renameChat).not.toHaveBeenCalled();
  });

  it("cancels a rename with Escape", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    mount();

    await userEvent.click(await screen.findByRole("button", { name: "Rename August numbers" }));
    await userEvent.type(await screen.findByTestId("chat-title-input"), "nope{Escape}");
    expect(screen.queryByTestId("chat-title-input")).toBeNull();
    expect(renameChat).not.toHaveBeenCalled();
    expect(await screen.findByTestId("chat-list")).toHaveTextContent("August numbers");
  });

  it("renames from the open chat's title", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    renameChat.mockResolvedValue({ chat: { ...chat, title: "Q3 forecast" } });
    mount("/agent/chat/chat_01abc");

    await userEvent.click(await screen.findByTestId("chat-title"));
    const box = await screen.findByTestId("chat-title-input");
    await userEvent.clear(box);
    await userEvent.type(box, "Q3 forecast{Enter}");

    await waitFor(() =>
      expect(renameChat).toHaveBeenCalledWith({ chatId: "chat_01abc", title: "Q3 forecast" }),
    );
  });

  it("says a missing chat is gone rather than reconnecting the stream", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    streamChat.mockImplementation(() => {
      throw new ConnectError("agent store: not found: chat chat_nope", Code.NotFound);
    });
    mount("/agent/chat/chat_nope");
    expect(await screen.findByText("This chat is gone")).toBeInTheDocument();
    expect(screen.queryByText(/Nothing was lost/)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Back to chats" }));
    expect(await screen.findByText("Pick a chat, or start a new one")).toBeInTheDocument();
  });

  it("asks before deleting, and the chat goes when it is confirmed", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    deleteChat.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByTestId("chat-delete"));

    expect(screen.getByText(/The conversation goes with it/)).toBeInTheDocument();
    expect(deleteChat).not.toHaveBeenCalled();

    listChats.mockResolvedValue({ chats: [], nextCursor: "" });
    await userEvent.click(screen.getByTestId("chat-delete-confirm"));

    await waitFor(() => expect(deleteChat).toHaveBeenCalledWith({ chatId: "chat_01abc" }));
    expect(await screen.findByRole("status")).toHaveTextContent("August numbers deleted.");
    await waitFor(() => expect(screen.getByTestId("chat-list")).not.toHaveTextContent("August numbers"));
  });

  it("warns that a running task will be stopped, and only then deletes", async () => {
    listChats.mockResolvedValue({ chats: [{ ...chat, turnRunning: true }], nextCursor: "" });
    deleteChat.mockResolvedValue({});
    mount();
    await userEvent.click(await screen.findByTestId("chat-delete"));

    expect(screen.getByText(/Stop the task and delete August numbers/)).toBeInTheDocument();
    expect(screen.getByText(/A task is running in this chat/)).toBeInTheDocument();
    expect(deleteChat).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Keep" }));
    expect(deleteChat).not.toHaveBeenCalled();
    expect(screen.getByTestId("chat-list")).toHaveTextContent("August numbers");

    await userEvent.click(await screen.findByTestId("chat-delete"));
    listChats.mockResolvedValue({ chats: [], nextCursor: "" });
    await userEvent.click(screen.getByTestId("chat-delete-confirm"));

    await waitFor(() => expect(deleteChat).toHaveBeenCalledWith({ chatId: "chat_01abc" }));
    expect(await screen.findByRole("status")).toHaveTextContent("August numbers deleted.");
  });

  it("keeps the chat when the confirm is declined", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    mount();
    await userEvent.click(await screen.findByTestId("chat-delete"));
    await userEvent.click(screen.getByRole("button", { name: "Keep" }));

    expect(deleteChat).not.toHaveBeenCalled();
    expect(screen.getByTestId("chat-list")).toHaveTextContent("August numbers");
  });

  it("leaves the conversation when the open chat is deleted", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    deleteChat.mockResolvedValue({});
    mount("/agent/chat/chat_01abc");
    await waitFor(() => expect(streamChat).toHaveBeenCalled());

    await userEvent.click(await screen.findByTestId("chat-delete"));
    listChats.mockResolvedValue({ chats: [], nextCursor: "" });
    await userEvent.click(screen.getByTestId("chat-delete-confirm"));

    expect(await screen.findByText("No chats yet")).toBeInTheDocument();
  });

  it("says the conductor is down without breaking the page", async () => {
    listChats.mockRejectedValue(
      new ConnectError("podium-agent is not reachable", Code.Unavailable),
    );
    mount();
    expect(await screen.findByText(/podium-agent is not reachable/)).toBeInTheDocument();
    expect(screen.getByTestId("chat-new")).toBeInTheDocument();
  });

  it("says so when the conductor does not know who is calling", async () => {
    listChats.mockRejectedValue(new ConnectError("no login", Code.Unauthenticated));
    mount();
    expect(await screen.findByText(/does not know who you are/)).toBeInTheDocument();
  });

  it("says so when the chat is not available at all", async () => {
    listChats.mockRejectedValue(new ConnectError("no chat here", Code.FailedPrecondition));
    mount();
    expect(await screen.findByText(/not available on this conductor/)).toBeInTheDocument();
  });

  // A mirrored conversation has more than one person in it, so a question needs a name over
  // it. A web chat's do not: the only person who can ask is the person reading.
  it("says who asked, in a conversation with more than one asker", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(chatRow({ origin: "slack", startedBy: "alice", participants: ["alice", "bob"] }));
    stream.push(said(1, "user", "what does this repo do?", "alice"));
    stream.push(said(2, "assistant", "It is a task runner.", "Podium"));
    stream.push(said(3, "user", "and the node?", "bob"));

    const authors = await waitFor(() => {
      const found = screen.getAllByTestId("chat-author");
      expect(found).toHaveLength(2);
      return found;
    });
    expect(authors.map((a) => a.textContent)).toEqual(["alice", "bob"]);
  });

  it("offers no composer for a conversation that lives somewhere else", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(chatRow({ origin: "slack", startedBy: "alice", participants: ["alice", "bob"] }));
    stream.push(said(1, "user", "what does this repo do?", "alice"));

    const note = await screen.findByTestId("chat-mirrored-note");
    expect(note).toHaveTextContent("lives in slack");
    expect(note).toHaveTextContent("alice, bob");
    expect(screen.queryByTestId("chat-composer")).not.toBeInTheDocument();
  });

  it("keeps the composer for a conversation Podium owns", async () => {
    listChats.mockResolvedValue({ chats: [chat], nextCursor: "" });
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    stream.push(chatRow({ origin: "web" }));
    stream.push(message(1, "user", "how many active accounts"));

    await screen.findByTestId("chat-message");
    expect(screen.getByTestId("chat-composer")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-mirrored-note")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-author")).not.toBeInTheDocument();
  });

  // Opening a mirrored thread used to render the composer and then take it away, because
  // the only source of the origin was a stream frame that lands after the first render. The
  // list row already knows, so there is nothing to flash: no composer at any point.
  it("never flashes the composer when opening a mirrored thread", async () => {
    listChats.mockResolvedValue({
      chats: [{ ...chat, origin: "slack", startedBy: "alice" }],
      nextCursor: "",
    });
    // A stream that opens and says NOTHING, which is the window the flash happened in.
    const stream = live();
    streamChat.mockImplementation(() => stream);
    mount("/agent/chat/chat_01abc");

    const note = await screen.findByTestId("chat-mirrored-note");
    expect(note).toHaveTextContent("lives in slack");
    expect(screen.queryByTestId("chat-composer")).not.toBeInTheDocument();

    // And it stays gone once the stream confirms what the list already said.
    stream.push(chatRow({ origin: "slack", startedBy: "alice", participants: ["alice"] }));
    stream.push(said(1, "user", "what does this repo do?", "alice"));
    await screen.findByTestId("chat-author");
    expect(screen.queryByTestId("chat-composer")).not.toBeInTheDocument();
  });

  it("marks a mirrored thread in the list and says who started it", async () => {
    listChats.mockResolvedValue({
      chats: [{ ...chat, origin: "slack", startedBy: "alice" }],
      nextCursor: "",
    });
    mount();

    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(list).toHaveTextContent("slack"));
    expect(list).toHaveTextContent("alice");
  });

});
