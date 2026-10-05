// Shallow clones for the turn. The token reaches git through a credential helper that reads
// the environment at the moment git asks for it, so it never appears in an argv, in
// .git/config, or in a log line. Redaction on the way out is defence in depth, not the
// control.

import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { join } from "node:path";

import type { RepoRef } from "./brief.js";

/** WorkspaceDir is the task's volume: one directory per repo goes under it. */
export const WorkspaceDir = "/workspace";

/**
 * workspaceOf is where this turn works.
 *
 * For a task it is WorkspaceDir, the volume the node mounted. For a HOST turn there is no
 * such directory — the conductor forked this process in a jail of its own and made that the
 * working directory — so it is process.cwd(). Getting this wrong is not subtle: the harness
 * is given the workspace as `--dir` and refuses to start with "Failed to change directory
 * to /workspace", which is a turn that dies before its first request.
 */
export function workspaceOf(brief: { runs_on?: string }, cwd = process.cwd()): string {
  return brief.runs_on === "host" ? cwd : WorkspaceDir;
}

/** artifactsUnder is the artifacts directory of one workspace. */
export function artifactsUnder(root: string): string {
  return `${root}/.podium/artifacts`;
}

/** CloneDepth is how much history a turn gets. Enough to branch, rebase and read blame. */
export const CloneDepth = 50;

/** TokenEnv is the environment variable the credential helper reads. */
export const TokenEnv = "GITHUB_TOKEN";

/**
 * GitUserName and GitUserEmail are the FALLBACK persona, used only when the brief names
 * none. They belong to no GitHub account, and that has a consequence worth stating: GitHub
 * links a commit to an account by the author's email, so a commit written by this persona is
 * attributed to nobody, and a deployment gate that checks the author's access — Vercel's —
 * refuses the pull request it is on. A playbook or profile that pushes anywhere real should
 * set `git:` to the account whose token it pushes with.
 */
export const GitUserName = "podium-agent";
export const GitUserEmail = "podium-agent@users.noreply.github.com";

/** GitPersona is the user.name and user.email one clone is configured with. */
export interface GitPersona {
  name: string;
  email: string;
}

/** FallbackPersona is what a brief naming no persona gets. */
export const FallbackPersona: GitPersona = { name: GitUserName, email: GitUserEmail };

/**
 * CredentialHelper is a one-line shell helper: git runs it with the operation as $1 and
 * reads the credential off its stdout. `$GITHUB_TOKEN` is expanded by that shell, from the
 * environment, every time — which is what keeps the value out of every argv and every file.
 */
export const CredentialHelper =
  `!f() { if test "$1" = get; then echo username=x-access-token; echo "password=$${TokenEnv}"; fi; }; f`;

/** CloneError carries git's stderr with the token already replaced. */
export class CloneError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "CloneError";
  }
}

/** GitRun runs one git command; it throws a CloneError carrying git's stderr. */
export type GitRun = (argv: string[]) => void;

/** redact replaces the token, raw and percent-encoded, wherever it appears. */
export function redact(text: string, token: string | undefined): string {
  if (token === undefined || token === "") {
    return text;
  }
  let out = text.split(token).join("[redacted]");
  const encoded = encodeURIComponent(token);
  if (encoded !== token) {
    out = out.split(encoded).join("[redacted]");
  }
  return out;
}

/**
 * repoCommands is the exact git sequence one repo needs, in order. The token is never one
 * of the arguments: with a token the clone borrows the credential helper through `-c`, and
 * the clone then keeps it in its own config so the agent's `git push` works too.
 */
