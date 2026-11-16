import { useQuery } from "@tanstack/react-query";
import type { ChatPullRequest, LinearIssue } from "../gen/podium/agent/v1/agent_pb";
import type { Tone } from "../components/Badge";
import { agent } from "./client";

/**
 * Linear issues named in a chat (docs/agent.md, "Tickets in a chat").
 *
 * This file finds CANDIDATES: anything shaped like an identifier ("EME-6") or a link to an issue
 * in Linear. Which of them are issues at all, and which the viewer may see, is the conductor's to
 * say — it matches them against the workspace's team keys, so UTF-8 is never asked of Linear, and
 * answers only for issues the viewer could see in Linear. A candidate it says nothing about stays
 * plain text, so a chip never appears for something that is not there.
 */

/** IssueMatch is one candidate in a run of text: where it is, and the ref to ask for. */
export type IssueMatch = { start: number; end: number; ref: string };

/** A link to an issue in Linear, with or without the title slug after it. */
const ISSUE_URL = /^https?:\/\/linear\.app\/[a-z0-9_-]+\/issue\/([a-z][a-z0-9]{0,9}-[1-9][0-9]{0,6})(?:[/?#].*)?$/i;

/**
 * An identifier: an upper-case team key of up to ten letters and digits starting with a letter, a
 * hyphen, and a number with no leading zero. Lower case is not matched: Linear's keys are upper
 * case, and "covid-19" is nobody's ticket.
 */
const IDENTIFIER = /[A-Z][A-Z0-9]{0,9}-[1-9][0-9]{0,6}/g;

const WORD = /[\p{L}\p{N}_]/u;

/**
 * findIssueRefs is every candidate identifier in text that stands on its own: not inside a word,
 * a path or a version ("v1-2", "/EME-6", "EME-6.1"), and not glued to another token by a hyphen
 * ("EME-6-x"). The conductor applies the same rule to a message it briefs the assistant with
 * (internal/agent/linear/refs.go).
 */
export function findIssueRefs(text: string): IssueMatch[] {
  const out: IssueMatch[] = [];
  for (const m of text.matchAll(IDENTIFIER)) {
    const start = m.index;
    const end = start + m[0].length;
    if (!standsAlone(text, start, end)) continue;
    out.push({ start, end, ref: m[0] });
  }
  return out;
}

function standsAlone(text: string, start: number, end: number): boolean {
  const before = start > 0 ? text[start - 1] : "";
  if (before !== "" && (WORD.test(before) || "-/.@#=".includes(before))) return false;
  const after = end < text.length ? text[end] : "";
  if (after === "") return true;
  if (WORD.test(after)) return false;
  const next = end + 1 < text.length ? text[end + 1] : "";
  return !("-./".includes(after) && next !== "" && WORD.test(next));
}

/** issueRefOfUrl is the identifier a linear.app issue link names, upper-cased, or undefined. */
export function issueRefOfUrl(href: string | undefined): string | undefined {
  if (!href) return undefined;
  const m = ISSUE_URL.exec(href.trim());
  return m ? m[1].toUpperCase() : undefined;
}

/** STATE_TONE is the colour of a Linear workflow state's kind, in the app's status tones. */
const STATE_TONE: Record<string, Tone> = {
  triage: "lost",
  backlog: "idle",
  unstarted: "idle",
  started: "warn",
  completed: "ok",
  canceled: "idle",
};

export function stateTone(stateType: string): Tone {
  return STATE_TONE[stateType] ?? "idle";
}

/** BATCH_MS is how long lookups are collected before they are asked for together. */
const BATCH_MS = 15;
/** MAX_BATCH is the most refs the conductor answers at once. */
const MAX_BATCH = 25;

type Waiter = { resolve: (i: LinearIssue | null) => void; reject: (e: unknown) => void };

const queues = new Map<boolean, Map<string, Waiter[]>>();
const timers = new Map<boolean, ReturnType<typeof setTimeout>>();

/**
 * fetchLinearIssue is one issue, or null for one there is none of or the viewer may not see. The
 * chips of a whole transcript arrive at once, so they are asked for together: every lookup made
 * within BATCH_MS is one request (several, past MAX_BATCH).
 */
export function fetchLinearIssue(ref: string, detail: boolean): Promise<LinearIssue | null> {
  return new Promise((resolve, reject) => {
    let queue = queues.get(detail);
    if (!queue) {
      queue = new Map();
      queues.set(detail, queue);
    }
    const waiting = queue.get(ref) ?? [];
    waiting.push({ resolve, reject });
    queue.set(ref, waiting);
    if (!timers.has(detail)) {
      timers.set(
        detail,
        setTimeout(() => flush(detail), BATCH_MS),
      );
    }
  });
}

function flush(detail: boolean) {
  timers.delete(detail);
  const queue = queues.get(detail);
  queues.delete(detail);
  if (!queue) return;
  const refs = [...queue.keys()];
  for (let i = 0; i < refs.length; i += MAX_BATCH) {
    const batch = refs.slice(i, i + MAX_BATCH);
    // Through a promise, so a client that cannot ask at all fails the lookups rather than the timer.
    Promise.resolve()
      .then(() => agent.getLinearIssues({ refs: batch, detail }))
      .then((res) => {
        const byRef = new Map(res.issues.map((issue) => [issue.ref, issue] as const));
        for (const ref of batch) {
          for (const w of queue.get(ref) ?? []) w.resolve(byRef.get(ref) ?? null);
        }
      })
      .catch((err: unknown) => {
        for (const ref of batch) {
          for (const w of queue.get(ref) ?? []) w.reject(err);
        }
      });
  }
}

/** linearIssueKey is where one issue is cached. */
export const linearIssueKey = (ref: string, detail: boolean) => ["agent", "linear-issue", ref, detail];

/**
 * useLinearIssue is one issue for a chip (detail false: what the webhooks said is enough) or its
 * card (detail true: Linear now). null is no issue the viewer may see.
 */
export function useLinearIssue(ref: string, detail: boolean, enabled = true) {
  return useQuery({
    queryKey: linearIssueKey(ref, detail),
    queryFn: () => fetchLinearIssue(ref, detail),
    enabled,
    staleTime: 60_000,
    retry: false,
  });
}

/** PullLink is a pull request link, from Linear's attachments or the chat's own, by its URL. */
export type PullLink = { url: string; label: string; from: "linear" | "chat" };

/**
 * pullLinks is the pull requests linked to the issue in Linear and the ones the conversation
 * produced, each once: a link Linear has and the chat found in an answer is the same pull request.
 */
export function pullLinks(
  linear: { url: string; title: string; source: string }[],
  chat: ChatPullRequest[],
): PullLink[] {
  const out: PullLink[] = [];
  const seen = new Set<string>();
  const norm = (u: string) => u.replace(/\/+$/, "").toLowerCase();
  for (const pr of chat) {
    const key = norm(pr.url);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({ url: pr.url, label: `${pr.owner}/${pr.repo}#${pr.number}`, from: "chat" });
  }
  for (const l of linear) {
    if (!/github\.com\/[^/]+\/[^/]+\/pull\/\d+/i.test(l.url) && l.source !== "github") continue;
    const key = norm(l.url);
    if (seen.has(key)) continue;
    seen.add(key);
    const m = /github\.com\/([^/]+)\/([^/]+)\/pull\/(\d+)/i.exec(l.url);
    out.push({ url: l.url, label: m ? `${m[1]}/${m[2]}#${m[3]}` : l.title || l.url, from: "linear" });
  }
  return out;
}
