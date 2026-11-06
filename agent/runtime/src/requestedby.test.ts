import { execFileSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import { HookNames, RequestedByEnv, installRequestedBy } from "./requestedby.js";

// Real git, real hooks: the point is what git does with them, which a mock cannot say.

function repo(env: NodeJS.ProcessEnv): string {
  const dir = mkdtempSync(join(tmpdir(), "podium-rb-repo-"));
  git(dir, env, "init", "-q", "-b", "main");
  git(dir, env, "config", "user.name", "Podium App");
  git(dir, env, "config", "user.email", "app@example.com");
  return dir;
}

function git(dir: string, env: NodeJS.ProcessEnv, ...args: string[]): string {
  return execFileSync("git", ["-C", dir, ...args], { env, encoding: "utf8" });
}

function commit(dir: string, env: NodeJS.ProcessEnv, message: string, ...extra: string[]): string {
  writeFileSync(join(dir, `f${Math.random()}`), "x");
  git(dir, env, "add", "-A");
  git(dir, env, "commit", "-q", "-m", message, ...extra);
  return git(dir, env, "log", "-1", "--format=%B");
}

function turnEnv(value: string): NodeJS.ProcessEnv {
  // A clean slate: no inherited command-line config from whoever runs the tests.
  const env: NodeJS.ProcessEnv = { ...process.env };
  for (const k of Object.keys(env)) {
    if (k.startsWith("GIT_CONFIG_") || k === RequestedByEnv) delete env[k];
  }
  env.GIT_CONFIG_NOSYSTEM = "1";
  env.HOME = mkdtempSync(join(tmpdir(), "podium-rb-home-"));
  installRequestedBy(mkdtempSync(join(tmpdir(), "podium-rb-hooks-")), value, env);
  return env;
}

describe("installRequestedBy", () => {
  it("puts the trailer on every commit, once", () => {
    const env = turnEnv("Dana Smith <dana@acme.com>");
    const dir = repo(env);
    const first = commit(dir, env, "fix: the thing");
    expect(first).toContain("Requested-by: Dana Smith <dana@acme.com>");
    const amended = execFileSync("git", ["-C", dir, "commit", "-q", "--amend", "--no-edit"], { env, encoding: "utf8" });
    expect(amended).toBe("");
    const msg = git(dir, env, "log", "-1", "--format=%B");
    expect(msg.match(/Requested-by:/g)).toHaveLength(1);
  });

  it("covers a repository the agent clones for itself", () => {
    const env = turnEnv("Dana <dana@acme.com>");
    const origin = repo(env);
    commit(origin, env, "init");
    const clone = mkdtempSync(join(tmpdir(), "podium-rb-clone-"));
    execFileSync("git", ["clone", "-q", origin, join(clone, "r")], { env });
    const dir = join(clone, "r");
    git(dir, env, "config", "user.name", "Podium App");
    git(dir, env, "config", "user.email", "app@example.com");
    expect(commit(dir, env, "feat: more")).toContain("Requested-by: Dana <dana@acme.com>");
  });

  it("still runs the repository's own .git/hooks", () => {
    const env = turnEnv("Dana <dana@acme.com>");
    const dir = repo(env);
    const marker = join(dir, ".git", "pre-commit-ran");
    writeFileSync(join(dir, ".git", "hooks", "pre-commit"), `#!/bin/sh\ntouch "${marker}"\n`);
    chmodSync(join(dir, ".git", "hooks", "pre-commit"), 0o755);
    commit(dir, env, "chore: x");
    expect(() => readFileSync(marker)).not.toThrow();
  });

  it("runs a repository's own core.hooksPath, and a commit-msg there cannot drop the trailer", () => {
    const env = turnEnv("Dana <dana@acme.com>");
    const dir = repo(env);
    mkdirSync(join(dir, ".husky"));
    // A hook that rewrites the message wholesale. Ours runs after it.
    writeFileSync(join(dir, ".husky", "commit-msg"), `#!/bin/sh\necho "rewritten" > "$1"\n`);
    chmodSync(join(dir, ".husky", "commit-msg"), 0o755);
    git(dir, env, "config", "core.hooksPath", ".husky");
    const msg = commit(dir, env, "original");
    expect(msg).toContain("rewritten");
    expect(msg).toContain("Requested-by: Dana <dana@acme.com>");
  });

  it("refuses the commit when the repository's commit-msg does", () => {
    const env = turnEnv("Dana <dana@acme.com>");
    const dir = repo(env);
    writeFileSync(join(dir, ".git", "hooks", "commit-msg"), "#!/bin/sh\nexit 1\n");
    chmodSync(join(dir, ".git", "hooks", "commit-msg"), 0o755);
    expect(() => commit(dir, env, "nope")).toThrow();
  });

  it("appends to command-line config already in the environment", () => {
    const env: NodeJS.ProcessEnv = { GIT_CONFIG_COUNT: "1", GIT_CONFIG_KEY_0: "a.b", GIT_CONFIG_VALUE_0: "c" };
    const set = installRequestedBy(mkdtempSync(join(tmpdir(), "podium-rb-hooks-")), "X <x@y>", env);
    expect(env.GIT_CONFIG_COUNT).toBe("2");
    expect(env.GIT_CONFIG_KEY_0).toBe("a.b");
    expect(env.GIT_CONFIG_KEY_1).toBe("core.hooksPath");
    expect(set[RequestedByEnv]).toBe("X <x@y>");
  });

  it("installs nothing for a turn committing as the person", () => {
    const env: NodeJS.ProcessEnv = {};
    expect(installRequestedBy(mkdtempSync(join(tmpdir(), "podium-rb-hooks-")), "  ", env)).toEqual({});
    expect(env.GIT_CONFIG_COUNT).toBeUndefined();
  });

  it("installs every client-side hook name", () => {
    expect(HookNames).toContain("commit-msg");
    expect(HookNames).toContain("pre-commit");
    expect(HookNames).toContain("pre-push");
  });
});
