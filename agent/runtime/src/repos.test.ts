import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { describe, expect, it } from "vitest";

import type { RepoRef } from "./brief.js";
import {
  CloneError,
  CredentialHelper,
  FallbackPersona,
  GitUserEmail,
  TokenEnv,
  cloneRepos,
  redact,
  repoCommands,
} from "./repos.js";

const token = "ghp_ThisIsNotARealToken0000000000000000";

const repo: RepoRef = {
  name: "podium",
  url: "https://github.com/podium-ade/podium",
  default_branch: "main",
};

describe("repoCommands", () => {
  it("clones the plain URL when there is no token", () => {
    expect(repoCommands(repo, false)).toEqual([
      ["clone", "--depth", "50", "--branch", "main", repo.url, "/workspace/podium"],
      ["-C", "/workspace/podium", "config", "user.name", "podium-agent"],
      ["-C", "/workspace/podium", "config", "user.email", GitUserEmail],
    ]);
  });

  it("clones through the credential helper when there is one", () => {
    const argv = repoCommands(repo, true);
    expect(argv[0]).toEqual([
      "-c",
      `credential.helper=${CredentialHelper}`,
      "clone",
      "--depth",
      "50",
      "--branch",
      "main",
      repo.url,
      "/workspace/podium",
    ]);
    expect(argv.at(-1)).toEqual([
      "-C",
      "/workspace/podium",
      "config",
      "credential.helper",
      CredentialHelper,
    ]);
  });

  it("never puts the token in an argv", () => {
    const flat = repoCommands(repo, true).flat().join(" ");
    expect(flat).not.toContain(token);
    expect(flat).toContain("$GITHUB_TOKEN");
  });

  it("keeps the plain URL as origin, so the token is never in .git/config", () => {
    for (const argv of repoCommands(repo, true)) {
      expect(argv.join(" ")).not.toContain("x-access-token:");
    }
  });

  it("puts each repo under its own directory", () => {
    expect(repoCommands({ ...repo, name: "other" }, false)[0]?.at(-1)).toBe("/workspace/other");
    expect(repoCommands(repo, false, "/tmp/ws")[0]?.at(-1)).toBe("/tmp/ws/podium");
  });
});

describe("repoCommands with a minting helper", () => {
  const helper = "!/tmp/podium-git-xyz/podium-git-credential.mjs";

  it("uses the helper for the clone and keeps it in the clone's own config", () => {
    const argv = repoCommands(repo, false, "/workspace", FallbackPersona, helper);
    expect(argv[0]?.slice(0, 2)).toEqual(["-c", `credential.helper=${helper}`]);
    expect(argv.at(-1)).toEqual(["-C", "/workspace/podium", "config", "credential.helper", helper]);
  });

  // With a GitHub App there is no static token to fall back to, so a turn that preferred
  // one would be a turn using a credential the conductor stopped issuing.
  it("beats the static token helper when both are available", () => {
    const flat = repoCommands(repo, true, "/workspace", FallbackPersona, helper).flat().join(" ");
    expect(flat).toContain(helper);
    expect(flat).not.toContain(TokenEnv);
  });

  it("writes the persona the mint reported, so commits carry the app's bot account", () => {
    const bot = { name: "podium-agent[bot]", email: "987654+podium-agent[bot]@users.noreply.github.com" };
    const argv = repoCommands(repo, false, "/workspace", bot, helper);
    expect(argv).toContainEqual(["-C", "/workspace/podium", "config", "user.name", bot.name]);
    expect(argv).toContainEqual(["-C", "/workspace/podium", "config", "user.email", bot.email]);
  });

  it("passes the helper through cloneRepos", () => {
    const seen: string[][] = [];
    cloneRepos([repo], { run: (argv) => seen.push(argv), helper });
    expect(seen.flat().join(" ")).toContain(helper);
  });
});

describe("cloneRepos", () => {
  it("runs every command for every repo, in order", () => {
    const seen: string[][] = [];
    cloneRepos([repo, { ...repo, name: "second" }], { run: (argv) => seen.push(argv) });
    expect(seen).toHaveLength(6);
    expect(seen[0]?.[0]).toBe("clone");
    expect(seen[3]?.[0]).toBe("clone");
  });

  it("does nothing for an empty list", () => {
    const seen: string[][] = [];
    cloneRepos([], { run: (argv) => seen.push(argv) });
    expect(seen).toEqual([]);
  });

  it("propagates a failure so the turn can exit 4", () => {
    expect(() =>
      cloneRepos([repo], {
        run: () => {
          throw new Error("fatal: could not read Username");
        },
      }),
    ).toThrow("fatal: could not read Username");
  });
});

describe("redact", () => {
  it("replaces the token wherever it appears", () => {
    const stderr = `fatal: unable to access 'https://x-access-token:${token}@github.com/o/r/': 403`;
    expect(redact(stderr, token)).toBe(
      "fatal: unable to access 'https://x-access-token:[redacted]@github.com/o/r/': 403",
    );
    expect(redact(stderr, token)).not.toContain(token);
  });

  it("replaces a percent-encoded token too", () => {
    const weird = "tok/en+with=chars";
    const text = `saw ${encodeURIComponent(weird)} and ${weird}`;
    expect(redact(text, weird)).toBe("saw [redacted] and [redacted]");
  });

  it("leaves the text alone when there is no token", () => {
    expect(redact("fatal: repository not found", undefined)).toBe("fatal: repository not found");
    expect(redact("fatal: repository not found", "")).toBe("fatal: repository not found");
  });
});

describe("cloneRepos against a real git", () => {
  /** origin builds a tiny repository cloneRepos can check out from a local path. */
  function origin(): string {
    const root = mkdtempSync(join(tmpdir(), "podium-origin-"));
    const git = (...argv: string[]): void => {
      execFileSync("git", ["-C", root, ...argv], { stdio: "ignore" });
    };
    execFileSync("git", ["init", "-b", "main", root], { stdio: "ignore" });
    writeFileSync(join(root, "README.md"), "hello\n", "utf8");
    git("add", "README.md");
    git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "first");
    return root;
  }

  it("leaves the token out of .git/config, so the agent cannot cat it", () => {
    const url = origin();
    const root = mkdtempSync(join(tmpdir(), "podium-workspace-"));
    cloneRepos([{ name: "podium", url, default_branch: "main" }], { token, root });

    const config = readFileSync(join(root, "podium", ".git", "config"), "utf8");
    expect(config).not.toContain(token);
    expect(config).toContain("$GITHUB_TOKEN");
    expect(config).toContain("podium-agent@users.noreply.github.com");
    expect(readFileSync(join(root, "podium", "README.md"), "utf8")).toBe("hello\n");
  });

  it("clones without a token at all", () => {
    const root = mkdtempSync(join(tmpdir(), "podium-workspace-"));
    cloneRepos([{ name: "podium", url: origin(), default_branch: "main" }], { root });
    const config = readFileSync(join(root, "podium", ".git", "config"), "utf8");
    expect(config).not.toContain("credential.helper");
    expect(config).not.toContain("helper");
  });

  it("fails with git's own message, redacted, when the branch is not there", () => {
    const root = mkdtempSync(join(tmpdir(), "podium-workspace-"));
    let thrown: unknown;
    try {
      cloneRepos([{ name: "podium", url: origin(), default_branch: "nope" }], { token, root });
    } catch (err) {
      thrown = err;
    }
    expect(thrown).toBeInstanceOf(CloneError);
    expect((thrown as Error).message).toContain("nope");
    expect((thrown as Error).message).not.toContain(token);
  });
});
