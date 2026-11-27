import { type Routine, type RoutineDraft } from "../gen/podium/agent/v1/agent_pb";
import { toDate } from "./format";

/** routineKey is where one chat's routine is cached. */
export const routineKey = (chatId: string) => ["agent", "routine", chatId];

/** routinesKey is where the viewer's list of routines is cached. */
export const routinesKey = ["agent", "routines"];

/** draftOf is what the editor starts from for an existing routine. */
export function draftOf(r: Routine): Partial<RoutineDraft> {
  return {
    name: r.name,
    schedule: r.schedule,
    timezone: r.timezone,
    instruction: r.instruction,
    notes: r.notes,
    personalityId: r.personalityId,
    model: r.model,
    effort: r.effort,
    dailyCapUsd: r.dailyCapUsd,
    trigger: r.trigger,
    events: r.events,
    teams: r.teams,
    chatMode: r.chatMode,
    playbook: r.playbook,
    githubFilters: r.githubFilters,
  };
}

/** Trigger is what starts a routine's runs: its schedule, Linear events, or GitHub events. */
export type Trigger = "schedule" | "linear" | "github";

/**
 * ChatMode is where a routine on events runs: all in its own chat, or in each ticket's or pull
 * request's conversation — the one every agent session on a Linear issue is a turn of, or the
 * chat a pull request's reviews are in.
 */
export type ChatMode = "routine" | "subject";

/**
 * perTicket is a routine on events whose runs each happen in the conversation of what they are
 * about: a Linear ticket's, or a pull request's.
 */
export function perTicket(r: Pick<Routine, "trigger" | "chatMode">): boolean {
  return onEvents(r) && r.chatMode === "subject";
}

/** perTicketText says where such a routine's runs are, after what it runs on: "in each pull request’s chat". */
export function perTicketText(r: Pick<Routine, "trigger">): string {
  return isGitHub(r) ? "in each pull request’s chat" : "in each ticket’s conversation";
}

/** isLinear is a routine that runs on Linear events instead of a schedule. */
export function isLinear(r: Pick<Routine, "trigger">): boolean {
  return r.trigger === "linear";
}

/** isGitHub is a routine that runs on events the GitHub App's webhook delivers instead of a schedule. */
export function isGitHub(r: Pick<Routine, "trigger">): boolean {
  return r.trigger === "github";
}

/**
 * onEvents is a routine that runs when its events arrive, from Linear or GitHub: it has no
 * schedule, no next run and nothing to run by hand, and what it has instead is the events waiting.
 */
export function onEvents(r: Pick<Routine, "trigger">): boolean {
  return isLinear(r) || isGitHub(r);
}

/** LINEAR_TYPES are the Linear entities a routine can run on, as the server lists them, with their names in words. */
export const LINEAR_TYPES: { type: string; label: string }[] = [
  { type: "Issue", label: "Issue" },
  { type: "Comment", label: "Comment" },
  { type: "Project", label: "Project" },
  { type: "ProjectUpdate", label: "Project update" },
  { type: "Cycle", label: "Cycle" },
  { type: "IssueLabel", label: "Issue label" },
];

/** LINEAR_ACTIONS are what can happen to one, as Linear names it, with the word a label uses. */
export const LINEAR_ACTIONS: { action: string; label: string }[] = [
  { action: "create", label: "created" },
  { action: "update", label: "updated" },
  { action: "remove", label: "removed" },
];

/** LINEAR_EVENTS is every event a routine can run on, "Issue.create" each, with a plain label: "Issue created". */
export const LINEAR_EVENTS: { event: string; label: string }[] = LINEAR_TYPES.flatMap((t) =>
  LINEAR_ACTIONS.map((a) => ({ event: `${t.type}.${a.action}`, label: `${t.label} ${a.label}` })),
);

/** teamKeys reads the Teams field: comma- or space-separated keys, upper case, each once. */
export function teamKeys(text: string): string[] {
  const keys = text
    .split(/[\s,]+/)
    .map((k) => k.trim().toUpperCase())
    .filter((k) => k !== "");
  return [...new Set(keys)];
}

/**
 * GitHubGroup is a kind of GitHub event, as the editor lists them: CI results, pull requests, and
 * issues.
 */
export type GitHubGroup = "CI" | "Pull requests" | "Issues";

