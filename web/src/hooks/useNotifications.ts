import { useEffect, useRef, useSyncExternalStore, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { agent } from "../lib/client";
import { useViewer } from "../lib/identity";
import { readChatSeen, writeChatSeen } from "../lib/chatSeen";
import { useToast } from "../components/Toast";
import {
  alertsFromChats,
  alertsFromInbox,
  alertsFromProviders,
  chatSeenBaseline,
  mergeNotifications,
  notificationServerSnapshot,
  notificationSnapshot,
  readNotifications,
  sameNotifications,
  subscribeNotifications,
  writeNotifications,
  type AppNotification,
} from "../lib/notifications";

/** useNotificationList is the log every screen reads. Recording happens in the provider. */
export function useNotificationList(): AppNotification[] {
  return useSyncExternalStore(
    subscribeNotifications,
    notificationSnapshot,
    notificationServerSnapshot,
  );
}

/**
 * NotificationsProvider records live conditions into the log while the app is open.
 * The log itself stays in this browser: it is a history of what this UI has shown,
 * not a second copy of conductor state.
 */
export function NotificationsProvider({ children }: { children: ReactNode }) {
  const viewer = useViewer();
  const settings = useQuery({
    queryKey: ["agent", "settings"],
    queryFn: () => agent.getSettings({}),
    enabled: viewer?.agentEnabled === true,
    refetchInterval: 60_000,
  });

  // Every chat, for the replies that arrived while nobody was looking at them.
  const chats = useQuery({
    queryKey: ["agent", "chats", "inbox"],
    queryFn: () => agent.listChats({}),
    enabled: viewer?.agentEnabled === true,
    refetchInterval: 60_000,
  });

  // The conductor's inbox: shares and mentions (phase 5). Polled more often than the rest,
  // because "Dana shared a chat with you" is the one thing here somebody is waiting on.
  const inbox = useQuery({
    queryKey: ["agent", "notifications"],
    queryFn: () => agent.listNotifications({ unreadOnly: true, limit: 50 }),
    enabled: viewer?.agentEnabled === true,
    refetchInterval: 15_000,
  });
  const toast = useToast();
  const toasted = useRef<Set<bigint> | null>(null);

  // A new share or mention is toasted once, the moment it first appears. The first read is
  // the baseline: opening Podium does not replay everything that happened while it was closed
  // (the bell has those). Whatever is about the chat on screen is marked read, which is what
  // resolves it in the bell on every browser.
  const refetchInbox = inbox.refetch;
  const marking = useRef(false);
  useEffect(() => {
    const rows = inbox.data?.notifications;
    if (!rows) return;
    const open = openChatId(window.location.pathname);
    const drafts = alertsFromInbox(rows, open);
    if (toasted.current === null) {
      toasted.current = new Set(rows.map((n) => n.id));
    } else {
      rows.forEach((n) => {
        if (toasted.current?.has(n.id)) return;
        toasted.current?.add(n.id);
        const draft = drafts.find((d) => d.key === `inbox:${n.id}`);
        if (draft) toast(draft.title, "ok");
      });
    }
    const seen = rows.filter((n) => n.chatId === open).map((n) => n.id);
    if (seen.length === 0 || marking.current) return;
    marking.current = true;
    void agent
      .markNotificationsRead({ ids: seen })
      .then(() => refetchInbox())
      .catch(() => {
        // The next poll tries again.
      })
      .finally(() => {
        marking.current = false;
      });
  }, [inbox.data, refetchInbox, toast]);

  useEffect(() => {
    if (!viewer?.agentEnabled || !settings.data) return;
    const list = chats.data?.chats ?? [];
    const seen = chatSeenBaseline(list, readChatSeen());
    writeChatSeen(seen);
    const open = openChatId(window.location.pathname);
    const now = Date.now();
    const current = [
      ...alertsFromProviders(settings.data.providers, now, "global"),
      ...alertsFromProviders(settings.data.personal, now, "personal"),
      ...alertsFromChats(list, seen, open),
      ...alertsFromInbox(inbox.data?.notifications ?? [], open),
    ];
    const stored = readNotifications();
    const merged = mergeNotifications(stored, current, Date.now());
    if (!sameNotifications(merged, stored)) writeNotifications(merged);
  }, [viewer?.agentEnabled, settings.data, chats.data, inbox.data]);

  return children;
}

/** openChatId is the chat on screen at path, if any: /agent/chat/:id or /agent/chat/a/:who/:id. */
function openChatId(path: string): string {
  const parts = path.replace(/^\/agent\/chat\/?/, "").split("/").filter(Boolean);
  if (!path.startsWith("/agent/chat")) return "";
  return parts[0] === "a" ? (parts[2] ?? "") : (parts[0] ?? "");
}
