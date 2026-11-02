import { useEffect, useSyncExternalStore, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { agent } from "../lib/client";
import { useViewer } from "../lib/identity";
import { readChatSeen, writeChatSeen } from "../lib/chatSeen";
import {
  alertsFromChats,
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

  useEffect(() => {
    if (!viewer?.agentEnabled || !settings.data) return;
    const list = chats.data?.chats ?? [];
    const seen = chatSeenBaseline(list, readChatSeen());
    writeChatSeen(seen);
    const open = openChatId(window.location.pathname);
    const current = [...alertsFromProviders(settings.data.providers), ...alertsFromChats(list, seen, open)];
    const stored = readNotifications();
    const merged = mergeNotifications(stored, current, Date.now());
    if (!sameNotifications(merged, stored)) writeNotifications(merged);
  }, [viewer?.agentEnabled, settings.data, chats.data]);

  return children;
}

/** openChatId is the chat on screen at path, if any: /agent/chat/:id or /agent/chat/a/:who/:id. */
function openChatId(path: string): string {
  const parts = path.replace(/^\/agent\/chat\/?/, "").split("/").filter(Boolean);
  if (!path.startsWith("/agent/chat")) return "";
  return parts[0] === "a" ? (parts[2] ?? "") : (parts[0] ?? "");
}
