import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  AssistantSchema,
  ChatFrameSchema,
  ChatMessageSchema,
  ChatSchema,
  type ChatFrame,
} from "../../gen/podium/agent/v1/agent_pb";
import { TaskStatus } from "../../gen/podium/v1/common_pb";
import { TaskSchema } from "../../gen/podium/v1/task_pb";
import { ToastHost } from "../Toast";
import { ChatPanel } from "./ChatPanel";
import { desktopCandidate, hasDesktop } from "../../lib/desktop";

const streamChat = vi.fn();
const getTask = vi.fn();
const createDesktopTicket = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual =
    await vi.importActual<typeof import("../../lib/client")>(
      "../../lib/client",
    );
  return {
    ...actual,
    agent: {
      listChats: async () => ({ chats: [row] }),
      listPersonalities: async () => ({ personalities: [] }),
      listPlaybooks: async () => ({
        playbooks: [],
        assistant: create(AssistantSchema, { displayName: "Podium" }),
      }),
      streamChat: (...a: unknown[]) => streamChat(...a),
      markChatRead: async () => ({}),
      searchMembers: async () => ({ members: [] }),
    },
    tasks: {
      getTask: (...a: unknown[]) => getTask(...a),
      createDesktopTicket: (...a: unknown[]) => createDesktopTicket(...a),
    },
  };
});

const row = create(ChatSchema, {
  id: "chat_1",
  title: "On the desktop",
  createdAt: timestampFromDate(new Date(Date.now() - 60_000)),
  lastMessageAt: timestampFromDate(new Date(Date.now() - 30_000)),
  origin: "web",
  login: "me",
  myRole: "owner",
});

function frames(...fs: ChatFrame[]) {
  return {
    async *[Symbol.asyncIterator]() {
      for (const f of fs) yield f;
      await new Promise(() => {});
    },
  };
}

const message = (seq: number, role: string, text: string, taskId = "") =>
  create(ChatMessageSchema, {
    chatId: "chat_1",
    seq: BigInt(seq),
    role,
    text,
    taskId,
    ts: timestampFromDate(new Date()),
  });

const msgFrame = (seq: number, role: string, text: string, taskId = "") =>
  create(ChatFrameSchema, {
    frame: { case: "message", value: message(seq, role, text, taskId) },
  });

/** A desktop playbook's task: it exposes "desktop", and is RUNNING while it waits for more. */
const desktopTask = (status = TaskStatus.RUNNING) =>
  create(TaskSchema, {
    id: "task_desk",
    status,
    spec: {
      image: "podium-agent-runtime-desktop:dev",
      expose: { ports: { desktop: { port: 6901 } } },
    },
  });

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter initialEntries={["/agent/chat/chat_1"]}>
          <Routes>
            <Route path="/agent/chat/*" element={<ChatPanel />} />
          </Routes>
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("desktopCandidate", () => {
  it("is the running turn's task, else the newest message that names one", () => {
    const msgs = [
      message(1, "user", "open it"),
      message(2, "assistant", "opened", "task_a"),
      message(3, "user", "now scroll"),
    ];
    expect(desktopCandidate(msgs, "task_running")).toBe("task_running");
    // After the answer the turn's status is finished, but its task still waits with the
    // desktop up: the newest row it posted names it, after a reload too.
    expect(desktopCandidate(msgs)).toBe("task_a");
    expect(desktopCandidate([message(1, "user", "hi")])).toBeUndefined();
  });
});

describe("hasDesktop", () => {
  it("is a running task that exposes the desktop port", () => {
    expect(hasDesktop(desktopTask())).toBe(true);
    expect(hasDesktop(desktopTask(TaskStatus.SUCCEEDED))).toBe(false);
    expect(
      hasDesktop(
        create(TaskSchema, {
          status: TaskStatus.RUNNING,
          spec: { expose: { ports: { web: { port: 3000 } } } },
        }),
      ),
    ).toBe(false);
    expect(hasDesktop(undefined)).toBe(false);
  });
});

