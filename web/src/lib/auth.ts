// The local transport requires `Authorization: Bearer <PODIUM_LOCAL_TOKEN>` on every Connect
// endpoint, and a browser that just loaded index.html has no token. Rather than weakening the
// server or baking the secret into the served HTML, the UI asks for it once and keeps it in
// localStorage. Under `pnpm dev` the Vite proxy injects the header instead, so the token here
// stays empty and nothing is prompted. Under the tailnet transport identity comes from
// Tailscale's WhoIs, there is no token to hold, and TokenGate never shows this module's
// prompt at all.

const KEY = "podium.localToken";

let token = read();
const listeners = new Set<() => void>();

function read(): string {
  try {
    return globalThis.localStorage?.getItem(KEY) ?? "";
  } catch {
    return "";
  }
}

export function getToken(): string {
  return token;
}

export function setToken(next: string): void {
  token = next.trim();
  try {
    if (token) globalThis.localStorage?.setItem(KEY, token);
    else globalThis.localStorage?.removeItem(KEY);
  } catch {
    /* private mode: keep it in memory for this tab */
  }
  for (const fn of listeners) fn();
}

export function clearToken(): void {
  setToken("");
}

/** onRejected fires whenever the server answers Unauthenticated, so the shell can re-prompt. */
export function onRejected(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function notifyRejected(): void {
  for (const fn of listeners) fn();
}

/** Public /auth/status: whether this control plane offers Google Workspace sign-in. */
export type AuthStatus = {
  google: boolean;
  claimed: boolean;
  hosted_domain: string;
};

export async function fetchAuthStatus(): Promise<AuthStatus> {
  const res = await fetch("/auth/status", { headers: { Accept: "application/json" } });
  if (!res.ok) {
    return { google: false, claimed: false, hosted_domain: "" };
  }
  return (await res.json()) as AuthStatus;
}

export function authErrorMessage(code: string): string {
  switch (code) {
    case "denied":
      return "Google sign-in was cancelled.";
    case "no_workspace":
      return "Sign in with a Google Workspace account, not personal Gmail.";
    case "domain":
      return "This Podium belongs to a different Google Workspace.";
    default:
      return "Google sign-in failed. Try again.";
  }
}
