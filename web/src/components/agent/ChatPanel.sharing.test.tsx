import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  ChatFrameSchema,
  ChatMessageSchema,
  ChatSchema,
  AssistantSchema,
  type ChatFrame,
} from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { ChatPanel } from "./ChatPanel";

const listChats = vi.fn();
const streamChat = vi.fn();
const leaveChat = vi.fn();
const markChatRead = vi.fn();
const getChatSharing = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listChats: (...a: unknown[]) => listChats(...a),
      listPersonalities: async () => ({ personalities: [] }),
      listPlaybooks: async () => ({ playbooks: [], assistant: create(AssistantSchema, { displayName: "Podium" }) }),
      streamChat: (...a: unknown[]) => streamChat(...a),
      leaveChat: (...a: unknown[]) => leaveChat(...a),
      markChatRead: (...a: unknown[]) => markChatRead(...a),
      getChatSharing: (...a: unknown[]) => getChatSharing(...a),
      searchMembers: async () => ({ members: [] }),
    },
  };
});

function frames(...fs: ChatFrame[]) {
  return {
    async *[Symbol.asyncIterator]() {
      for (const f of fs) yield f;
      await new Promise(() => {});
    },
  };
}

const row = (over: Record<string, unknown>) =>
  create(ChatSchema, {
    id: "chat_1",
    title: "Launch",
    createdAt: timestampFromDate(new Date(Date.now() - 60_000)),
    lastMessageAt: timestampFromDate(new Date(Date.now() - 30_000)),
    preview: "hello",
    origin: "web",
    ...over,
  });

const chatFrame = (over: Record<string, unknown>) =>
  create(ChatFrameSchema, { frame: { case: "chat", value: row(over) } });

const msg = (seq: number, text: string) =>
  create(ChatFrameSchema, {
    frame: {
      case: "message",
      value: create(ChatMessageSchema, {
        chatId: "chat_1",
        seq: BigInt(seq),
        role: "user",
        text,
        ts: timestampFromDate(new Date()),
      }),
    },
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

describe("ChatPanel sharing", () => {
  beforeEach(() => {
    for (const f of [listChats, streamChat, leaveChat, markChatRead, getChatSharing]) f.mockReset();
    markChatRead.mockResolvedValue({});
  });

  it("groups shared and workspace chats, shows unread, and offers each role its own menu", async () => {
    const mine = row({ id: "c_mine", title: "Mine", login: "me", myRole: "owner", accessReason: "owner", unreadCount: 2n });
    const edit = row({ id: "c_edit", title: "Edits", login: "alice", myRole: "editor", accessReason: "shared", sharedBy: "alice", sharedWithMe: true });
    const view = row({ id: "c_view", title: "Views", login: "alice", myRole: "viewer", accessReason: "shared", sharedBy: "link", sharedWithMe: true });
    const ws = row({ id: "c_ws", title: "Everyone", login: "alice", myRole: "viewer", accessReason: "workspace" });
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace" ? { chats: [ws] } : { chats: [mine, edit, view] },
    );
    const user = userEvent.setup();
    mount();
    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(list).toHaveTextContent("Everyone"));
    expect(screen.getByTestId("chat-origin-shared")).toHaveTextContent("Shared with me");
    expect(screen.getByTestId("chat-origin-workspace")).toHaveTextContent("Workspace");
    expect(within(list).getAllByTestId("chat-row-unread")).toHaveLength(1);
    expect(within(list).getByTestId("chat-row-unread")).toHaveTextContent("2");
    expect(list).toHaveTextContent("Shared by alice");
    expect(list).toHaveTextContent("Opened from a link · view only");

    await user.click(screen.getByRole("button", { name: "Actions for Mine" }));
    expect(screen.getByTestId("chat-rename")).toBeInTheDocument();
    expect(screen.getByTestId("chat-delete")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-leave")).toBeNull();
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("button", { name: "Actions for Edits" }));
    expect(screen.getByTestId("chat-rename")).toBeInTheDocument();
    expect(screen.getByTestId("chat-leave")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-delete")).toBeNull();
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("button", { name: "Actions for Views" }));
    expect(screen.queryByTestId("chat-rename")).toBeNull();
    expect(screen.queryByTestId("chat-delete")).toBeNull();
    leaveChat.mockResolvedValue({});
    await user.click(screen.getByTestId("chat-leave"));
    await waitFor(() => expect(leaveChat).toHaveBeenCalledWith({ chatId: "c_view" }));

    expect(screen.queryByRole("button", { name: "Actions for Everyone" })).toBeNull();
  });

  it("opens a viewer's chat read-only, with Share, and marks it read", async () => {
    listChats.mockResolvedValue({ chats: [] });
    streamChat.mockImplementation(() =>
      frames(chatFrame({ myRole: "viewer", login: "alice", sharedWithMe: true }), msg(1, "hi"), msg(4, "there")),
    );
    mount("/agent/chat/chat_1");
    expect(await screen.findByTestId("chat-view-only")).toHaveTextContent("You have view access");
    expect(screen.queryByTestId("chat-composer")).toBeNull();
    expect(screen.getByTestId("chat-share")).toBeInTheDocument();
    await waitFor(() => expect(markChatRead).toHaveBeenLastCalledWith({ chatId: "chat_1", seq: 4n }));
  });

  it("passes a share link's token to the stream", async () => {
    listChats.mockResolvedValue({ chats: [] });
    streamChat.mockImplementation(() => frames(chatFrame({ myRole: "editor", login: "alice", sharedWithMe: true })));
    mount("/agent/chat/chat_1?k=tok123");
    await waitFor(() => expect(streamChat).toHaveBeenCalled());
    expect(streamChat.mock.calls[0][0]).toMatchObject({ chatId: "chat_1", linkToken: "tok123" });
    expect(await screen.findByTestId("chat-composer")).toBeInTheDocument();
  });
});
