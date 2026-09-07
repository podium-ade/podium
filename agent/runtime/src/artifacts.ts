// Everything the turn leaves behind. Both files land in the directory the node collects
// automatically when the container exits (internal/node/docker/artifacts.go,
// AutoArtifactDir), so nothing here calls `podium-runner artifact add`.

import { appendFileSync, mkdirSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/** ArtifactsDir is AutoArtifactDir: every regular file under it becomes an artifact. */
export const ArtifactsDir = "/workspace/.podium/artifacts";

/** TranscriptName holds one JSON line per SDK message. */
export const TranscriptName = "transcript.jsonl";

/** TurnName holds the turn's summary. */
export const TurnName = "turn.json";

/** ChatTitleName is the model-written name of a web chat, first turn only. */
export const ChatTitleName = "chat-title.txt";

const runtimeOwned = new Set([TranscriptName, TurnName, ChatTitleName]);

/** TurnSummary is turn.json. */
export interface TurnSummary {
  session_id: string;
  turn_id: string;
  sdk_session_id: string;
  num_turns: number;
  total_cost_usd: number;
  exit_code: number;
  started_at: string;
  finished_at: string;
}

/**
 * ensureArtifacts creates the directory and an empty transcript, so a turn that yields no
 * SDK message at all — a dry run — still leaves both files for the reader to find.
 */
export function ensureArtifacts(dir = ArtifactsDir): void {
  mkdirSync(dir, { recursive: true });
  appendFileSync(join(dir, TranscriptName), "", "utf8");
}

/** appendTranscript records one SDK message verbatim, whatever member of the union it is. */
export function appendTranscript(message: unknown, dir = ArtifactsDir): void {
  let line: string;
  try {
    line = JSON.stringify(message);
  } catch (err) {
    line = JSON.stringify({ podium_unserialisable: String(err) });
  }
  appendFileSync(join(dir, TranscriptName), `${line}\n`, "utf8");
}

/**
 * turnSummaryJSON renders the summary. The same document leaves the container twice — as
 * turn.json here, and as the accounting message in report.ts — because artifacts are an
 * optional subsystem and a turn's accounting is not.
 */
export function turnSummaryJSON(summary: TurnSummary): string {
  return `${JSON.stringify(summary, null, 2)}\n`;
}

/** writeTurn writes turn.json. */
export function writeTurn(summary: TurnSummary, dir = ArtifactsDir): void {
  writeFileSync(join(dir, TurnName), turnSummaryJSON(summary), "utf8");
}

/**
 * matchAttachments returns the artifact names the final text names. Nothing is attached
 * that the agent did not name, and nothing named is attached if it does not exist. The
 * names are artifact names — what the collector calls the file — so the relay can look them
 * up in ListArtifacts.
 */
export function matchAttachments(finalText: string, dir = ArtifactsDir): string[] {
  let names: string[];
  try {
    names = readdirSync(dir, { withFileTypes: true })
      .filter((e) => e.isFile() && !runtimeOwned.has(e.name))
      .map((e) => e.name);
  } catch {
    return [];
  }
  return names.filter((name) => namedIn(finalText, name)).sort();
}

/**
 * namedIn looks for the exact file name. The neighbour checks are what stop `shot.png` from
 * matching a mention of `screenshot.png`; trailing punctuation is allowed, because "see
 * report.txt." is how a person writes it.
 */
function namedIn(text: string, name: string): boolean {
  for (let at = text.indexOf(name); at >= 0; at = text.indexOf(name, at + 1)) {
    const before = at === 0 ? "" : (text[at - 1] ?? "");
    const after = text[at + name.length] ?? "";
    if (/[A-Za-z0-9._/-]/.test(before)) {
      continue;
    }
    if (/[A-Za-z0-9]/.test(after)) {
      continue;
    }
    return true;
  }
  return false;
}
