// The entrypoint of the agent runtime image: decode one turn brief, run one agentic turn,
// report what the agent said through podium-runner, leave a transcript and a summary as
// artifacts, exit with a code that says how the turn ended.
//
// Exit codes are the whole external contract (00-index.md#agentic-host-track):
//   0  the turn finished (including a cancelled turn)
//   2  the brief was invalid
//   3  max turns reached
//   4  SDK or API error, a missing key, a failed clone, a network failure

import { setTimeout as sleep } from "node:timers/promises";

// Type-only, so the SDK is not loaded when the module is evaluated. `query` arrives through
// a dynamic import in runTurn: importing the SDK costs ~100ms of the runtime's ~160ms
// start-up, and every millisecond before the SIGTERM handler below is installed is a
// millisecond in which a cancel kills the process outright instead of ending the turn
// tidily. A dry run never loads it at all.
import type { Options, SDKMessage } from "@anthropic-ai/claude-agent-sdk";

import { ArtifactsDir, appendTranscript, ensureArtifacts, matchAttachments } from "./artifacts.js";
import { BriefEnv, BriefError, ExitBriefInvalid, decodeBrief, type TurnBrief } from "./brief.js";
import { runnerInvoke, type RunnerInvoke } from "./emit.js";
import { MemoryTools, buildSystemPrompt } from "./prompt.js";
import { messageOf, reportTurn, say, warn, type Summary } from "./report.js";
import { CloneError, TokenEnv, WorkspaceDir, cloneRepos, redact } from "./repos.js";

const ExitOK = 0;
const ExitMaxTurns = 3;
const ExitSDKError = 4;

/** DryRunEnv is the test seam every later step's e2e uses. */
const DryRunEnv = "PODIUM_AGENT_DRY_RUN";

/** DryRunSleepEnv and DryRunExitEnv are TEST-ONLY and honoured only in dry run. */
const DryRunSleepEnv = "PODIUM_AGENT_DRY_RUN_SLEEP_MS";
const DryRunExitEnv = "PODIUM_AGENT_DRY_RUN_EXIT";

/** KeyEnv is the only auth path: a Podium secret with target: env. */
const KeyEnv = "ANTHROPIC_API_KEY";

/** ProgressWindowMs coalesces progress messages: never two inside this window. */
const ProgressWindowMs = 5_000;

const CancelledText = "cancelled before finishing";

