import type { Chat } from "../gen/podium/agent/v1/agent_pb";

/**
 * Phase 5 of docs/agent.md: a chat is shared the way a document is. The
 * server decides every permission (chat_role); these helpers only say what the UI offers.
 */

export type ChatRole = "owner" | "editor" | "viewer" | "";

/** roleOf is the caller's role on a chat, as the server reported it. */
export function roleOf(chat: Pick<Chat, "myRole"> | undefined): ChatRole {
  const r = chat?.myRole ?? "";
  return r === "owner" || r === "editor" || r === "viewer" ? r : "";
}

/** canWrite is whether the composer is offered: the owner and editors. */
export function canWrite(role: ChatRole): boolean {
  return role === "owner" || role === "editor";
}

/** roleLabel is a role in the words the share dialog uses. */
export function roleLabel(role: string): string {
  if (role === "owner") return "Owner";
  if (role === "editor") return "Can edit";
  if (role === "viewer") return "Can view";
  return "";
}

/**
 * generalAccessLabel names a general-access mode. Every mode needs a Podium sign-in: "link"
 * is not public, it lets in anybody signed in who also holds the link.
 */
export function generalAccessLabel(access: string): string {
  if (access === "members") return "Everyone in Podium";
  if (access === "link") return "Anyone in Podium with the link";
  return "Restricted";
}

/** generalAccessHint says what a mode means, under its picker. */
export function generalAccessHint(access: string, role: string): string {
  const what = role === "editor" ? "can edit" : "can view";
  if (access === "members") return `Everyone signed in to Podium ${what}. It shows up under Workspace for them.`;
  if (access === "link")
    return `Anyone signed in to Podium who has the link ${what}. It isn't listed anywhere, and the link alone doesn't let in someone who isn't signed in.`;
  return "Only the people above can open this chat.";
}

/**
 * shareLink is the URL a link share hands out. The token is the k= query parameter; the
 * conversation reads it and passes it to StreamChat, which is where the server checks it.
 */
export function shareLink(origin: string, chatId: string, token: string): string {
  return `${origin.replace(/\/$/, "")}/agent/chat/${encodeURIComponent(chatId)}?k=${encodeURIComponent(token)}`;
}

/** sharedByLabel is what a shared chat's row says about how it got there. */
export function sharedByLabel(chat: Pick<Chat, "accessReason" | "sharedBy" | "myRole">): string {
  const view = chat.myRole === "viewer" ? " · view only" : "";
  if (chat.accessReason === "workspace") return `Workspace${view}`;
  if (chat.accessReason !== "shared") return "";
  if (chat.sharedBy === "link") return `Opened from a link${view}`;
  if (chat.sharedBy === "members") return `From Workspace${view}`;
  return `Shared by ${chat.sharedBy || "its owner"}${view}`;
}

/** unread is a chat's unread count as a number, for display. */
export function unread(chat: Pick<Chat, "unreadCount">): number {
  return Number(chat.unreadCount ?? 0n);
}