describe("ChatPanel desktop", () => {
  beforeEach(() => {
    for (const f of [streamChat, getTask, createDesktopTicket]) f.mockReset();
  });

  it("shows a running desktop turn's desktop beside the conversation, from a link minted for it", async () => {
    streamChat.mockImplementation(() =>
      frames(
        create(ChatFrameSchema, { frame: { case: "chat", value: row } }),
        msgFrame(1, "user", "open the dashboard"),
        msgFrame(2, "assistant", "It's open on the desktop.", "task_desk"),
        create(ChatFrameSchema, {
          frame: { case: "status", value: { state: "finished", taskId: "" } },
        }),
      ),
    );
    getTask.mockResolvedValue({ task: desktopTask() });
    createDesktopTicket.mockResolvedValue({
      ready: true,
      url: "https://podium-desktop.example.com/t/task_desk/tok/",
    });
    mount();

    const frame = await screen.findByTitle("The agent's desktop");
    expect(frame).toHaveAttribute(
      "src",
      "https://podium-desktop.example.com/t/task_desk/tok/",
    );
    expect(frame).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(getTask).toHaveBeenCalledWith({ taskId: "task_desk" });
    expect(createDesktopTicket).toHaveBeenCalledWith({ taskId: "task_desk" });
    expect(
      screen.getByRole("separator", { name: "Resize the desktop" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Open the desktop in a new tab" }),
    ).toHaveAttribute(
      "href",
      "https://podium-desktop.example.com/t/task_desk/tok/",
    );
  });

  it("hides on request, and comes back from the title bar", async () => {
    streamChat.mockImplementation(() =>
      frames(msgFrame(1, "assistant", "on it", "task_desk")),
    );
    getTask.mockResolvedValue({ task: desktopTask() });
    createDesktopTicket.mockResolvedValue({
      ready: true,
      url: "https://podium-desktop.example.com/t/task_desk/tok/",
    });
    const user = userEvent.setup();
    mount();

    await screen.findByTestId("desktop-panel");
    await user.click(screen.getByRole("button", { name: "Hide the desktop" }));
    expect(screen.queryByTestId("desktop-panel")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show the desktop" }));
    expect(await screen.findByTestId("desktop-panel")).toBeInTheDocument();
  });

  it("has the workspace's terminal in a tab beside the desktop, and keeps both connected", async () => {
    streamChat.mockImplementation(() =>
      frames(msgFrame(1, "assistant", "on it", "task_desk")),
    );
    getTask.mockResolvedValue({ task: desktopTask() });
    createDesktopTicket.mockResolvedValue({
      ready: true,
      url: "https://podium-desktop.example.com/t/task_desk/desk/",
      terminalUrl: "https://podium-desktop.example.com/t/task_desk/term/",
    });
    const user = userEvent.setup();
    mount();

    await screen.findByTitle("The agent's desktop");
    // The terminal connects when it is first opened, not before.
    expect(
      screen.queryByTitle("A terminal in the agent's workspace"),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Terminal" }));
    expect(
      screen.getByTitle("A terminal in the agent's workspace"),
    ).toHaveAttribute("src", "https://podium-desktop.example.com/t/task_desk/term/");
    expect(
      screen.getByRole("link", { name: "Open the terminal in a new tab" }),
    ).toHaveAttribute("href", "https://podium-desktop.example.com/t/task_desk/term/");
    // The desktop's stream is still there behind it: switching back does not reconnect.
    expect(screen.getByTitle("The agent's desktop")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Desktop" }));
    expect(
      screen.getByTitle("A terminal in the agent's workspace"),
    ).toBeInTheDocument();
    expect(createDesktopTicket).toHaveBeenCalledTimes(1);
  });

  it("shows the desktop starting, and loads it once it answers", async () => {
    streamChat.mockImplementation(() =>
      frames(msgFrame(1, "assistant", "on it", "task_desk")),
    );
    getTask.mockResolvedValue({ task: desktopTask() });
    // The task runs before its desktop is up: the first answer says it is not ready.
    createDesktopTicket
      .mockResolvedValueOnce({
        ready: false,
        url: "https://podium-desktop.example.com/t/task_desk/early/",
        terminalUrl: "https://podium-desktop.example.com/t/task_desk/early-term/",
      })
      .mockResolvedValue({
        ready: true,
        url: "https://podium-desktop.example.com/t/task_desk/desk/",
        terminalUrl: "https://podium-desktop.example.com/t/task_desk/term/",
      });
    mount();

    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent("Starting the desktop…"),
    );
    // Nothing loads the early link: its frame would keep the error page it got.
    expect(screen.queryByTitle("The agent's desktop")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "Open the desktop in a new tab" }),
    ).not.toBeInTheDocument();

    const frame = await screen.findByTitle("The agent's desktop", undefined, { timeout: 5000 });
    expect(frame).toHaveAttribute("src", "https://podium-desktop.example.com/t/task_desk/desk/");
    expect(createDesktopTicket).toHaveBeenCalledTimes(2);
  });

  it("has no terminal tab for a task that serves none", async () => {
    streamChat.mockImplementation(() =>
      frames(msgFrame(1, "assistant", "on it", "task_desk")),
    );
    getTask.mockResolvedValue({ task: desktopTask() });
    createDesktopTicket.mockResolvedValue({
      ready: true,
      url: "https://podium-desktop.example.com/t/task_desk/desk/",
      terminalUrl: "",
    });
    mount();

    await screen.findByTitle("The agent's desktop");
    expect(screen.getByRole("tab", { name: "Desktop" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Terminal" })).not.toBeInTheDocument();
  });

  it("shows nothing for a task that is not a desktop, or not running any more", async () => {
    streamChat.mockImplementation(() =>
      frames(msgFrame(1, "assistant", "done", "task_desk")),
    );
    getTask.mockResolvedValue({ task: desktopTask(TaskStatus.SUCCEEDED) });
    mount();

    await waitFor(() => expect(getTask).toHaveBeenCalled());
    expect(screen.queryByTestId("desktop-panel")).not.toBeInTheDocument();
    expect(createDesktopTicket).not.toHaveBeenCalled();
  });

  it("says why when the server streams no desktops", async () => {
    streamChat.mockImplementation(() =>
      frames(msgFrame(1, "assistant", "on it", "task_desk")),
    );
    getTask.mockResolvedValue({ task: desktopTask() });
    createDesktopTicket.mockRejectedValue(
      new ConnectError(
        "desktops are not configured on this server: set PODIUM_DESKTOP_URL",
        Code.FailedPrecondition,
      ),
    );
    mount();

    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent(
        "set PODIUM_DESKTOP_URL",
      ),
    );
    expect(screen.queryByTitle("The agent's desktop")).not.toBeInTheDocument();
  });
});
