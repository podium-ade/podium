import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import {
  AssistantSchema,
  ChatFrameSchema,
  ChatLinearIssueSchema,
  ChatMessageSchema,
  ChatSchema,
  type ChatFrame,
} from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { ChatPanel } from "./ChatPanel";

const listChats = vi.fn();
const streamChat = vi.fn();
const setChatRailHidden = vi.fn();
const markChatRead = vi.fn(async () => ({}));

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listChats: (...a: unknown[]) => listChats(...a),
      listPersonalities: async () => ({ personalities: [] }),
      listPlaybooks: async () => ({ playbooks: [], assistant: create(AssistantSchema, { displayName: "Podium" }) }),
      streamChat: (...a: unknown[]) => streamChat(...a),
      setChatRailHidden: (...a: unknown[]) => setChatRailHidden(...a),
      markChatRead: (...a: unknown[]) => markChatRead(...(a as [])),
      getChatSharing: async () => ({}),
      searchMembers: async () => ({ members: [] }),
      listRoutines: async () => ({ routines: [] }),
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

const ticketChat = (over: Record<string, unknown> = {}) =>
  create(ChatSchema, {
    id: "c_eme6",
    title: "EME-6 Login is flaky",
    origin: "linear",
    myRole: "editor",
    createdAt: timestampFromDate(new Date(Date.now() - 3_600_000)),
    lastMessageAt: timestampFromDate(new Date(Date.now() - 60_000)),
    preview: "Can you check Safari?",
    linearIssue: create(ChatLinearIssueSchema, {
      identifier: "EME-6",
      title: "Login is flaky",
      stateName: "In Progress",
      stateType: "started",
      askedAt: timestampFromDate(new Date(Date.now() - 60_000)),
      waitingOnMe: true,
    }),
    ...over,
  });

const message = (seq: number, over: Record<string, unknown>) =>
  create(ChatFrameSchema, {
    frame: {
      case: "message",
      value: create(ChatMessageSchema, { chatId: "c_eme6", seq: BigInt(seq), ts: timestampFromDate(new Date()), ...over }),
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

describe("ChatPanel ticket conversations", () => {
  beforeEach(() => {
    for (const f of [listChats, streamChat, setChatRailHidden]) f.mockReset();
    window.localStorage.clear();
  });

  it("draws a ticket in the rail as its identifier and title, its state, and whether it waits on the reader", async () => {
    const quiet = ticketChat({
      id: "c_eme7",
      title: "EME-7 Docs",
      lastMessageAt: timestampFromDate(new Date(Date.now() - 7_200_000)),
      linearIssue: create(ChatLinearIssueSchema, { identifier: "EME-7", title: "Docs", stateName: "Done", stateType: "completed" }),
    });
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace" ? { chats: [] } : { chats: [quiet, ticketChat()] },
    );
    mount();
    const list = await screen.findByTestId("chat-list");
    await waitFor(() => expect(list).toHaveTextContent("Login is flaky"));

    const ids = within(list).getAllByTestId("chat-row-ticket").map((el) => el.textContent);
    expect(ids).toEqual(["EME-6", "EME-7"]);
    const states = within(list).getAllByTestId("chat-row-ticket-state");
    expect(states[0]).toHaveTextContent("In Progress");
    expect(states[0].querySelector("[data-tone]")).toHaveAttribute("data-tone", "started");
    expect(states[1]).toHaveTextContent("Done");
    expect(within(list).getAllByTestId("chat-row-waiting")).toHaveLength(1);
    expect(within(list).getByTestId("chat-row-waiting")).toHaveTextContent("Waiting on you");
  });

  it("offers Hide, not Rename, Leave or Delete, and hides it", async () => {
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace" ? { chats: [] } : { chats: [ticketChat()] },
    );
    setChatRailHidden.mockResolvedValue({});
    const user = userEvent.setup();
    mount();
    await screen.findByText("Login is flaky");
    await user.click(screen.getByRole("button", { name: "Actions for EME-6 Login is flaky" }));
    expect(screen.queryByTestId("chat-rename")).toBeNull();
    expect(screen.queryByTestId("chat-leave")).toBeNull();
    expect(screen.queryByTestId("chat-delete")).toBeNull();
    await user.click(screen.getByTestId("chat-hide"));
    await waitFor(() => expect(setChatRailHidden).toHaveBeenCalledWith({ chatId: "c_eme6", hidden: true }));
  });

  it("folds somebody else's turn into a count of its steps and says whose it was", async () => {
    listChats.mockResolvedValue({ chats: [] });
    streamChat.mockImplementation(() =>
      frames(
        create(ChatFrameSchema, { frame: { case: "chat", value: ticketChat() } }),
        message(1, { role: "user", text: "@Podium fix the login", author: "Alice", authorLogin: "alice@acme.com", via: "linear" }),
        message(2, { role: "activity", redacted: true, ranAs: "alice@acme.com", ranAsName: "Alice" }),
        message(3, { role: "activity", redacted: true, ranAs: "alice@acme.com", ranAsName: "Alice" }),
        message(4, { role: "progress", redacted: true, ranAs: "alice@acme.com", ranAsName: "Alice" }),
        message(5, { role: "activity", redacted: true, ranAs: "alice@acme.com", ranAsName: "Alice" }),
        message(6, { role: "assistant", text: "Fixed in #42.", ranAs: "alice@acme.com", ranAsName: "Alice" }),
        create(ChatFrameSchema, { frame: { case: "replayed", value: true } }),
      ),
    );
    mount("/agent/chat/c_eme6");
    const steps = await screen.findByTestId("chat-foreign-steps");
    expect(steps).toHaveTextContent("4 steps, ran as Alice");
    expect(screen.getByTestId("chat-ran-as")).toHaveTextContent("ran as Alice");
    expect(screen.getByText("Fixed in #42.")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-trail")).toBeNull();
  });
});
