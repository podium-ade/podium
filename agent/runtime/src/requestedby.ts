// The Requested-by trailer, enforced. A turn that writes to GitHub as the Podium App on
// somebody's behalf puts `Requested-by: Name <login>` on every commit it makes, and that
// must not depend on the model remembering an instruction.
//
// It is done with git itself rather than in the clone: the turn's process environment
// carries core.hooksPath through GIT_CONFIG_COUNT/KEY/VALUE, which git reads as command-line
// config for EVERY invocation under this process — the repositories cloneRepos checks out,
// a repository the agent clones for itself, a worktree, a rebase. A hook written into one
// clone's .git/hooks would cover only that clone, and would not run at all in a repository
// that sets its own core.hooksPath (husky, lefthook).
//
// Pointing core.hooksPath somewhere else would silence the repository's own hooks, so every
// hook here hands on to the repository's: its local core.hooksPath when it sets one, else
// its .git/hooks. commit-msg runs the repository's hook first and adds the trailer last, so
// a hook that rewrites the message cannot drop it.
//
// What it cannot stop is `git commit --no-verify`, which skips commit-msg by design. The
// brief's instruction still says the trailer is there and is to be left alone; between the
// two, a commit without it is a turn going out of its way.

import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** RequestedByEnv is the variable the hook reads the trailer's value from. */
export const RequestedByEnv = "PODIUM_REQUESTED_BY";

/** TrailerKey is the trailer every commit gets. */
export const TrailerKey = "Requested-by";

/**
 * HookNames are the client-side hooks git runs. Each one is installed, because a
 * core.hooksPath that lacks a hook simply does not run the repository's copy of it.
 */
export const HookNames = [
  "applypatch-msg",
  "pre-applypatch",
  "post-applypatch",
  "pre-commit",
  "pre-merge-commit",
  "prepare-commit-msg",
  "commit-msg",
  "post-commit",
  "pre-rebase",
  "post-checkout",
  "post-merge",
  "pre-push",
  "pre-auto-gc",
  "post-rewrite",
  "reference-transaction",
  "post-index-change",
] as const;

/**
 * hookScript is the one script every hook name runs. $0's name says which hook it is.
 *
 * The repository's hook directory is read with `git config --local`, which reads only the
 * repository's own file — not the command-line config this module sets — so it finds what
 * the repository asked for and never this directory again.
 */
export function hookScript(): string {
  return `#!/bin/sh
# Podium: hand every hook on to the repository's own, and put Requested-by on each commit.
name=$(basename "$0")
here=$(cd "$(dirname "$0")" && pwd -P)

own=$(git config --local --get core.hooksPath 2>/dev/null || true)
if [ -n "$own" ]; then
  case "$own" in
    /*) ;;
    *) top=$(git rev-parse --show-toplevel 2>/dev/null || pwd); own="$top/$own" ;;
  esac
else
  common=$(git rev-parse --git-common-dir 2>/dev/null || true)
  [ -n "$common" ] && own="$common/hooks"
fi
if [ -n "$own" ] && [ -d "$own" ]; then
  own=$(cd "$own" && pwd -P)
  [ "$own" = "$here" ] && own=""
else
  own=""
fi

if [ "$name" != "commit-msg" ]; then
  if [ -n "$own" ] && [ -x "$own/$name" ]; then
    exec "$own/$name" "$@"
  fi
  exit 0
fi

if [ -n "$own" ] && [ -x "$own/$name" ]; then
  "$own/$name" "$@" || exit $?
fi
if [ -n "$${RequestedByEnv}" ]; then
  git interpret-trailers --in-place --if-exists addIfDifferent \\
    --trailer "${TrailerKey}: $${RequestedByEnv}" "$1" || exit $?
fi
exit 0
`;
}

/**
 * installRequestedBy writes the hook directory under dir and points every git this process
 * starts at it. It returns the environment it set, for a caller that passes an explicit env
 * to a child. Empty requestedBy installs nothing: a turn committing as the person needs no
 * trailer and keeps the repository's hooks exactly as they are.
 */
export function installRequestedBy(
  dir: string,
  requestedBy: string,
  env: NodeJS.ProcessEnv = process.env,
): Record<string, string> {
  const value = requestedBy.trim();
  if (value === "") {
    return {};
  }
  const hooks = join(dir, "hooks");
  mkdirSync(hooks, { recursive: true });
  const script = hookScript();
  for (const name of HookNames) {
    const path = join(hooks, name);
    writeFileSync(path, script);
    chmodSync(path, 0o755);
  }
  // Appended after any command-line config already in the environment, never over it.
  const count = Number.parseInt(env.GIT_CONFIG_COUNT ?? "0", 10);
  const n = Number.isFinite(count) && count > 0 ? count : 0;
  const set: Record<string, string> = {
    [RequestedByEnv]: value,
    GIT_CONFIG_COUNT: String(n + 1),
    [`GIT_CONFIG_KEY_${n}`]: "core.hooksPath",
    [`GIT_CONFIG_VALUE_${n}`]: hooks,
  };
  Object.assign(env, set);
  return set;
}
