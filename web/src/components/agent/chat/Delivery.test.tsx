import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Undelivered } from "../../../lib/chatThread";
import { ToastHost } from "../../Toast";
import { DeliveryNotice } from "./Delivery";

const retryChatDelivery = vi.fn();
let refusal: Error | undefined;

vi.mock("../../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../../lib/client")>("../../../lib/client");
  return {
    ...actual,
    // A refusal is thrown from the client call itself, the way a real transport surfaces a
    // ConnectError. (A vi.fn rejected value is reported by vitest as unhandled here, even
    // though the mutation handles it.)
    agent: {
      retryChatDelivery: async (...a: unknown[]) => {
        if (refusal) throw refusal;
        return retryChatDelivery(...a);
      },
    },
  };
});

function mount(items?: Undelivered[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <DeliveryNotice items={items} />
      </ToastHost>
    </QueryClientProvider>,
  );
}

const failed: Undelivered = { chatId: "chat_1", seq: 4n, surface: "slack", status: "failed", error: "channel_not_found" };

describe("DeliveryNotice", () => {
  beforeEach(() => {
    retryChatDelivery.mockReset();
    refusal = undefined;
  });

  it("says nothing when every copy landed", () => {
    const { container } = mount([]);
    expect(container.querySelector("[data-testid=chat-undelivered]")).toBeNull();
    mount(undefined);
    expect(screen.queryByText(/delivered/)).toBeNull();
  });

  it("says an answer did not reach Slack, and retries it", async () => {
    retryChatDelivery.mockResolvedValue({});
    mount([failed]);
    const row = screen.getByTestId("chat-undelivered");
    expect(row).toHaveTextContent("Not delivered to Slack");
    expect(row).toHaveAttribute("title", "channel_not_found");
    await userEvent.click(screen.getByRole("button", { name: /retry/i }));
    await waitFor(() => expect(retryChatDelivery).toHaveBeenCalledWith({ chatId: "chat_1", seq: 4n }));
  });

  it("shows a copy still being retried without a button", () => {
    mount([{ ...failed, surface: "github", status: "pending", error: "timeout" }]);
    expect(screen.getByTestId("chat-undelivered")).toHaveTextContent("Delivering to GitHub — retrying");
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
  });

  it("shows one line per message and surface", () => {
    mount([failed, failed, { ...failed, surface: "github" }]);
    expect(screen.getAllByTestId("chat-undelivered")).toHaveLength(2);
  });

  it("says why a retry was refused", async () => {
    const user = userEvent.setup();
    refusal = new ConnectError("that message has no failed delivery to retry", Code.NotFound);
    mount([failed]);
    await user.click(screen.getByRole("button", { name: /retry/i }));
    expect(await screen.findByRole("status")).toHaveTextContent("no failed delivery to retry");
  });
});
