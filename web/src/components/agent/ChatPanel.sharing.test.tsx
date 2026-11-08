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
const listRoutines = vi.fn(async () => ({ routines: [] as unknown[] }));

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
      listRoutines: (...a: unknown[]) => listRoutines(...(a as [])),
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

describe("ChatPanel rail groups", () => {
  beforeEach(() => {
    for (const f of [listChats, streamChat, leaveChat, markChatRead, getChatSharing]) f.mockReset();
    markChatRead.mockResolvedValue({});
    window.localStorage.clear();
  });

  const prs = (n: number) =>
    Array.from({ length: n }, (_, i) =>
      row({
        id: `c_pr${i}`,
        title: `#${100 + i}: Change ${i}`,
        origin: "github",
        startedBy: "octocat",
        surfaces: [{ kind: "github", label: `acme/api#${100 + i}`, url: "" }],
        lastMessageAt: timestampFromDate(new Date(Date.now() - (i + 1) * 60_000)),
      }),
    );

  it("says under a routine's chat whether it is a system routine and when it last ran", async () => {
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace"
        ? { chats: [] }
        : {
            chats: [
              row({ id: "c_sys", title: "Fix failing CI", routineId: "rtn_sys", login: "" }),
              row({ id: "c_mine", title: "PR digest", routineId: "rtn_mine" }),
            ],
          },
    );
    listRoutines.mockResolvedValue({
      routines: [
        { id: "rtn_sys", chatId: "c_sys", scope: "system", running: false, lastRunAt: timestampFromDate(new Date(Date.now() - 2 * 3_600_000)) },
        { id: "rtn_mine", chatId: "c_mine", scope: "personal", running: false },
      ],
    });
    mount();
    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(within(list).getByTestId("chat-row-system")).toHaveTextContent("System"));
    const lines = within(list).getAllByTestId("chat-row-where").map((e) => e.textContent);
    expect(lines).toContain("Systemran 2h ago");
    expect(lines).toContain("not run yet");
  });

  it("lists a page of each origin and shows more on demand", async () => {
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace" ? { chats: [] } : { chats: [row({ id: "c_web", title: "Mine" }), ...prs(11)] },
    );
    const user = userEvent.setup();
    mount();
    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(list).toHaveTextContent("Change 0"));
    expect(list).toHaveTextContent("Change 7");
    expect(list).not.toHaveTextContent("Change 8");
    const where = within(list).getAllByTestId("chat-row-where")[0];
    expect(where).toHaveTextContent("acme/api");
    expect(where).toHaveTextContent("octocat");
    const more = screen.getByTestId("chat-more-github");
    expect(more).toHaveTextContent("Show 3 more· 3 left");
    await user.click(more);
    expect(list).toHaveTextContent("Change 10");
    expect(screen.queryByTestId("chat-more-github")).not.toBeInTheDocument();
  });

  it("narrows the list to one source and remembers the choice", async () => {
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace" ? { chats: [] } : { chats: [row({ id: "c_web", title: "Mine" }), ...prs(2)] },
    );
    const user = userEvent.setup();
    const { unmount } = mount();
    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(list).toHaveTextContent("Mine"));
    expect(screen.getByTestId("chat-source-all")).toHaveAttribute("aria-checked", "true");
    expect(screen.getByTestId("chat-source-github")).toHaveTextContent("2");
    await user.click(screen.getByTestId("chat-source-github"));
    expect(screen.getByTestId("chat-source-github")).toHaveAttribute("aria-checked", "true");
    expect(list).toHaveTextContent("Change 0");
    expect(list).not.toHaveTextContent("Mine");
    expect(screen.queryByTestId("chat-origin-github")).not.toBeInTheDocument();
    unmount();

    mount();
    const again = await screen.findByTestId("chat-list");
    await waitFor(() => expect(again).toHaveTextContent("Change 0"));
    expect(again).not.toHaveTextContent("Mine");
  });

  it("draws only marks past four sources, keeping the chosen count and an unread dot", async () => {
    const chats = [
      row({ id: "c_web", title: "Mine" }),
      ...prs(2),
      row({ id: "c_sl", title: "Oncall", origin: "slack", channel: "eng", unreadCount: 2n }),
      row({ id: "c_sh", title: "Plan", login: "alice", myRole: "editor", accessReason: "shared", sharedWithMe: true }),
      row({ id: "c_ws", title: "Runbook", login: "carol", myRole: "viewer", accessReason: "workspace" }),
    ];
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace" ? { chats: [] } : { chats },
    );
    mount();
    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(list).toHaveTextContent("Runbook"));
    expect(screen.getByTestId("chat-source-all")).toHaveTextContent("6");
    expect(screen.getByTestId("chat-source-github")).not.toHaveTextContent("2");
    expect(screen.getByTestId("chat-source-unread-slack")).toBeInTheDocument();
    expect(screen.getByTestId("chat-source-slack")).toHaveAccessibleName("Slack, 2 unread");
    expect(screen.queryByTestId("chat-source-unread-github")).not.toBeInTheDocument();
  });

  it("folds an origin away and remembers it", async () => {
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace" ? { chats: [] } : { chats: [row({ id: "c_web", title: "Mine" }), ...prs(2)] },
    );
    const user = userEvent.setup();
    const { unmount } = mount();
    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(list).toHaveTextContent("Change 0"));
    const github = screen.getByTestId("chat-origin-github");
    expect(github).toHaveAttribute("aria-expanded", "true");
    await user.click(github);
    expect(github).toHaveAttribute("aria-expanded", "false");
    expect(list).not.toHaveTextContent("Change 0");
    expect(list).toHaveTextContent("Mine");
    unmount();

    mount();
    const again = await screen.findByTestId("chat-list");
    await waitFor(() => expect(again).toHaveTextContent("Mine"));
    expect(screen.getByTestId("chat-origin-github")).toHaveAttribute("aria-expanded", "false");
    expect(again).not.toHaveTextContent("Change 0");
  });
});
