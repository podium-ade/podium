// Messages leave the container through `podium-runner message` and nothing else. The
// newline-JSON event protocol has exactly one implementation (internal/runner) and this is
// not a second one.
import { execFile } from "node:child_process";
/** RunnerPath is where the node bind-mounts podium-runner (docs/runner-events.md). */
export const RunnerPath = "/podium/runner";
/**
 * RunnerPathEnv overrides RunnerPath. A turn the conductor runs on its own host
 * (internal/agent/conductor/host.go) has no node, so nothing bind-mounts anything: the
 * runner is an ordinary binary somewhere on that host and only the conductor knows where.
 */
export const RunnerPathEnv = "PODIUM_RUNNER_PATH";
/** defaultRunnerPath is the runner this turn should invoke. */
export function defaultRunnerPath(env = process.env) {
    const override = env[RunnerPathEnv];
    return override === undefined || override === "" ? RunnerPath : override;
}
/** MaxMessageBytes is internal/runner.MaxMessageBytes: the runner refuses more. */
export const MaxMessageBytes = 32 * 1024;
/**
 * messageArgv builds the argv. The text goes on stdin (`TEXT` of `-`) rather than in argv,
 * so a turn's answer never appears in the container's process list.
 */
export function messageArgv(type, attachments) {
    const argv = ["message", "--type", type];
    for (const name of attachments) {
        argv.push("--attach", name);
    }
    argv.push("-");
    return argv;
}
/**
 * emitMessage sends one message, split into as many runner calls as the 32 KiB cap needs.
 * Only the last chunk carries the attachments, so the relay attaches them to the last thing
 * it posts.
 */
export async function emitMessage(type, text, attachments, invoke) {
    const chunks = splitMessage(text);
    for (let i = 0; i < chunks.length; i++) {
        const last = i === chunks.length - 1;
        await invoke(messageArgv(type, last ? attachments : []), chunks[i] ?? "");
    }
}
/** runnerInvoke is the real thing: one short-lived process per message. */
export function runnerInvoke(runnerPath = defaultRunnerPath()) {
    return (argv, text) => new Promise((resolve, reject) => {
        const child = execFile(runnerPath, argv, (err, _stdout, stderr) => {
            if (err) {
                const detail = stderr.trim() === "" ? err.message : stderr.trim();
                reject(new Error(`${runnerPath} ${argv.join(" ")}: ${detail}`));
                return;
            }
            resolve();
        });
        child.stdin?.end(text);
    });
}
/**
 * splitMessage cuts text into runner-sized pieces on the widest boundary that works:
 * paragraph, then line, then word, then codepoint. The runner refuses a long message rather
 * than truncating it, so the splitting has to happen here.
 */
export function splitMessage(text, max = MaxMessageBytes) {
    const trimmed = trimEnd(text);
    if (trimmed === "") {
        return [];
    }
    if (Buffer.byteLength(trimmed, "utf8") <= max) {
        return [trimmed];
    }
    const out = [];
    for (const piece of chop(trimmed, max)) {
        const clean = trimEnd(piece);
        if (clean !== "") {
            out.push(clean);
        }
    }
    return out;
}
function chop(text, max) {
    if (Buffer.byteLength(text, "utf8") <= max) {
        return [text];
    }
    for (const sep of ["\n\n", "\n", " "]) {
        const parts = splitKeepingSeparator(text, sep);
        if (parts.length > 1) {
            return pack(parts, max).flatMap((p) => chop(p, max));
        }
    }
    return hardChop(text, max);
}
function splitKeepingSeparator(text, sep) {
    const parts = text.split(sep);
    return parts.map((p, i) => (i < parts.length - 1 ? p + sep : p)).filter((p) => p !== "");
}
function pack(parts, max) {
    const out = [];
    let cur = "";
    let curBytes = 0;
    for (const part of parts) {
        const bytes = Buffer.byteLength(part, "utf8");
        if (cur !== "" && curBytes + bytes > max) {
            out.push(cur);
            cur = "";
            curBytes = 0;
        }
        cur += part;
        curBytes += bytes;
    }
    if (cur !== "") {
        out.push(cur);
    }
    return out;
}
function hardChop(text, max) {
    const out = [];
    let cur = "";
    let curBytes = 0;
    for (const ch of text) {
        const bytes = Buffer.byteLength(ch, "utf8");
        if (cur !== "" && curBytes + bytes > max) {
            out.push(cur);
            cur = "";
            curBytes = 0;
        }
        cur += ch;
        curBytes += bytes;
    }
    if (cur !== "") {
        out.push(cur);
    }
    return out;
}
function trimEnd(text) {
    return text.replace(/\s+$/u, "");
}
