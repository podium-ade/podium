import type { Entry } from "./entries";

/**
 * PullRequest is one GitHub pull request, described by nothing but its own URL — the same
 * shape the conductor gives a chat's links (internal/agent/conductor/pullrequest.go).
 */
export type PullRequest = { url: string; owner: string; repo: string; number: number };

/** Nine digits is generous for a pull-request number and stops a long run of them parsing. */
const MAX_DIGITS = 9;

// The whole URL and nothing looser, as the conductor matches it: a bare "#123" is ambiguous
// across repositories, and /pull/ on github.com is the only shape that says one thing.
const PULL_REQUEST_RE = /https:\/\/github\.com\/([A-Za-z0-9][A-Za-z0-9-]*)\/([A-Za-z0-9_.-]+)\/pull\/([0-9]+)/g;

/** findPullRequests returns every pull request named in text, in order, each one once. */
export function findPullRequests(text: string): PullRequest[] {
  const out: PullRequest[] = [];
  const seen = new Set<string>();
  for (const m of text.matchAll(PULL_REQUEST_RE)) {
    const [, owner, rawRepo, digits] = m;
    // A sentence that ends on the link leaves its full stop on the repository name.
    const repo = rawRepo.replace(/\.+$/, "");
    if (!repo || digits.length > MAX_DIGITS) continue;
    const number = Number(digits);
    if (number <= 0) continue;
    const url = `https://github.com/${owner}/${repo}/pull/${number}`;
    if (seen.has(url)) continue;
    seen.add(url);
    out.push({ url, owner, repo, number });
  }
  return out;
}

/**
 * taskPullRequests is the pull requests a task's agent named in its own words — its
 * progress notes, questions and answer. Tool output and logs are left out: a `gh pr list`
 * prints every pull request in the repository, and none of them is this task's.
 */
export function taskPullRequests(entries: readonly Entry[]): PullRequest[] {
  const text = entries
    .filter((e) => e.kind === "message" && e.message && e.message.type !== "accounting")
    .map((e) => e.message!.text)
    .join("\n");
  return findPullRequests(text);
}
