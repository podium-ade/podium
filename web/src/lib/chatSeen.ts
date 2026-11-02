/**
 * When this browser last saw each chat's latest message, so a reply that arrives while nobody
 * is looking — a delegated task finishing an hour later — can say so in the inbox. Kept in this
 * browser only, like the notification log it feeds.
 */
const KEY = "podium.chatSeen.v1";

export type ChatSeen = Record<string, number>;

export function readChatSeen(): ChatSeen {
  try {
    const raw = window.localStorage.getItem(KEY);
    const parsed = raw ? (JSON.parse(raw) as unknown) : {};
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as ChatSeen) : {};
  } catch {
    return {};
  }
}

export function writeChatSeen(seen: ChatSeen): void {
  try {
    window.localStorage.setItem(KEY, JSON.stringify(seen));
  } catch {
    // Private mode or full storage: the inbox just will not know what was seen.
  }
}

/** markChatSeen records that the chat's message at `at` has been seen. It never moves back. */
export function markChatSeen(chatId: string, at: number): void {
  const seen = readChatSeen();
  if ((seen[chatId] ?? 0) >= at) return;
  seen[chatId] = at;
  writeChatSeen(seen);
}