/**
 * GITHUB_EVENTS is every GitHub event a routine can run on, as the server names it, with a plain
 * label, the shorter one its row in the editor shows under its group, and its group. A hint says
 * more where the label alone could be misread. The CI ones are also rows of GITHUB_CI, which is how
 * the editor shows them.
 */
export const GITHUB_EVENTS: { event: string; label: string; short?: string; group: GitHubGroup; hint?: string }[] = [
  { event: "checks.completed", label: "All checks finished", group: "CI" },
  { event: "checks.failed", label: "All checks finished, at least one failed", group: "CI" },
  {
    event: "workflow_run.failed",
    label: "Workflow run failed",
    group: "CI",
    hint: "Failed, timed out or could not start.",
  },
  { event: "workflow_run.completed", label: "Workflow run completed", group: "CI" },
  { event: "check_suite.failed", label: "Check suite failed", group: "CI" },
  { event: "check_suite.completed", label: "Check suite completed", group: "CI" },
  { event: "pull_request.opened", label: "Pull request opened", short: "Opened", group: "Pull requests" },
  {
    event: "pull_request.ready_for_review",
    label: "Pull request ready for review",
    short: "Ready for review",
    group: "Pull requests",
  },
  {
    event: "pull_request.synchronize",
    label: "Pull request got new commits",
    short: "Got new commits",
    group: "Pull requests",
  },
  { event: "pull_request.merged", label: "Pull request merged", short: "Merged", group: "Pull requests" },
  { event: "pull_request.closed", label: "Pull request closed", short: "Closed", group: "Pull requests" },
  {
    event: "pull_request_review.submitted",
    label: "Review submitted",
    short: "A review submitted",
    group: "Pull requests",
  },
  { event: "issues.opened", label: "Issue opened", short: "Opened", group: "Issues" },
];

/**
 * CIRow is one thing CI finishes, as the editor's table shows it: a routine runs when it
 * finishes whatever the result, or only when it failed — one of the two, never both.
 */
export type CIRow = { label: string; hint: string; completed: string; failed: string };

/** GITHUB_CI is the CI rows of the GitHub events table, the one a CI routine wants first. */
export const GITHUB_CI: CIRow[] = [
  {
    label: "All checks",
    hint: "Once per commit, when every workflow, check and status on it is done.",
    completed: "checks.completed",
    failed: "checks.failed",
  },
  {
    label: "Workflow run",
    hint: "GitHub Actions, once per workflow.",
    completed: "workflow_run.completed",
    failed: "workflow_run.failed",
  },
  {
    label: "Check suite",
    hint: "Any CI app, once per suite. Actions makes one per workflow run.",
    completed: "check_suite.completed",
    failed: "check_suite.failed",
  },
];

/** CIOutcome is what a CI row runs on: nothing, every finish, or only a failed one. */
export type CIOutcome = "off" | "finished" | "failed";

/** ciOutcome reads a CI row from events. Asking for both is every finish, since a failure is one. */
export function ciOutcome(events: string[], row: CIRow): CIOutcome {
  if (events.includes(row.completed)) return "finished";
  if (events.includes(row.failed)) return "failed";
  return "off";
}

/** withOutcome is events with a CI row set to an outcome, in GITHUB_EVENTS order. */
export function withOutcome(events: string[], row: CIRow, to: CIOutcome): string[] {
  const set = new Set(events.filter((e) => e !== row.completed && e !== row.failed));
  if (to === "finished") set.add(row.completed);
  if (to === "failed") set.add(row.failed);
  return GITHUB_EVENTS.map((e) => e.event).filter((e) => set.has(e));
}

/** GITHUB_GROUPS are the groups of GITHUB_EVENTS, in the order the editor shows them. */
export const GITHUB_GROUPS: GitHubGroup[] = ["CI", "Pull requests", "Issues"];

/**
 * repoNames reads the Repositories field: owner/name or a whole owner each, comma- or
 * space-separated, lower case, each once.
 */
export function repoNames(text: string): string[] {
  const names = text
    .split(/[\s,]+/)
    .map((n) => n.trim().toLowerCase())
    .filter((n) => n !== "");
  return [...new Set(names)];
}

/**
 * runsText says when a routine runs: "Runs every day at 09:00", "Runs on Linear: issue created (ENG)",
 * "Runs on GitHub: workflow run failed (acme/api)".
 */
