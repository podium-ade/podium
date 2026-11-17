import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ToastHost } from "./Toast";
import { NotificationInbox } from "./NotificationInbox";
import type { AppNotification } from "../lib/notifications";

const sendChatMessage = vi.fn();
const markNotificationsRead = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    agent: {
      sendChatMessage: (...a: unknown[]) => sendChatMessage(...a),
      markNotificationsRead: (...a: unknown[]) => markNotificationsRead(...a),
    },
  };
});

function question(over: Partial<AppNotification> = {}): AppNotification {
  return {
    id: "inbox:12:1",
    key: "inbox:12",
    level: "alert",
    title: "Podium asks you about ENG-12 · Export is slow",
    body: "Which region is slow?",
    href: "/agent/chat/chat_t",
    action: "Open conversation",
    createdAt: Date.now() - 60_000,
    reply: { chatId: "chat_t", notificationId: "12" },
    ...over,
  };
}

function mount(rows: AppNotification[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter>
          <NotificationInbox rows={rows} />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("NotificationInbox, a question", () => {
  beforeEach(() => {
    sendChatMessage.mockReset();
    markNotificationsRead.mockReset();
    sendChatMessage.mockResolvedValue({ addressed: true });
    markNotificationsRead.mockResolvedValue({});
  });

  it("shows the ticket and the question, and answers it into its conversation", async () => {
    mount([question()]);
    expect(screen.getByText("Podium asks you about ENG-12 · Export is slow")).toBeInTheDocument();
    expect(screen.getByText("Which region is slow?")).toBeInTheDocument();
    const send = screen.getByRole("button", { name: "Send answer" });
    expect(send).toBeDisabled();

    await userEvent.type(screen.getByLabelText("Your answer"), "eu-west, since Monday");
    await userEvent.click(send);

    await waitFor(() =>
      expect(sendChatMessage).toHaveBeenCalledWith({ chatId: "chat_t", text: "eu-west, since Monday" }),
    );
    expect(await screen.findByTestId("question-sent")).toHaveTextContent("Answer sent.");
    expect(screen.queryByTestId("question-reply")).not.toBeInTheDocument();
  });

  it("sends with Ctrl+Enter", async () => {
    mount([question()]);
    await userEvent.type(screen.getByLabelText("Your answer"), "main{Control>}{Enter}{/Control}");
    await waitFor(() => expect(sendChatMessage).toHaveBeenCalledWith({ chatId: "chat_t", text: "main" }));
  });

  it("keeps the answer when the send fails", async () => {
    sendChatMessage.mockRejectedValue(new Error("view only"));
    mount([question()]);
    await userEvent.type(screen.getByLabelText("Your answer"), "eu-west");
    await userEvent.click(screen.getByRole("button", { name: "Send answer" }));
    expect(await screen.findByText("view only")).toBeInTheDocument();
    expect(screen.getByLabelText("Your answer")).toHaveValue("eu-west");
  });

  it("dismisses the server's row without answering", async () => {
    mount([question()]);
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(markNotificationsRead).toHaveBeenCalledWith({ ids: [12n] }));
    expect(sendChatMessage).not.toHaveBeenCalled();
  });

  it("offers no answer once it is resolved", () => {
    mount([question({ resolvedAt: Date.now() })]);
    expect(screen.queryByTestId("question-reply")).not.toBeInTheDocument();
    expect(screen.getByText("Which region is slow?")).toBeInTheDocument();
  });

  it("keeps an ordinary notice's single action", () => {
    mount([question({ reply: undefined, action: "Open", key: "inbox:3", id: "inbox:3:1" })]);
    expect(screen.queryByTestId("question-reply")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open" })).toHaveAttribute("href", "/agent/chat/chat_t");
  });
});
