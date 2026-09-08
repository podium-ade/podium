// Putting one turn's Agent Skills where the harness will find them.
//
// The harness discovers skills from `$HOME/.config/opencode/skills/<name>/SKILL.md` on its
// own (https://opencode.ai/docs/skills/), so Podium's whole job here is to have written that
// directory before the harness starts, and to have written nothing else.
//
// A bundle is somebody else's content. It reached this process as base64 on an environment
// variable, which is a channel with no integrity of its own, so nothing is written until the
// digest in the brief matches the bytes that arrived — and every path inside is checked
// component by component rather than normalised, because a normalised `../../.ssh` is still
// a write outside the skill's directory.
//
// The wire format is a JSON map of relative path to file content, gzipped. It is not a tar
// on purpose: a tar entry can be a symlink, a hardlink, a device node or a mode bit, and
// each of those is a thing an unpacker has to remember to refuse. This format cannot express
// any of them, so that whole class of archive attack is absent rather than defended against.
// What it costs is that a bundle carries text only, and nothing it writes is executable.
import { createHash } from "node:crypto";
import { mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { gunzipSync } from "node:zlib";
/** SkillFile is the one file a bundle must contain, spelled exactly this way. */
export const SkillFile = "SKILL.md";
/**
 * SkillNameRE is the harness's own naming rule, which is also the directory name. It is
 * re-checked here rather than trusted from the brief: the name becomes a path component.
 */
export const SkillNameRE = /^[a-z0-9]+(-[a-z0-9]+)*$/;
/** MaxSkillNameLen is the harness's limit on a skill name. */
export const MaxSkillNameLen = 64;
/**
 * The caps, mirroring internal/agent/skills. The conductor has already applied them; they
 * are applied again here because this side is what writes to a filesystem, and a cap that
 * only exists on the side that packs is not a cap.
 */
export const MaxSkills = 8;
export const MaxSkillFiles = 64;
export const MaxSkillBytes = 128 * 1024;
export const MaxSkillEncodedBytes = 64 * 1024;
/** MaxSkillPathLen and MaxSkillPathDepth bound one path inside a bundle. */
export const MaxSkillPathLen = 255;
export const MaxSkillPathDepth = 8;
/** pathSegmentRE is what one component of a bundle path may look like. */
const pathSegmentRE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
/** SkillError is every way a bundle can be unusable. It always fails the turn. */
export class SkillError extends Error {
    constructor(message) {
        super(message);
        this.name = "SkillError";
    }
}
/** skillsRoot is where the harness looks for global skills. */
export function skillsRoot(home = process.env.HOME || homedir()) {
    return join(home, ".config", "opencode", "skills");
}
/**
 * installSkills unpacks every skill in the brief and returns the names it wrote, in order.
 *
 * One failure fails all of them, and the caller fails the turn. A turn that ran with three
 * of its four skills would answer differently from the playbook it claims to be, and nobody
 * watching would know which of the two had happened.
 */
export function installSkills(refs, env = process.env, root = skillsRoot()) {
    if (refs.length > MaxSkills) {
        throw new SkillError(`the turn brief carries ${refs.length} skills; the limit is ${MaxSkills}`);
    }
    const done = [];
    for (const ref of refs) {
        if (done.includes(ref.name)) {
            throw new SkillError(`skill "${ref.name}" appears twice in the turn brief`);
        }
        install(ref, env, root);
        done.push(ref.name);
    }
    return done;
}
function install(ref, env, root) {
    const where = `skill "${ref.name}"`;
    if (ref.name.length > MaxSkillNameLen || !SkillNameRE.test(ref.name)) {
        throw new SkillError(`${where}: the name must match ${SkillNameRE.source} and be at most ${MaxSkillNameLen} characters`);
    }
    const encoded = env[ref.bundle_env];
    if (encoded === undefined || encoded === "") {
        throw new SkillError(`${where}: the turn brief names ${ref.bundle_env} but it is not set`);
    }
    if (Buffer.byteLength(encoded, "utf8") > MaxSkillEncodedBytes) {
        throw new SkillError(`${where}: ${ref.bundle_env} is ${Buffer.byteLength(encoded, "utf8")} bytes; the limit is ${MaxSkillEncodedBytes}`);
    }
    let raw;
    try {
        // maxOutputLength is the decompression bomb guard, and it is the reason the cap is
        // checked here rather than after parsing: a bundle that expands past it never reaches
        // memory in full.
        raw = gunzipSync(Buffer.from(encoded, "base64"), { maxOutputLength: MaxSkillBytes });
    }
    catch (err) {
        throw new SkillError(`${where}: ${ref.bundle_env} did not decompress (the limit is ${MaxSkillBytes} bytes unpacked): ${messageOf(err)}`);
    }
    const digest = createHash("sha256").update(raw).digest("hex");
    if (digest !== ref.sha256) {
        // Before anything is parsed, and long before anything is written.
        throw new SkillError(`${where}: the bundle hashes to ${digest} and the turn brief says ${ref.sha256}`);
    }
    const files = readDocument(where, raw);
    const dir = join(root, ref.name);
    for (const [rel, content] of files) {
        const dest = join(dir, rel);
        mkdirSync(dirname(dest), { recursive: true });
        writeFileSync(dest, content, { encoding: "utf8", mode: 0o644 });
    }
}
/**
 * readDocument parses and checks the bundle. It returns the files sorted by path so that
 * what is written is a function of the bundle and not of a JSON key order.
 */
function readDocument(where, raw) {
    let parsed;
    try {
        parsed = JSON.parse(raw.toString("utf8"));
    }
    catch (err) {
        throw new SkillError(`${where}: the bundle is not JSON: ${messageOf(err)}`);
    }
    const doc = parsed;
    const map = doc?.files;
    if (map === null || typeof map !== "object" || Array.isArray(map)) {
        throw new SkillError(`${where}: the bundle has no files object`);
    }
    const entries = Object.entries(map);
    if (entries.length === 0) {
        throw new SkillError(`${where}: the bundle is empty`);
    }
    if (entries.length > MaxSkillFiles) {
        throw new SkillError(`${where}: the bundle holds ${entries.length} files; the limit is ${MaxSkillFiles}`);
    }
    const out = [];
    let total = 0;
    for (const [rel, content] of entries) {
        checkPath(where, rel);
        if (typeof content !== "string") {
            throw new SkillError(`${where}: ${rel} is not text; a skill bundle carries text only`);
        }
        total += Buffer.byteLength(content, "utf8");
        if (total > MaxSkillBytes) {
            throw new SkillError(`${where}: the bundle holds more than ${MaxSkillBytes} bytes of files`);
        }
        out.push([rel, content]);
    }
    if (!out.some(([rel]) => rel === SkillFile)) {
        throw new SkillError(`${where}: the bundle has no ${SkillFile}`);
    }
    out.sort(([a], [b]) => (a < b ? -1 : 1));
    return out;
}
/**
 * checkPath is the traversal guard: every component has to be an ordinary name, so `..`,
 * an absolute path, a Windows separator, a NUL and a hidden dotfile are all refused by the
 * same rule rather than by a list of special cases.
 */
function checkPath(where, rel) {
    if (rel === "" || rel.length > MaxSkillPathLen) {
        throw new SkillError(`${where}: a bundle path must be 1 to ${MaxSkillPathLen} characters`);
    }
    if (rel.startsWith("/") || rel.includes("\\") || rel.includes("\0")) {
        throw new SkillError(`${where}: ${JSON.stringify(rel)} must be a relative path with no backslash`);
    }
    const parts = rel.split("/");
    if (parts.length > MaxSkillPathDepth) {
        throw new SkillError(`${where}: ${rel} is more than ${MaxSkillPathDepth} directories deep`);
    }
    for (const part of parts) {
        if (!pathSegmentRE.test(part)) {
            throw new SkillError(`${where}: ${rel} has a path component ${JSON.stringify(part)} outside ${pathSegmentRE.source}`);
        }
    }
}
function messageOf(err) {
    return err instanceof Error ? err.message : String(err);
}