async function main(): Promise<number> {
  const startedAt = new Date().toISOString();

  // First, before anything that can block: the runner forwards SIGTERM
  // (docs/runner-events.md#signals-and-exit-codes) and it can arrive at any moment,
  // including while the brief is still being decoded. A cancelled turn says so, leaves its
  // artifacts, and exits 0 — the command exited by itself, and `podium task cancel` is what
  // marks the task cancelled.
  const controller = new AbortController();
  let cancelled = false;
  process.on("SIGTERM", () => {
    cancelled = true;
    controller.abort();
  });

  const invoke = runnerInvoke();

  // Before the brief, so that even an unusable brief leaves a summary behind.
  try {
    ensureArtifacts();
  } catch (err) {
    warn(`${ArtifactsDir} is not usable, so this turn will leave nothing behind: ${messageOf(err)}`);
  }

  let brief: TurnBrief;
  try {
    brief = decodeBrief(process.env);
  } catch (err) {
    const why = err instanceof BriefError ? err.message : messageOf(err);
    warn(why);
    await reportTurn(
      invoke,
      { sessionID: "", turnID: "", sdkSessionID: "", turns: 0, cost: 0, code: ExitBriefInvalid, startedAt },
      `turn brief is invalid: ${why}`,
    );
    return ExitBriefInvalid;
  }

  const summary = {
    sessionID: brief.session_id,
    turnID: brief.turn_id,
    sdkSessionID: "",
    turns: 0,
    cost: 0,
    code: ExitOK,
    startedAt,
  };

  const dry = process.env[DryRunEnv] === "1";
  // One line on stderr, before anything slow: it is a task log chunk, so it is the only
  // evidence an operator has that the runtime started at all, and it names nothing from the
  // brief but the two IDs the conductor already knows.
  warn(`turn ${brief.turn_id} of session ${brief.session_id} starting${dry ? " (dry run)" : ""}`);

  if (dry) {
    return dryRun(brief, invoke, controller, () => cancelled, summary);
  }

  if (brief.memory && !secretFromEnv(brief.memory.api_key_env)) {
    const why = `turn brief names memory env ${brief.memory.api_key_env} but it is not set`;
    warn(why);
    summary.code = ExitBriefInvalid;
    await reportTurn(invoke, summary, `turn brief is invalid: ${why}`);
    return ExitBriefInvalid;
  }

  if (!secretFromEnv(KeyEnv)) {
    const why =
      `${KeyEnv} is not set. It reaches this container only as a Podium secret with ` +
      `target: env, named on the skill that submitted this task.`;
    warn(why);
    summary.code = ExitSDKError;
    await reportTurn(invoke, summary, `I could not start: ${why}`);
    return ExitSDKError;
  }

  const token = secretFromEnv(TokenEnv);
  if (token !== undefined) {
    // gh reads GH_TOKEN; git reads GITHUB_TOKEN through the credential helper repos.ts
    // installs. Both end up in the SDK's environment, so the agent's Bash calls work.
    process.env.GH_TOKEN ??= token;
  }

  if (brief.repos && brief.repos.length > 0) {
    try {
      cloneRepos(brief.repos, { token });
    } catch (err) {
      const why = err instanceof CloneError ? err.message : redact(messageOf(err), token);
      warn(why);
      summary.code = ExitSDKError;
      await reportTurn(invoke, summary, `I could not check the repositories out: ${why}`);
      return ExitSDKError;
    }
  }

  let finalText = "";
  let held = "";
  let lastProgressAt = 0;

  const tryProgress = async (): Promise<void> => {
    if (held.trim() === "") {
      return;
    }
    // Hold and replace: inside the window the newer text supersedes the older one rather
    // than adding a second message nobody asked for.
    if (Date.now() - lastProgressAt < ProgressWindowMs) {
      return;
    }
    const text = held;
    held = "";
    lastProgressAt = Date.now();
    await say(invoke, "progress", text);
  };

  try {
    const { query } = await import("@anthropic-ai/claude-agent-sdk");
    for await (const message of query({ prompt: brief.instruction, options: options(brief, controller) })) {
      appendTranscriptSafely(message);

      if (message.type === "assistant") {
        const text = assistantText(message);
        if (text !== "") {
          await tryProgress();
          held = text;
        }
        if (assistantUsesATool(message)) {
          await tryProgress();
        }
        continue;
      }

      if (message.type !== "result") {
        continue;
      }

      summary.sdkSessionID = message.session_id;
      summary.turns = message.num_turns;
      summary.cost = message.total_cost_usd;
      if (message.subtype === "success") {
        finalText = message.result;
        if (message.is_error) {
          // subtype success with is_error means the turn ended on an API error and `result`
          // is the error text. It is still the thing to relay.
          summary.code = ExitSDKError;
        }
      } else if (message.subtype === "error_max_turns") {
        summary.code = ExitMaxTurns;
        finalText =
          `I ran out of turns. This skill allows ${brief.skill.max_turns} and the work was ` +
          `not finished, so nothing here is a complete answer.`;
      } else {
        summary.code = ExitSDKError;
        finalText = `The turn failed before I could answer (${message.subtype}).${errorDetail(message.errors)}`;
      }
    }
  } catch (err) {
    if (!cancelled) {
      summary.code = ExitSDKError;
      finalText = `The turn failed before I could answer: ${redact(messageOf(err), token)}`;
    }
  }

  if (cancelled) {
    summary.code = ExitOK;
    finalText = CancelledText;
  }
  if (finalText.trim() === "") {
    finalText = "The turn ended without an answer.";
  }

  await reportTurn(invoke, summary, finalText, matchAttachments(finalText));
  return summary.code;
}

