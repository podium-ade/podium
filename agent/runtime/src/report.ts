// Everything a turn says on its way out: stderr for the operator, runner messages for the
// conversation, and the accounting the conductor records.

import { ArtifactsDir, turnSummaryJSON, writeTurn, type TurnSummary } from "./artifacts.js";
import { emitMessage, type MessageType, type RunnerInvoke } from "./emit.js";

/** Summary is what the runtime knows about the turn it just ran. */
export interface Summary {
  sessionID: string;
  turnID: string;
  sdkSessionID: string;
  turns: number;
  cost: number;
  code: number;
  startedAt: string;
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
