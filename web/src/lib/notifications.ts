import type { ProviderSettings } from "../gen/podium/agent/v1/agent_pb";
import { PROVIDERS } from "./agents";
import { oauthExpired } from "./providerCredential";

/** How many resolved alerts this browser keeps. Open ones are never dropped to make room. */
export const RESOLVED_KEEP = 50;

const STORAGE_KEY = "podium.notifications.v1";

/** NotificationLevel is the three looks the inbox renders.
 * alert: a person can fix it. standard: worth knowing, nothing to do.
 * error: Podium itself failed. Nothing raises an error yet. */
export type NotificationLevel = "error" | "alert" | "standard";

export const NOTIFICATION_LEVELS: readonly NotificationLevel[] = ["error", "alert", "standard"];

export type AppNotification = {
  id: string;
  /** key groups one ongoing condition, so a still-open alert is not recorded twice. */
  key: string;
  level: NotificationLevel;
  title: string;
  body: string;
  href: string;
  action: string;
  createdAt: number;
  readAt?: number;
  resolvedAt?: number;
};

export type AlertDraft = Omit<AppNotification, "id" | "createdAt" | "readAt" | "resolvedAt">;

/** alertsFromProviders is the live set. History is everything this browser has already recorded. */
export function alertsFromProviders(
  providers: readonly ProviderSettings[] | undefined,
  now = Date.now(),
): AlertDraft[] {
  const drafts: AlertDraft[] = [];
  for (const provider of PROVIDERS) {
    const row = providers?.find((p) => p.provider === provider.id);
    if (!oauthExpired(row, now)) continue;
    drafts.push({
      key: `provider-oauth-expired:${provider.id}`,
      level: "alert",
      title: `Sign in to ${provider.models} again`,
      body:
        "The subscription token expired and could not be renewed. Chats that use this model fail until you sign in again.",
      href: "/agent/settings/models",
      action: "Sign in again",
    });
  }
  return drafts;
}

/**
 * mergeNotifications folds the live set into the stored log.
 * An open row with the same key stays. A condition that cleared is marked resolved.
 * A condition that comes back after that is a new row, so the history keeps both.
 */
export function mergeNotifications(
  stored: readonly AppNotification[],
  current: readonly AlertDraft[],
  now: number,
): AppNotification[] {
  const next = stored.map((row) => ({ ...row }));
  const openKeys = new Set(current.map((alert) => alert.key));
  for (const alert of current) {
    const open = next.find((row) => row.key === alert.key && row.resolvedAt == null);
    if (open) {
      open.level = alert.level;
      open.title = alert.title;
      open.body = alert.body;
      open.href = alert.href;
      open.action = alert.action;
      continue;
    }
    next.push({ ...alert, id: `${alert.key}:${now}`, createdAt: now });
  }
  for (const row of next) {
    if (row.resolvedAt == null && !openKeys.has(row.key)) row.resolvedAt = now;
  }
  const unresolved = next.filter((row) => row.resolvedAt == null);
  const resolved = next
    .filter((row) => row.resolvedAt != null)
    .sort((a, b) => (b.resolvedAt ?? 0) - (a.resolvedAt ?? 0))
    .slice(0, RESOLVED_KEEP);
  return [...unresolved, ...resolved].sort((a, b) => b.createdAt - a.createdAt);
}

export function ago(at: number, now = Date.now()): string {
  const secs = Math.round((now - at) / 1000);
  if (secs < 10) return "just now";
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

type Listener = () => void;

const listeners = new Set<Listener>();
let cache: AppNotification[] | null = null;
const EMPTY: AppNotification[] = [];

function parseStored(raw: string | null): AppNotification[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(isNotification).map(withLevel);
  } catch {
    return [];
  }
}

function withLevel(row: AppNotification): AppNotification {
  if (row.level === "error" || row.level === "alert" || row.level === "standard") return row;
  return { ...row, level: "alert" };
}

function isNotification(value: unknown): value is AppNotification {
  if (!value || typeof value !== "object") return false;
  const row = value as AppNotification;
  return (
    typeof row.id === "string" &&
    typeof row.key === "string" &&
    typeof row.title === "string" &&
    typeof row.body === "string" &&
    typeof row.href === "string" &&
    typeof row.action === "string" &&
    typeof row.createdAt === "number"
  );
}

export function readNotifications(): AppNotification[] {
  if (cache) return cache;
  cache = parseStored(typeof localStorage === "undefined" ? null : localStorage.getItem(STORAGE_KEY));
  return cache;
}

export function writeNotifications(rows: AppNotification[]) {
  cache = rows;
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(rows));
  } catch {
    // A full or blocked store still shows the list for this page view.
  }
  for (const listener of listeners) listener();
}

export function subscribeNotifications(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function notificationSnapshot(): AppNotification[] {
  return readNotifications();
}

export function notificationServerSnapshot(): AppNotification[] {
  return EMPTY;
}

export function sameNotifications(a: readonly AppNotification[], b: readonly AppNotification[]): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/** resetNotificationsForTests drops the in-memory list and the browser store. */
export function resetNotificationsForTests() {
  cache = [];
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    // jsdom always has localStorage; ignore a host that does not.
  }
  for (const listener of listeners) listener();
}
