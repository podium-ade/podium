import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { ChatPullRequestSchema } from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { ChatPullRequests } from "./ChatPullRequests";

const attachChatPullRequest = vi.fn();
const detachChatPullRequest = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      attachChatPullRequest: (...a: unknown[]) => attachChatPullRequest(...a),
      detachChatPullRequest: (...a: unknown[]) => detachChatPullRequest(...a),
    },
  };
});

function pr(owner: string, repo: string, number: number, source = "turn") {
  return create(ChatPullRequestSchema, {
    url: `https://github.com/${owner}/${repo}/pull/${number}`,
    owner,
    repo,
    number,
    source,
    createdAt: timestampFromDate(new Date()),
  });
}

function mount(pullRequests = [pr("acme", "api", 41)]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <ChatPullRequests chatId="chat_01abc" pullRequests={pullRequests} />
      </ToastHost>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  attachChatPullRequest.mockReset();
  detachChatPullRequest.mockReset();
  attachChatPullRequest.mockResolvedValue({ pullRequests: [] });
  detachChatPullRequest.mockResolvedValue({ pullRequests: [] });
});

describe("ChatPullRequests", () => {
  it("shows each link as owner/repo#number and opens it in a new tab", () => {
    mount([pr("acme", "api", 41), pr("acme", "web", 12, "human")]);

    const links = screen.getAllByTestId("chat-pull-request");
    expect(links).toHaveLength(2);
    expect(links[0]).toHaveTextContent("acme/api#41");
    expect(links[0]).toHaveAttribute("href", "https://github.com/acme/api/pull/41");
    expect(links[0]).toHaveAttribute("target", "_blank");
    expect(links[0]).toHaveAttribute("rel", "noreferrer noopener");
  });

  it("says where a link came from, because a turn's is not a human's", () => {
    mount([pr("acme", "api", 41), pr("acme", "web", 12, "human")]);

    const links = screen.getAllByTestId("chat-pull-request");
    expect(links[0].getAttribute("title")).toContain("found in a turn's answer");
    expect(links[1].getAttribute("title")).toContain("attached by hand");
  });

  it("attaches the URL a human pasted", async () => {
    const user = userEvent.setup();
    mount([]);
    expect(screen.getByText(/No pull requests yet/)).toBeInTheDocument();

    await user.click(screen.getByTestId("chat-pr-add"));
    await user.type(
      screen.getByTestId("chat-pr-url"),
      "https://github.com/acme/api/pull/41/files",
    );
    await user.click(screen.getByRole("button", { name: "Attach" }));

    await waitFor(() =>
      expect(attachChatPullRequest).toHaveBeenCalledWith({
        chatId: "chat_01abc",
        url: "https://github.com/acme/api/pull/41/files",
      }),
    );
    // The form closes; the bar itself is fed by the stream, so nothing local is kept.
    await waitFor(() => expect(screen.queryByTestId("chat-pr-url")).not.toBeInTheDocument());
  });

  it("keeps the form open and says why when the server refuses the URL", async () => {
    const user = userEvent.setup();
    attachChatPullRequest.mockRejectedValue(
      new ConnectError('not a GitHub pull request URL: "https://example.com"', Code.InvalidArgument),
    );
    mount([]);

    await user.click(screen.getByTestId("chat-pr-add"));
    await user.type(screen.getByTestId("chat-pr-url"), "https://example.com");
    await user.click(screen.getByRole("button", { name: "Attach" }));

    expect(await screen.findByText(/not a GitHub pull request URL/)).toBeInTheDocument();
    expect(screen.getByTestId("chat-pr-url")).toHaveValue("https://example.com");
  });

  it("detaches the link the button names", async () => {
    const user = userEvent.setup();
    mount([pr("acme", "api", 41)]);

    await user.click(screen.getByRole("button", { name: "Detach acme/api#41" }));

    await waitFor(() =>
      expect(detachChatPullRequest).toHaveBeenCalledWith({
        chatId: "chat_01abc",
        url: "https://github.com/acme/api/pull/41",
      }),
    );
  });

  it("stays one row high however many links there are", () => {
    const many = Array.from({ length: 8 }, (_, i) => pr("acme", "api", i + 1));
    mount(many);

    expect(screen.getAllByTestId("chat-pull-request")).toHaveLength(8);
    // The strip scrolls sideways rather than wrapping, which is what keeps the bar a
    // fixed height and the conversation where it was.
    expect(screen.getByTestId("chat-pull-requests").className).toContain("overflow-x-auto");
  });
});