function options(brief: TurnBrief, controller: AbortController): Options {
  const env = { ...process.env };
  // The brief can be a quarter of a megabyte and the SDK has no business with it.
  delete env[BriefEnv];

  const allowedTools = [...brief.skill.allowed_tools];
  const mcpServers: NonNullable<Options["mcpServers"]> = {};
  if (brief.memory) {
    mcpServers.memory = {
      type: "http",
      url: brief.memory.mcp_url,
      // Hindsight takes the same header for MCP and REST, and accepts a bare token as well
      // as a Bearer one. Verified against hindsight 0.9.2 on 2026-09-03.
      headers: { Authorization: `Bearer ${secretFromEnv(brief.memory.api_key_env) ?? ""}` },
    };
    for (const tool of MemoryTools) {
      if (!allowedTools.includes(tool)) {
        allowedTools.push(tool);
      }
    }
  }

  return {
    // A plain string, never the claude_code preset: the runtime's own block is the whole
    // operating contract for a turn and the preset's interactive assumptions do not hold.
    systemPrompt: buildSystemPrompt(brief),
    cwd: WorkspaceDir,
    model: brief.profile.model,
    maxTurns: brief.skill.max_turns,
    allowedTools,
    disallowedTools: [],
    // The one permission decision in this runtime, and the only place it is made.
    //
    // The run is headless: there is no human to answer a prompt, so any mode that could
    // block on one would hang the task until its timeout. The sandbox is the task
    // container itself — every capability dropped, no-new-privileges, a private network, a
    // fresh workspace (docs/security.md#3-a-task-container--untrusted) — and the policy
    // knob is the skill's tool allow-list above, which is what decides what the agent can
    // reach at all. This is the same trust decision Podium already makes for every task's
    // command. A half-measure like acceptEdits would still block on a Bash call.
    permissionMode: "bypassPermissions",
    mcpServers,
    // Nothing is read from the image's filesystem: no ~/.claude, no .claude/settings.json,
    // no CLAUDE.md out of a cloned repo. A repository could plant one.
    settingSources: [],
    env,
    abortController: controller,
    includePartialMessages: false,
  };
}

async function dryRun(
  brief: TurnBrief,
  invoke: RunnerInvoke,
  controller: AbortController,
  cancelled: () => boolean,
  summary: Summary,
): Promise<number> {
  summary.sdkSessionID = "dry-run";

  const sleepMs = positiveInt(process.env[DryRunSleepEnv]);
  if (sleepMs > 0) {
    try {
      await sleep(sleepMs, undefined, { signal: controller.signal });
    } catch {
      // Aborted. cancelled() below decides what that means.
    }
  }

  if (cancelled()) {
    await reportTurn(invoke, summary, CancelledText);
    return ExitOK;
  }

  summary.code = positiveInt(process.env[DryRunExitEnv]);
  await reportTurn(invoke, summary, `dry run: ${brief.instruction}`);
  return summary.code;
}

function appendTranscriptSafely(message: SDKMessage): void {
  try {
    appendTranscript(message);
  } catch (err) {
    warn(`could not record an SDK message: ${messageOf(err)}`);
  }
}

/**
 * assistantText joins the text blocks of one assistant message. The SDK message union has
 * dozens of members and grows with every release, so nothing here switches over it: the
 * progress heuristic keys on `assistant` and ignores everything else.
 */
function assistantText(message: { message: { content: unknown } }): string {
  const parts: string[] = [];
  for (const block of contentBlocks(message)) {
    if (block.type === "text" && typeof block.text === "string") {
      parts.push(block.text);
    }
  }
  return parts.join("\n").trim();
}

function assistantUsesATool(message: { message: { content: unknown } }): boolean {
  return contentBlocks(message).some((b) => b.type === "tool_use");
}

function contentBlocks(message: { message: { content: unknown } }): { type?: string; text?: unknown }[] {
  const content = message.message.content;
  return Array.isArray(content) ? (content as { type?: string; text?: unknown }[]) : [];
}

function errorDetail(errors: string[] | undefined): string {
  if (errors === undefined || errors.length === 0) {
    return "";
  }
  return ` ${errors.join("; ")}`;
}

/** secretFromEnv treats an empty variable as absent: the node injects one or it does not. */
function secretFromEnv(name: string): string | undefined {
  const value = process.env[name];
  return value === undefined || value === "" ? undefined : value;
}

function positiveInt(raw: string | undefined): number {
  if (raw === undefined) {
    return 0;
  }
  const n = Number.parseInt(raw, 10);
  return Number.isFinite(n) && n > 0 ? n : 0;
}

const code = await main();
process.exit(code);
