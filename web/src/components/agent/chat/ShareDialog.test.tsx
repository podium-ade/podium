import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { ChatSharingSchema, type ChatSharing } from "../../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../../Toast";
import { ShareDialog } from "./ShareDialog";

const getChatSharing = vi.fn();
const shareChat = vi.fn();
const unshareChat = vi.fn();
const updateChatSharing = vi.fn();
const searchMembers = vi.fn();

vi.mock("../../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../../lib/client")>("../../../lib/client");
  return {
    ...actual,
    agent: {
      getChatSharing: (...a: unknown[]) => getChatSharing(...a),
      shareChat: (...a: unknown[]) => shareChat(...a),
      unshareChat: (...a: unknown[]) => unshareChat(...a),
      updateChatSharing: (...a: unknown[]) => updateChatSharing(...a),
      searchMembers: (...a: unknown[]) => searchMembers(...a),
    },
  };
});

type Over = {
  grants?: { login: string; name: string; role: string }[];
  generalAccess?: string;
  generalRole?: string;
  linkToken?: string;
  myRole?: string;
  origin?: string;
  privateSource?: boolean;
};

function sharing(over: Over = {}): ChatSharing {
  return create(ChatSharingSchema, {
    chatId: "chat_1",
    owner: { login: "alice", name: "Alice", role: "owner" },
    grants: over.grants ?? [{ login: "bob", name: "Bob", role: "editor", grantedBy: "alice" }],
    generalAccess: over.generalAccess ?? "restricted",
    generalRole: over.generalRole ?? "viewer",
    linkToken: over.linkToken ?? "",
    myRole: over.myRole ?? "owner",
    origin: over.origin ?? "web",
    privateSource: over.privateSource ?? false,
  });
}

function mount(multiplayer = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <ShareDialog chatId="chat_1" title="Launch" multiplayer={multiplayer} open onOpenChange={() => {}} />
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("ShareDialog", () => {
  beforeEach(() => {
    for (const f of [getChatSharing, shareChat, unshareChat, updateChatSharing, searchMembers]) f.mockReset();
  });

  it("lists the owner first, then everybody with access", async () => {
    getChatSharing.mockResolvedValue({ sharing: sharing() });
    mount();
    const people = await screen.findByTestId("share-people");
    await waitFor(() => expect(within(people).getAllByTestId("share-person")).toHaveLength(2));
    const [owner, bob] = within(people).getAllByTestId("share-person");
    expect(owner).toHaveAttribute("data-login", "alice");
    expect(owner).toHaveTextContent("Owner");
    expect(within(bob).getByRole("combobox", { name: "Role for Bob" })).toHaveValue("editor");
  });

  it("shares with a person found by name, as the role picked", async () => {
    const user = userEvent.setup();
    getChatSharing.mockResolvedValue({ sharing: sharing() });
    searchMembers.mockResolvedValue({
      members: [
        { login: "bob", name: "Bob" },
        { login: "carol", name: "Carol" },
      ],
    });
    shareChat.mockResolvedValue({ sharing: sharing() });
    mount();
    await user.selectOptions(await screen.findByRole("combobox", { name: "Role for new people" }), "viewer");
    await user.type(screen.getByTestId("share-add-input"), "ca");
    const results = await screen.findAllByTestId("share-add-result");
    expect(results).toHaveLength(1);
    expect(results[0]).toHaveTextContent("Carol");
    await user.click(results[0]);
    await waitFor(() => expect(shareChat).toHaveBeenCalledWith({ chatId: "chat_1", login: "carol", role: "viewer" }));
  });

  it("changes a role and removes a person", async () => {
    const user = userEvent.setup();
    getChatSharing.mockResolvedValue({ sharing: sharing() });
    shareChat.mockResolvedValue({ sharing: sharing() });
    unshareChat.mockResolvedValue({ sharing: sharing({ grants: [] }) });
    mount();
    await user.selectOptions(await screen.findByRole("combobox", { name: "Role for Bob" }), "viewer");
    await waitFor(() => expect(shareChat).toHaveBeenCalledWith({ chatId: "chat_1", login: "bob", role: "viewer" }));
    await user.click(screen.getByRole("button", { name: "Remove Bob" }));
    await waitFor(() => expect(unshareChat).toHaveBeenCalledWith({ chatId: "chat_1", login: "bob" }));
    await waitFor(() => expect(screen.getAllByTestId("share-person")).toHaveLength(1));
  });

  it("turns on link sharing and offers the link to copy and reset", async () => {
    const user = userEvent.setup();
    getChatSharing.mockResolvedValue({ sharing: sharing() });
    updateChatSharing.mockResolvedValue({
      sharing: sharing({ generalAccess: "link", generalRole: "editor", linkToken: "tok123" }),
    });
    mount();
    await user.selectOptions(await screen.findByTestId("share-general-access"), "link");
    await waitFor(() =>
      expect(updateChatSharing).toHaveBeenCalledWith({ chatId: "chat_1", generalAccess: "link" }),
    );
    const link = await screen.findByTestId("share-link");
    expect(link).toHaveTextContent("/agent/chat/chat_1?k=tok123");
    expect(screen.getByTestId("share-general-role")).toHaveValue("editor");
    await user.click(screen.getByTestId("share-reset-link"));
    await waitFor(() => expect(updateChatSharing).toHaveBeenLastCalledWith({ chatId: "chat_1", rotateLink: true }));
  });

  it("shows somebody who is not the owner who has access, and nothing to change", async () => {
    getChatSharing.mockResolvedValue({
      sharing: sharing({ myRole: "viewer", generalAccess: "link", linkToken: "" }),
    });
    mount();
    await waitFor(() => expect(screen.getAllByTestId("share-person")).toHaveLength(2));
    expect(screen.queryByTestId("share-add-input")).toBeNull();
    expect(screen.queryByRole("combobox", { name: "Role for Bob" })).toBeNull();
    expect(screen.getByTestId("share-general-access")).toBeDisabled();
    expect(screen.queryByTestId("share-link")).toBeNull();
  });

  it("warns the owner that a first share moves the chat to the company key", async () => {
    getChatSharing.mockResolvedValue({ sharing: sharing({ grants: [] }) });
    mount();
    expect(await screen.findByTestId("share-key-warning")).toHaveTextContent("company’s model key");
    expect(screen.getByTestId("share-key-warning")).toHaveTextContent("their own GitHub account, secrets and MCP servers");
    expect(screen.queryByTestId("share-key-note")).toBeNull();
  });

  it("says a shared chat runs on the company key, without the warning", async () => {
    getChatSharing.mockResolvedValue({ sharing: sharing() });
    mount();
    expect(await screen.findByTestId("share-key-note")).toHaveTextContent("using the company’s model key");
    expect(screen.queryByTestId("share-key-warning")).toBeNull();
  });

  it("treats a chat the conductor marked multiplayer as shared", async () => {
    getChatSharing.mockResolvedValue({ sharing: sharing({ grants: [] }) });
    mount(true);
    expect(await screen.findByTestId("share-key-note")).toBeInTheDocument();
    expect(screen.queryByTestId("share-key-warning")).toBeNull();
  });

  it("warns before a private Slack conversation is shared", async () => {
    getChatSharing.mockResolvedValue({ sharing: sharing({ origin: "slack", privateSource: true }) });
    mount();
    expect(await screen.findByTestId("share-private-warning")).toHaveTextContent("private Slack conversation");
  });
});
