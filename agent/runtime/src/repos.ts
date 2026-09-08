// Shallow clones for the turn. The token reaches git through a credential helper that reads
// the environment at the moment git asks for it, so it never appears in an argv, in
// .git/config, or in a log line. Redaction on the way out is defence in depth, not the
// control.

import { execFileSync } from "node:child_process";
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

export const GitUserName = "podium-agent";
export const GitUserEmail = "podium-agent@users.noreply.github.com";

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
export function repoCommands(repo: RepoRef, tokenAvailable: boolean, root = WorkspaceDir): string[][] {
  const dest = join(root, repo.name);
  const clone = ["clone", "--depth", String(CloneDepth), "--branch", repo.default_branch, repo.url, dest];
  const out: string[][] = [
    tokenAvailable ? ["-c", `credential.helper=${CredentialHelper}`, ...clone] : clone,
    ["-C", dest, "config", "user.name", GitUserName],
    ["-C", dest, "config", "user.email", GitUserEmail],
  ];
  if (tokenAvailable) {
    out.push(["-C", dest, "config", "credential.helper", CredentialHelper]);
  }
  return out;
}

/** cloneRepos checks every repo in the brief out under /workspace. */
export function cloneRepos(
  repos: RepoRef[],
  opts: { token?: string | undefined; root?: string; run?: GitRun } = {},
): void {
  const token = opts.token;
  const has = token !== undefined && token !== "";
  const run = opts.run ?? gitRun(token);
  for (const repo of repos) {
    for (const argv of repoCommands(repo, has, opts.root ?? WorkspaceDir)) {
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