export function runsText(r: Pick<Routine, "trigger" | "scheduleText">): string {
  if (onEvents(r)) {
    return `Runs ${r.scheduleText.charAt(0).toLowerCase()}${r.scheduleText.slice(1)}`;
  }
  return `Runs ${r.scheduleText}`;
}

/** waitingText is how many events wait for a routine's next run, in words. */
export function waitingText(n: number): string {
  if (n <= 0) return "none";
  return n === 1 ? "1 event" : `${n} events`;
}

/** Scope is who a routine runs as: a person, or nobody (a system routine admins set up). */
export type Scope = "personal" | "system";

/** isSystem is a routine that runs as nobody, configured by admins. */
export function isSystem(r: Pick<Routine, "scope">): boolean {
  return r.scope === "system";
}

/**
 * CI_FIXER is the system routine phase 2 is proven on: it starts out only proposing, and an
 * admin lets it act by editing the instruction. The guard rails are written into it.
 */
export const CI_FIXER: Pick<RoutineDraft, "name" | "instruction" | "notes" | "schedule"> = {
  name: "Fix failing CI",
  schedule: "*/10 * * * *",
  instruction: [
    "Look at the open pull requests in [name the repositories, like \"the API and the web app\"] for failed CI checks.",
    "",
    "For each pull request whose latest commit has a failed check:",
    "1. Skip it if the same check is also failing on its base branch — the fault is not in the pull request. Say so once.",
    "2. Skip it if the state shows you already handled this commit (key: owner/repo#number@sha).",
    "3. Skip it, and say you are giving up, if the state shows 3 attempts on this pull request already.",
    "4. Read the failed job's log and work out the smallest change that fixes it.",
    "5. Propose it: comment on the pull request with what failed, why, and the change you would make. Do not push anything.",
    "",
    "Record every commit you handled and the attempts per pull request in the state. Finish idle when nothing failed since the last run.",
  ].join("\n"),
  notes: "- Never fix CI by disabling a lint rule or skipping a test.",
};

/**
 * CI_FIXER_ON_GITHUB is CI_FIXER for a routine on GitHub events: instead of looking for failed
 * checks every few minutes, each run is given the commits whose checks all finished with one failed.
 * The guard rails are the same.
 */
export const CI_FIXER_ON_GITHUB: Pick<RoutineDraft, "name" | "instruction" | "notes" | "events"> = {
  name: "Fix failing CI",
  events: ["checks.failed"],
  instruction: [
    "Each event in this run is a commit whose checks have all finished, at least one of them failed. It lists the failed checks, with the job to read each GitHub Actions one's log by.",
    "",
    "For each one on a pull request:",
    "1. Skip it if the commit is no longer the pull request's head — a newer push is being checked.",
    "2. Skip a check that also fails on the pull request's base branch — the fault is not in the pull request. Say so once.",
    "3. Skip it if the state shows you already handled this commit (key: owner/repo#number@sha).",
    "4. Skip it, and say you are giving up, if the state shows 3 attempts on this pull request already.",
    "5. Read the failed jobs' logs and work out the smallest change that fixes them.",
    "6. Propose it: comment on the pull request with what failed, why, and the change you would make. Do not push anything.",
    "",
    "A failure on no pull request — a push to a branch — is reported, and nothing else is done about it.",
    "",
    "Record every commit you handled and the attempts per pull request in the state. Finish idle when every event was already handled.",
  ].join("\n"),
  notes: CI_FIXER.notes,
};

/**
 * REVIEW_PLAYBOOK, as a routine's playbook, is the reviewer of the pull request's repository: the
 * playbook naming it in github_repos, else the github: true one.
 */
export const REVIEW_PLAYBOOK = "@review";

/**
 * REVIEW_ON_GITHUB is the "Review pull requests" template: the review the GitHub App started on its
 * own when a pull request opened, as a routine. It starts the repository's review playbook right away, with
 * no assistant's turn before it, and leaves drafts alone until they are marked ready. Its
 * instruction is the one a deployment that was reviewing before was given its routine with
 * (connections.ReviewRoutineInstruction).
 */