export function repoCommands(
  repo: RepoRef,
  tokenAvailable: boolean,
  root = WorkspaceDir,
  git: GitPersona = FallbackPersona,
  helper = "",
): string[][] {
  const dest = join(root, repo.name);
  const clone = ["clone", "--depth", String(CloneDepth), "--branch", repo.default_branch, repo.url, dest];
  // A minting helper beats the static one: with a GitHub App there is no static token to
  // fall back to, and a turn that used one would be a turn using a credential the
  // conductor stopped issuing.
  const credential = helper !== "" ? helper : tokenAvailable ? CredentialHelper : "";
  const out: string[][] = [
    credential !== "" ? ["-c", `credential.helper=${credential}`, ...clone] : clone,
    ["-C", dest, "config", "user.name", git.name],
    ["-C", dest, "config", "user.email", git.email],
  ];
  if (credential !== "") {
    out.push(["-C", dest, "config", "credential.helper", credential]);
  }
  return out;
}

/**
 * resumeCommands updates a checkout that is already on disk. A restored snapshot has a
 * .git, and cloning again would wipe it. The token stays in the credential helper.
 * Fast-forward is a separate command, and only when HEAD is the default branch: a turn
 * that left a feature branch checked out keeps that branch.
 */
export function resumeCommands(
  repo: RepoRef,
  tokenAvailable: boolean,
  root = WorkspaceDir,
  git: GitPersona = FallbackPersona,
  helper = "",
): string[][] {
  const dest = join(root, repo.name);
  const fetch = ["-C", dest, "fetch", "--depth", String(CloneDepth), "origin", repo.default_branch];
  const credential = helper !== "" ? helper : tokenAvailable ? CredentialHelper : "";
  const out: string[][] = [
    credential !== "" ? ["-c", `credential.helper=${credential}`, ...fetch] : fetch,
    ["-C", dest, "config", "user.name", git.name],
    ["-C", dest, "config", "user.email", git.email],
  ];
  if (credential !== "") {
    out.push(["-C", dest, "config", "credential.helper", credential]);
  }
  return out;
}

/** fastForwardCommand moves the default branch up to what fetch just stored. */
export function fastForwardCommand(repo: RepoRef, root = WorkspaceDir): string[] {
  return ["-C", join(root, repo.name), "merge", "--ff-only", `origin/${repo.default_branch}`];
}

/** checkoutAt reports whether dest is already a git checkout, and which branch HEAD is. */
function checkoutAt(dest: string): { present: boolean; branch: string } {
  if (!existsSync(join(dest, ".git"))) {
    return { present: false, branch: "" };
  }
  try {
    const branch = execFileSync("git", ["-C", dest, "rev-parse", "--abbrev-ref", "HEAD"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
    return { present: true, branch };
  } catch {
    return { present: true, branch: "" };
  }
}

/** cloneRepos checks every repo in the brief out under /workspace, or fetches one that is already there. */
export function cloneRepos(
  repos: RepoRef[],
  opts: {
    token?: string | undefined;
    root?: string;
    run?: GitRun;
    git?: GitPersona | undefined;
    /** The credential.helper value from gitcred.installHelper, when this turn mints. */
    helper?: string | undefined;
  } = {},
): void {
  const token = opts.token;
  const has = token !== undefined && token !== "";
  const run = opts.run ?? gitRun(token);
  const root = opts.root ?? WorkspaceDir;
  const git = opts.git ?? FallbackPersona;
  const helper = opts.helper ?? "";
  for (const repo of repos) {
    const dest = join(root, repo.name);
    const checkout = checkoutAt(dest);
    const commands = checkout.present
      ? resumeCommands(repo, has, root, git, helper)
      : repoCommands(repo, has, root, git, helper);
    if (checkout.present && checkout.branch === repo.default_branch) {
      commands.push(fastForwardCommand(repo, root));
    }
    for (const argv of commands) {
      run(argv);
    }
  }
}

function gitRun(token: string | undefined): GitRun {
  return (argv) => {
    try {
      execFileSync("git", argv, {
        // A repository with no usable credential must fail rather than sit on a prompt
        // until the task's timeout.
        env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
        stdio: ["ignore", "inherit", "pipe"],
        encoding: "utf8",
      });
    } catch (err) {
      const stderr = (err as { stderr?: string }).stderr ?? "";
      const detail = stderr.trim() === "" ? String(err) : stderr.trim();
      throw new CloneError(redact(`git ${argv.join(" ")}: ${detail}`, token));
    }
  };
}
