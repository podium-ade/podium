// Checking one turn's Agent Skills before the harness is pointed at them.
//
// The skills are already on disk when this process starts: on a node they are read-only
// mounts the node wrote from the task spec, one directory per skill under the brief's
// skills_dir, and for a host turn the conductor wrote them into the turn's own HOME. Nothing
// here writes anything. The harness is told where they are with `skills.paths` in its
// config (https://opencode.ai/docs/skills/), so the directory is read where it was mounted.
//
// What this does is refuse to start a turn on a directory that is not what the brief
// promised. A node too old to know about mounts drops them without a word, and the turn
// would then run with fewer skills than its playbook describes; a digest that does not
// match is the same failure in a different shape. Each skill's files are hashed exactly as
// internal/agent/skills.Digest does and compared with the brief's sha256.

import { createHash } from "node:crypto";
import { lstatSync, readFileSync, readdirSync } from "node:fs";
import { homedir } from "node:os";
import { isAbsolute, join } from "node:path";

import type { SkillRef } from "./brief.js";

/** SkillFile is the one file a skill must contain, spelled exactly this way. */
export const SkillFile = "SKILL.md";

/**
 * SkillNameRE is the harness's own naming rule, which is also the directory name. It is
 * re-checked here rather than trusted from the brief: the name becomes a path component.
 */
export const SkillNameRE = /^[a-z0-9]+(-[a-z0-9]+)*$/;

/** MaxSkillNameLen is the harness's limit on a skill name. */
export const MaxSkillNameLen = 64;

/**
 * The caps, mirroring internal/agent/skills. The conductor has already applied them; here
 * they bound how much of a directory this process is willing to read to find out.
 */
export const MaxSkills = 8;
export const MaxSkillFiles = 64;
export const MaxSkillBytes = 10 * 1024 * 1024;

/** SkillError is every way a skill can be unusable. It always fails the turn. */
export class SkillError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SkillError";
  }
}

/** resolveSkillsDir is the brief's skills_dir as an absolute path: relative is to HOME. */
export function resolveSkillsDir(dir: string, home = process.env.HOME || homedir()): string {
  return isAbsolute(dir) ? dir : join(home, dir);
}

/**
 * skillDigest is internal/agent/skills.Digest: for each path in byte order, the path, a
 * NUL, the content's length in bytes as decimal, a NUL, and the content.
 */
export function skillDigest(files: Map<string, Buffer>): string {
  const h = createHash("sha256");
  // Paths are ASCII (the conductor's path rule), so a code-unit sort is a byte sort.
  for (const rel of [...files.keys()].sort()) {
    const content = files.get(rel) as Buffer;
    h.update(`${rel}\0${content.length}\0`, "utf8");
    h.update(content);
  }
  return h.digest("hex");
}

/**
 * verifySkills checks every skill in the brief against what is under dir and returns the
 * names it checked, in order.
 *
 * One failure fails all of them, and the caller fails the turn. A turn that ran with three
 * of its four skills would answer differently from the playbook it claims to be, and nobody
 * watching would know which of the two had happened.
 */
export function verifySkills(refs: SkillRef[], dir: string): string[] {
  if (refs.length > MaxSkills) {
    throw new SkillError(`the turn brief carries ${refs.length} skills; the limit is ${MaxSkills}`);
  }
  const done: string[] = [];
  for (const ref of refs) {
    if (done.includes(ref.name)) {
      throw new SkillError(`skill "${ref.name}" appears twice in the turn brief`);
    }
    verify(ref, dir);
    done.push(ref.name);
  }
  return done;
}

function verify(ref: SkillRef, dir: string): void {
  const where = `skill "${ref.name}"`;
  if (ref.name.length > MaxSkillNameLen || !SkillNameRE.test(ref.name)) {
    throw new SkillError(`${where}: the name must match ${SkillNameRE.source} and be at most ${MaxSkillNameLen} characters`);
  }
  const root = join(dir, ref.name);
  let isDir = false;
  try {
    isDir = lstatSync(root).isDirectory();
  } catch {
    // Reported below, as the same failure.
  }
  if (!isDir) {
    throw new SkillError(
      `${where}: ${root} is not there; the node that ran this turn did not mount it, which an older node does not know how to do`,
    );
  }

  const files = new Map<string, Buffer>();
  let total = 0;
  const walk = (abs: string, rel: string): void => {
    for (const entry of readdirSync(abs, { withFileTypes: true })) {
      const childAbs = join(abs, entry.name);
      const childRel = rel === "" ? entry.name : `${rel}/${entry.name}`;
      if (entry.isDirectory()) {
        walk(childAbs, childRel);
        continue;
      }
      if (!entry.isFile()) {
        throw new SkillError(`${where}: ${childRel} is not a regular file`);
      }
      if (files.size === MaxSkillFiles) {
        throw new SkillError(`${where}: more than ${MaxSkillFiles} files; that is the limit`);
      }
      const content = readFileSync(childAbs);
      total += content.length;
      if (total > MaxSkillBytes) {
        throw new SkillError(`${where}: more than ${MaxSkillBytes} bytes of files; that is the limit`);
      }
      files.set(childRel, content);
    }
  };
  walk(root, "");
  if (!files.has(SkillFile)) {
    throw new SkillError(`${where}: ${root} has no ${SkillFile}`);
  }
  const digest = skillDigest(files);
  if (digest !== ref.sha256) {
    throw new SkillError(`${where}: ${root} hashes to ${digest} and the turn brief says ${ref.sha256}`);
  }
}
