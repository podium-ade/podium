import type { ProviderSettings } from "../gen/podium/agent/v1/agent_pb";

/** oauthExpired is true when a subscription access token is past the time the conductor
 * renews it. A time in the past means renewal has been failing, and turns on that
 * model fail until somebody signs in again. An API key does not expire. */
export function oauthExpired(settings?: ProviderSettings, now = Date.now()): boolean {
  if (!settings?.keySet || settings.authKind !== "oauth") return false;
  const seconds = Number(settings.expiresAt?.seconds ?? 0);
  if (!Number.isFinite(seconds) || seconds <= 0) return false;
  return seconds * 1000 <= now;
}

/** keyRefused is true when the provider refused a stored API key the last time the conductor
 * checked it. Turns that would spend it are stopped before they start until a working key is
 * saved, which clears it. */
export function keyRefused(settings?: ProviderSettings): boolean {
  return Boolean(settings?.keySet && settings.authKind !== "oauth" && settings.refusedAt);
}
