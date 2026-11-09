import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ChatSchema, NotificationSchema, ProviderSettingsSchema, type Chat } from "../gen/podium/agent/v1/agent_pb";
import {
  RESOLVED_KEEP,
  alertsFromChats,
  alertsFromInbox,
  alertsFromProviders,
  chatSeenBaseline,
  mergeNotifications,
  type AppNotification,
} from "./notifications";

const now = Date.parse("2026-10-05T20:00:00Z");

function expiredXai() {
  return create(ProviderSettingsSchema, {
    provider: "xai",
    keySet: true,
    authKind: "oauth",
    expiresAt: timestampFromDate(new Date(now - 1000)),
  });
}

function row(over: Partial<AppNotification> = {}): AppNotification {
  return {
    id: "provider-oauth-expired:xai:1",
    key: "provider-oauth-expired:xai",
    level: "alert",
    title: "Sign in to Grok again",
    body: "expired",
    href: "/agent/settings/models",
    action: "Sign in again",
    createdAt: now - 10_000,
    ...over,
  };
}

describe("alertsFromProviders", () => {
  it("warns for a subscription whose access token is already past", () => {
    const alerts = alertsFromProviders([expiredXai()], now);
    expect(alerts).toHaveLength(1);
    expect(alerts[0]?.key).toBe("provider-oauth-expired:xai");
    expect(alerts[0]?.href).toBe("/agent/settings/models");
    expect(alertsFromProviders([], now)).toEqual([]);
  });
});

describe("mergeNotifications", () => {
  it("records an open alert once, then resolves it when the condition clears", () => {
    const first = mergeNotifications([], alertsFromProviders([expiredXai()], now), now);
    expect(first).toHaveLength(1);
    expect(first[0]?.resolvedAt).toBeUndefined();

    const again = mergeNotifications(first, alertsFromProviders([expiredXai()], now), now + 1000);
    expect(again).toHaveLength(1);
    expect(again[0]?.id).toBe(first[0]?.id);

    const cleared = mergeNotifications(again, [], now + 2000);
    expect(cleared[0]?.resolvedAt).toBe(now + 2000);
  });

  it("starts a new row when the same condition comes back", () => {
    const resolved = row({ resolvedAt: now - 500 });
    const next = mergeNotifications([resolved], alertsFromProviders([expiredXai()], now), now);
    expect(next).toHaveLength(2);
    expect(next.filter((item) => item.resolvedAt == null)).toHaveLength(1);
  });

  it("keeps every open alert and only the newest resolved ones", () => {
    const resolved = Array.from({ length: RESOLVED_KEEP + 5 }, (_, i) =>
      row({
        id: `old:${i}`,
        key: `old:${i}`,
        createdAt: i,
        resolvedAt: i,
      }),
    );
    const open = row({ id: "open", key: "open", createdAt: now, resolvedAt: undefined });
    const next = mergeNotifications([open, ...resolved], [], now);
    expect(next.some((item) => item.id === "open")).toBe(true);
    expect(next.filter((item) => item.resolvedAt != null)).toHaveLength(RESOLVED_KEEP);
  });
});

function chat(id: string, lastMs: number | null, extra: { turnRunning?: boolean } = {}): Chat {
  return create(ChatSchema, {
    id,
    title: `Chat ${id}`,
    preview: `last words of ${id}`,
    lastMessageAt:
      lastMs == null ? undefined : { seconds: BigInt(Math.floor(lastMs / 1000)), nanos: (lastMs % 1000) * 1e6 },
    ...extra,
  });
}

describe("alertsFromChats", () => {
  it("announces a reply that arrived after the chat was last seen, once per reply", () => {
    const seen = { a: 1_000, b: 5_000 };
    const drafts = alertsFromChats([chat("a", 2_000), chat("b", 5_000)], seen, "");
    expect(drafts).toHaveLength(1);
    expect(drafts[0]).toMatchObject({ key: "chat-reply:a:2000", href: "/agent/chat/a", body: "last words of a" });
  });

  it("leaves out the chat on screen, one still answering, and one never seen", () => {
    const seen = { a: 1_000, b: 1_000 };
    expect(alertsFromChats([chat("a", 2_000)], seen, "a")).toEqual([]);
    expect(alertsFromChats([chat("b", 2_000, { turnRunning: true })], seen, "")).toEqual([]);
    expect(alertsFromChats([chat("c", 2_000)], seen, "")).toEqual([]);
  });

  it("counts a chat this browser has never seen as seen, so nothing old is announced", () => {
    const seen = chatSeenBaseline([chat("a", 3_000), chat("b", null)], { c: 9 });
    expect(seen).toEqual({ a: 3_000, b: 0, c: 9 });
  });
});

describe("alertsFromInbox", () => {
  it("says a paused routine is the owner's to look at, why, and who paused it", () => {
    const rows = [
      create(NotificationSchema, {
        id: 7n, kind: "routine", chatId: "chat_r", chatTitle: "PR digest", actor: "alice", detail: "Paused by an admin.",
      }),
      create(NotificationSchema, {
        id: 8n, kind: "routine", chatId: "chat_s", chatTitle: "Stale branches",
        detail: "5 runs failed within an hour. Look at the last one, then resume it.",
      }),
    ];
    const [byAdmin, byPodium] = alertsFromInbox(rows, "");
    expect(byAdmin).toMatchObject({
      level: "alert",
      title: "“PR digest” was paused",
      body: "Paused by an admin. By alice.",
      href: "/agent/chat/chat_r",
      action: "Open routine",
    });
    expect(byPodium?.body).toBe("5 runs failed within an hour. Look at the last one, then resume it.");
  });

  it("leaves out a routine notice that was read, or whose chat is open", () => {
    const row = create(NotificationSchema, { id: 7n, kind: "routine", chatId: "chat_r", detail: "x" });
    expect(alertsFromInbox([{ ...row, read: true }], "")).toEqual([]);
    expect(alertsFromInbox([row], "chat_r")).toEqual([]);
  });
});