export const REVIEW_ON_GITHUB: Pick<RoutineDraft, "name" | "instruction" | "events" | "playbook"> & {
  skipDrafts: boolean;
} = {
  name: "Review pull requests",
  instruction:
    "Review this pull request: what it changes, whether it does what it says, and what should be fixed before it merges. Post the review on the pull request yourself with `gh pr review` (--approve, --request-changes or --comment), and put a finding about one place in the diff on that line (`gh api repos/{owner}/{repo}/pulls/{number}/reviews` with comments). If you were asked to review it before, look only at what changed since your last review.",
  events: ["pull_request.opened", "pull_request.ready_for_review"],
  playbook: REVIEW_PLAYBOOK,
  skipDrafts: true,
};

/** REVIEW_PLAYBOOK_LABEL names REVIEW_PLAYBOOK: the playbook set to review the pull request's repository. */
export const REVIEW_PLAYBOOK_LABEL = "The repository’s review playbook";

/** playbookLabel names a routine's playbook the way the dialog offers it. */
export function playbookLabel(playbook: string): string {
  if (!playbook) return "The assistant";
  if (playbook === REVIEW_PLAYBOOK) return REVIEW_PLAYBOOK_LABEL;
  return playbook;
}

/** routineChatPath is the address of a routine's chat, under its assistant when it has one. */
export function routineChatPath(r: Pick<Routine, "personalityId" | "chatId">): string {
  return r.personalityId ? `/agent/chat/a/${r.personalityId}/${r.chatId}` : `/agent/chat/${r.chatId}`;
}

/**
 * routinePath is where a routine is opened: its chat, or — for a routine on events that has none,
 * because it runs in each ticket's conversation or has not run yet — its own page.
 */
export function routinePath(r: Pick<Routine, "id" | "personalityId" | "chatId">): string {
  return r.chatId ? routineChatPath(r) : `/agent/routines/${r.id}`;
}

/** routineByIdKey is where a routine read by its id, for its own page, is cached. */
export const routineByIdKey = (id: string) => ["agent", "routine", "id", id];

/** upcoming says a future moment the way a person reads one: "today 09:00", "Tue 09:00", "Nov 18 09:00". */
export function upcoming(ts: Parameters<typeof toDate>[0], now = new Date()): string {
  const d = toDate(ts);
  if (!d) return "—";
  // 24-hour, as the schedule itself is said ("weekdays at 09:00"), so the two read alike.
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
  const days = Math.floor((new Date(d).setHours(0, 0, 0, 0) - new Date(now).setHours(0, 0, 0, 0)) / 86_400_000);
  if (days <= 0) return `today ${time}`;
  if (days === 1) return `tomorrow ${time}`;
  if (days < 7) return `${d.toLocaleDateString(undefined, { weekday: "short" })} ${time}`;
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${time}`;
}

/** money is a small spend, in dollars. */
export function money(usd: number): string {
  if (usd <= 0) return "$0";
  return usd < 0.01 ? "<$0.01" : `$${usd.toFixed(2)}`;
}

/** routineStatus is a routine's state in a few words: running, its next run, or why it is paused. */
export function routineStatus(r: Routine): string {
  if (r.running) return "Running now";
  if (r.enabled && onEvents(r)) {
    if (r.pendingEvents > 0) return `${waitingText(r.pendingEvents)} waiting`;
    return isGitHub(r) ? "Waiting for GitHub events" : "Waiting for Linear events";
  }
  if (r.enabled) return `Next ${upcoming(r.nextRunAt)}`;
  if (r.pausedUntil) return `Paused until ${upcoming(r.pausedUntil)}`;
  return r.pausedReason ? "Paused by Podium" : "Paused";
}

/** browserZone is where the person is, which is what most routines mean by "09:00". */
export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** NewRoutine is what the new-routine dialog opens on. */
export type NewRoutine = { title: string; initial: Partial<RoutineDraft>; scope?: Scope };

export const BLANK_ROUTINE: NewRoutine = { title: "New routine", initial: {} };

/**
 * duplicateOf is a new routine prefilled from r: everything it does, named as a copy, running as
 * r does. Only an admin may make a system routine, so anybody else's copy runs as themselves.
 * Nothing r has done comes along: its runs, its chat and what it kept stay with it.
 */
export function duplicateOf(r: Routine, admin: boolean): NewRoutine {
  return {
    title: "Duplicate routine",
    initial: { ...draftOf(r), name: `${r.name} (copy)` },
    scope: admin && isSystem(r) ? "system" : "personal",
  };
}
