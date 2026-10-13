/**
 * GITHUB_CALLBACK_PATH is where GitHub sends the browser back after a person connects their
 * account. The conductor accepts no other path. Change it here and in
 * internal/agent/api/githubaccount.go together.
 */
export const GITHUB_CALLBACK_PATH = "/agent/github/callback";

export function githubCallbackURL(): string {
  return window.location.origin + GITHUB_CALLBACK_PATH;
}

const PENDING_KEY = "podium.github.oauth.pending";

export type PendingGitHubOAuth = { flowId: string; state: string };

export function rememberGitHubPending(p: PendingGitHubOAuth) {
  try {
    sessionStorage.setItem(PENDING_KEY, JSON.stringify(p));
  } catch {
    // Without storage the callback cannot name its flow, and the person starts again.
  }
}

export function takeGitHubPending(): PendingGitHubOAuth | undefined {
  try {
    const raw = sessionStorage.getItem(PENDING_KEY);
    sessionStorage.removeItem(PENDING_KEY);
    return raw ? (JSON.parse(raw) as PendingGitHubOAuth) : undefined;
  } catch {
    return undefined;
  }
}
