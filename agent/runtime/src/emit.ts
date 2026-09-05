// Messages leave the container through `podium-runner message` and nothing else. The
// newline-JSON event protocol has exactly one implementation (internal/runner) and this is
// not a second one.

import { execFile } from "node:child_process";

/** RunnerPath is where the node bind-mounts podium-runner (docs/runner-events.md). */
export const RunnerPath = "/podium/runner";

/** MaxMessageBytes is internal/runner.MaxMessageBytes: the runner refuses more. */
export const MaxMessageBytes = 32 * 1024;

/**
 * MessageType is what goes in `--type`. The wire's set is open (docs/runner-events.md):
 * `progress` and `final` are read by a human, `accounting` is turn.json for the conductor
 * and is never posted anywhere.
 */
export type MessageType = "progress" | "final" | "accounting";

/** RunnerInvoke runs one `podium-runner message` with the text on its stdin. */
export type RunnerInvoke = (argv: string[], text: string) => Promise<void>;

/**
 * messageArgv builds the argv. The text goes on stdin (`TEXT` of `-`) rather than in argv,
 * so a turn's answer never appears in the container's process list.
 */
export function messageArgv(type: MessageType, attachments: string[]): string[] {
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
export async function emitMessage(
  type: MessageType,
  text: string,
  attachments: string[],
  invoke: RunnerInvoke,
): Promise<void> {
  const chunks = splitMessage(text);
  for (let i = 0; i < chunks.length; i++) {
    const last = i === chunks.length - 1;
    await invoke(messageArgv(type, last ? attachments : []), chunks[i] ?? "");
  }
}

/** runnerInvoke is the real thing: one short-lived process per message. */
export function runnerInvoke(runnerPath = RunnerPath): RunnerInvoke {
  return (argv, text) =>
    new Promise<void>((resolve, reject) => {
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
export function splitMessage(text: string, max = MaxMessageBytes): string[] {
  const trimmed = trimEnd(text);
  if (trimmed === "") {
    return [];
  }
  if (Buffer.byteLength(trimmed, "utf8") <= max) {
    return [trimmed];
  }
  const out: string[] = [];
  for (const piece of chop(trimmed, max)) {
    const clean = trimEnd(piece);
    if (clean !== "") {
      out.push(clean);
    }
  }
  return out;
}

function chop(text: string, max: number): string[] {
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

function splitKeepingSeparator(text: string, sep: string): string[] {
  const parts = text.split(sep);
  return parts.map((p, i) => (i < parts.length - 1 ? p + sep : p)).filter((p) => p !== "");
}

function pack(parts: string[], max: number): string[] {
  const out: string[] = [];
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

function hardChop(text: string, max: number): string[] {
  const out: string[] = [];
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

function trimEnd(text: string): string {
  return text.replace(/\s+$/u, "");
}
