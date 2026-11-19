// Everything a turn says on its way out: stderr for the operator, runner messages for the
// conversation, and the accounting the conductor records.

import { join } from "node:path";

import { ArtifactsDir, turnSummaryJSON, writeTurn, type TurnSummary } from "./artifacts.js";
import { emitMessage, type MessageType, type RunnerInvoke } from "./emit.js";

/** Summary is what the runtime knows about the turn it just ran. */
export interface Summary {
  sessionID: string;
  turnID: string;
  sdkSessionID: string;
  turns: number;
  cost: number;
  /** tokens is summed over the turn's steps, as the harness reported each one. */
  tokens: Tokens;
  code: number;
  startedAt: string;
}

/** Tokens is what a turn's model steps used, summed. */
export interface Tokens {
  input: number;
  output: number;
  reasoning: number;
  cacheRead: number;
  cacheWrite: number;
}

/** noTokens is a turn that has used none yet. */
export function noTokens(): Tokens {
  return { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 };
}

/** addTokens adds one step's tokens to a running total. A field the step did not report adds
 *  nothing: the harness leaves cache out for a provider that has no prompt cache. */
export function addTokens(
  to: Tokens,
  step?: { input?: number; output?: number; reasoning?: number; cache?: { read?: number; write?: number } },
): void {
  if (!step) return;
  to.input += step.input ?? 0;
  to.output += step.output ?? 0;
  to.reasoning += step.reasoning ?? 0;
  to.cacheRead += step.cache?.read ?? 0;
  to.cacheWrite += step.cache?.write ?? 0;
}

export function warn(message: string): void {
  process.stderr.write(`podium-agent: ${message}\n`);
}

/** say never fails the turn: a message that cannot be delivered is reported and dropped. */
export async function say(
  invoke: RunnerInvoke,
  type: MessageType,
  text: string,
  attachments: string[] = [],
): Promise<void> {
  try {
    await emitMessage(type, text, attachments, invoke);
  } catch (err) {
    warn(`could not emit a ${type} message: ${messageOf(err)}`);
  }
}

/**
 * reportTurn is how every turn ends: turn.json, then the answer, then the accounting.
 *
 * The accounting leaves as a message as well as an artifact because the object store is
 * optional (README, Configuration) while `turns.num_turns` and `turns.cost_usd` are not —
 * with no store the artifact is never kept and the accounting was silently lost. The
 * message goes through the runner socket, which no configuration can switch off.
 *
 * It is emitted AFTER the final on purpose: a conductor that restarts mid-turn resumes the
 * stream from the last message it relayed, so only what follows the answer is replayed.
 */
export async function reportTurn(
  invoke: RunnerInvoke,
  s: Summary,
  finalText: string,
  attachments: string[] = [],
  dir = ArtifactsDir,
): Promise<void> {
  const summary: TurnSummary = {
    session_id: s.sessionID,
    turn_id: s.turnID,
    sdk_session_id: s.sdkSessionID,
    num_turns: s.turns,
    total_cost_usd: s.cost,
    input_tokens: s.tokens.input,
    output_tokens: s.tokens.output,
    reasoning_tokens: s.tokens.reasoning,
    cache_read_tokens: s.tokens.cacheRead,
    cache_write_tokens: s.tokens.cacheWrite,
    exit_code: s.code,
    started_at: s.startedAt,
    finished_at: new Date().toISOString(),
  };
  // Never fails the turn: the answer matters more than the file.
  try {
    writeTurn(summary, dir);
  } catch (err) {
    warn(`could not write the turn summary: ${messageOf(err)}`);
  }
  await say(invoke, "final", finalText, attachments);
  await say(invoke, "accounting", turnSummaryJSON(summary));
}

export function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * storeArtifacts hands the named files under dir to the node now, with `podium-runner
 * artifact add`, rather than leaving them for the collection when the container exits. A
 * warm turn reports its answer and then waits for a follow-up, so that collection is the
 * whole window away. The node stores a file once: the collection skips one stored here
 * that has not changed since. Never fails the turn: a file that cannot be handed over now
 * is still collected on the way out.
 */
export async function storeArtifacts(invoke: RunnerInvoke, names: string[], dir = ArtifactsDir): Promise<void> {
  for (const name of names) {
    try {
      await invoke(["artifact", "add", join(dir, name), "--name", name], "");
    } catch (err) {
      warn(`could not store ${name} before answering: ${messageOf(err)}`);
    }
  }
}
