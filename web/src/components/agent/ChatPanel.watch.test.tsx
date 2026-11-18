import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { AssistantSchema, ChatFrameSchema, ChatSchema, type Chat } from "../../gen/podium/agent/v1/agent_pb";
import { IdentityKind } from "../../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../../lib/identity";
import { ToastHost } from "../Toast";
import { ChatPanel } from "./ChatPanel";

const listChats = vi.fn();
const streamChat = vi.fn();
const setChatWatched = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listChats: (...a: unknown[]) => listChats(...a),
      listPersonalities: async () => ({ personalities: [] }),
      listPlaybooks: async () => ({ playbooks: [], assistant: create(AssistantSchema, { displayName: "Podium" }) }),
      streamChat: (...a: unknown[]) => streamChat(...a),
      markChatRead: async () => ({}),
      getChatSharing: async () => ({}),
      searchMembers: async () => ({ members: [] }),
      listRoutines: async () => ({ routines: [] }),
      setChatWatched: (...a: unknown[]) => setChatWatched(...a),
    },
  };
});

const me: Viewer = {
  login: "ada@acme.com",
  displayName: "Ada",
  kind: IdentityKind.USER,
  agentEnabled: true,
  serverVersion: "dev",
  roles: ["admin"],
  claimed: true,
  hostedDomain: "acme.com",
  canClaim: false,
  googleAuthEnabled: true,
  claimDomain: "",
  pictureUrl: "",
};

const row = (over: Record<string, unknown>) =>
  create(ChatSchema, {
    id: "c_mine",
    title: "Mine",
    createdAt: timestampFromDate(new Date(Date.now() - 60_000)),
    lastMessageAt: timestampFromDate(new Date(Date.now() - 30_000)),
    preview: "hello",
    origin: "web",
    login: "ada@acme.com",
    myRole: "owner",
    accessReason: "owner",
    ...over,
  });

/** stream is a chat's stream: its chat frame, then nothing more. */
function stream(chat: Chat) {
  return {
    async *[Symbol.asyncIterator]() {
      yield create(ChatFrameSchema, { frame: { case: "chat", value: chat } });
      await new Promise(() => {});
    },
  };
}

const routineChat = row({
  id: "c_routine",
  title: "Fix failing CI",
  login: "",
  myRole: "viewer",
  accessReason: "",
  routineId: "rtn_1",
});

/** Go is a link elsewhere in the app, to leave a chat by navigating. */
function Go() {
  const navigate = useNavigate();
  return (
    <button type="button" onClick={() => navigate("/agent/chat/c_mine")}>
      go mine
    </button>
  );
}

function mount(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <ViewerContext value={me}>
          <MemoryRouter initialEntries={[path]}>
            <Go />
            <Routes>
              <Route path="/agent/chat/*" element={<ChatPanel />} />
            </Routes>
          </MemoryRouter>
        </ViewerContext>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("the chat rail's Viewing entry and Watch", () => {
  let watched: boolean;
  beforeEach(() => {
    watched = false;
    for (const f of [listChats, streamChat, setChatWatched]) f.mockReset();
    listChats.mockImplementation(async (req: { section?: string }) => {
      if (req.section === "workspace") return { chats: [] };
      return { chats: watched ? [row({}), row({ ...routineChat, watched: true })] : [row({})] };
    });
    streamChat.mockImplementation((req: { chatId: string }) =>
      stream(req.chatId === "c_routine" ? row({ ...routineChat, watched }) : row({})),
    );
    setChatWatched.mockImplementation(async (req: { watched: boolean }) => {
      watched = req.watched;
      return {};
    });
  });

  it("shows a chat the rail has no row for at the top, and lets it go on navigation", async () => {
    const user = userEvent.setup();
    mount("/agent/chat/c_routine");
    const viewing = await screen.findByTestId("chat-viewing");
    expect(viewing).toHaveTextContent("Viewing");
    expect(viewing).toHaveTextContent("Fix failing CI");
    expect(within(viewing).getByTestId("chat-viewing-kind")).toHaveTextContent("Routine");
    expect(within(viewing).getByTestId("chat-viewing-watch")).toHaveTextContent("Watch");

    await user.click(screen.getByRole("button", { name: "go mine" }));
    await waitFor(() => expect(screen.queryByTestId("chat-viewing")).not.toBeInTheDocument());
  });

  it("does not show one for a chat the rail already lists", async () => {
    mount("/agent/chat/c_mine");
    await waitFor(() => expect(screen.getByTestId("chat-list")).toHaveTextContent("Mine"));
    await waitFor(() => expect(streamChat).toHaveBeenCalled());
    expect(screen.queryByTestId("chat-viewing")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-watch")).not.toBeInTheDocument();
  });

  it("keeps a watched chat in the rail, and Unwatch takes it out", async () => {
    const user = userEvent.setup();
    mount("/agent/chat/c_routine");
    await user.click(await screen.findByTestId("chat-viewing-watch"));
    await waitFor(() => expect(setChatWatched).toHaveBeenCalledWith({ chatId: "c_routine", watched: true }));

    const list = screen.getByTestId("chat-list");
    await waitFor(() => expect(screen.queryByTestId("chat-viewing")).not.toBeInTheDocument());
    expect(list).toHaveTextContent("Fix failing CI");
    // The header offers Unwatch for a watched chat, and so does its row.
    expect(screen.getByTestId("chat-watch")).toHaveTextContent("Unwatch");
    await user.click(screen.getByRole("button", { name: "Actions for Fix failing CI" }));
    await user.click(screen.getByTestId("chat-unwatch"));
    await waitFor(() => expect(setChatWatched).toHaveBeenLastCalledWith({ chatId: "c_routine", watched: false }));
    await waitFor(() => expect(screen.getByTestId("chat-viewing")).toBeInTheDocument());
  });

  it("shows one for a chat in the rail that the search hides, with nothing to watch", async () => {
    listChats.mockImplementation(async (req: { section?: string }) =>
      req.section === "workspace"
        ? { chats: [] }
        : { chats: ["Alpha", "Beta", "Gamma", "Delta", "Mine"].map((t, i) => row({ id: i === 4 ? "c_mine" : `c_${i}`, title: t })) },
    );
    const user = userEvent.setup();
    mount("/agent/chat/c_mine");
    await user.type(await screen.findByTestId("chat-search"), "alpha");
    const viewing = await screen.findByTestId("chat-viewing");
    expect(viewing).toHaveTextContent("Mine");
    expect(within(viewing).queryByTestId("chat-viewing-watch")).not.toBeInTheDocument();
  });
});
