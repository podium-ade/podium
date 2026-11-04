import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { afterEach, describe, expect, it } from "vitest";

import { isEntrypoint } from "./entry.js";

let dir = "";

afterEach(() => {
  if (dir !== "") {
    rmSync(dir, { recursive: true, force: true });
    dir = "";
  }
});

/** program writes an empty module under a directory whose name needs escaping in a URL. */
function program(name: string): string {
  dir = mkdtempSync(join(tmpdir(), "podium-entry-"));
  const sub = join(dir, name);
  mkdirSync(sub);
  const file = join(sub, "mcp.js");
  writeFileSync(file, "");
  return file;
}

describe("isEntrypoint", () => {
  // A git worktree for branch feature/x is a directory named feature%2Fx. Its URL spells
  // that feature%252Fx, which a string comparison with argv[1] never matched.
  it.each(["feature%2Fx", "with space", "hash#mark", "naïve"])(
    "matches the program under a directory named %s",
    (name) => {
      const file = program(name);
      expect(isEntrypoint(pathToFileURL(file).href, file)).toBe(true);
    },
  );

  it("matches the program run through a symlink", () => {
    const file = program("real");
    const link = join(dir, "link.js");
    symlinkSync(file, link);
    expect(isEntrypoint(pathToFileURL(file).href, link)).toBe(true);
  });

  it("does not match a module that was imported rather than run", () => {
    const file = program("feature%2Fx");
    const other = join(dir, "main.js");
    writeFileSync(other, "");
    expect(isEntrypoint(pathToFileURL(file).href, other)).toBe(false);
  });

  it("does not match when node was given no program", () => {
    const file = program("x");
    expect(isEntrypoint(pathToFileURL(file).href, undefined)).toBe(false);
    expect(isEntrypoint(pathToFileURL(file).href, "")).toBe(false);
    expect(isEntrypoint(pathToFileURL(file).href, join(dir, "missing.js"))).toBe(false);
  });
});
