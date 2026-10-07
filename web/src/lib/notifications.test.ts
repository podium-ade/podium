import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ProviderSettingsSchema } from "../gen/podium/agent/v1/agent_pb";
import {
  RESOLVED_KEEP,
  alertsFromProviders,
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
