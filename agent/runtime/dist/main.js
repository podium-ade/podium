// The entrypoint of the agent runtime image: decode one turn brief, run one agentic turn,
// report what the agent said through podium-runner, leave a transcript and a summary as
// artifacts, exit with a code that says how the turn ended.
//
// Exit codes are the whole external contract (00-index.md#agentic-host-track):
//   0  the turn finished (including a cancelled turn)
//   2  the brief was invalid
//   3  max turns reached
//   4  harness or API error, a missing key, a failed clone, a network failure
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { ArtifactsDir, appendTranscript, ensureArtifacts, matchAttachments } from "./artifacts.js";
import { BriefEnv, BriefError, ExitBriefInvalid, decodeBrief, onHost } from "./brief.js";
import { runnerInvoke } from "./emit.js";
import * as oc from "./opencode.js";
import { buildSystemPrompt } from "./prompt.js";
import { messageOf, reportTurn, say, warn } from "./report.js";
import { CloneError, TokenEnv, artifactsUnder, cloneRepos, redact, workspaceOf, } from "./repos.js";
import { SkillError, installSkills } from "./skills.js";
const ExitOK = 0;
const ExitMaxTurns = 3;
const ExitHarnessError = 4;
/**
 * mcpEntrypoint is this runtime's OTHER entrypoint, beside the one now running: the MCP
 * server a host turn delegates through (mcp.ts). It is resolved from this module's own
 * location rather than configured, because the two are built together and shipped together —
 * a path in the environment would be one more thing to get wrong on a host, and it would be
 * wrong in exactly the way that leaves a turn with no delegation tools and no explanation.
 */
export function mcpEntrypoint(here = import.meta.url) {
    return fileURLToPath(new URL("./mcp.js", here));
}
/** DryRunEnv is the test seam every later step's e2e uses. */
const DryRunEnv = "PODIUM_AGENT_DRY_RUN";
/** DryRunSleepEnv and DryRunExitEnv are TEST-ONLY and honoured only in dry run. */
const DryRunSleepEnv = "PODIUM_AGENT_DRY_RUN_SLEEP_MS";
const DryRunExitEnv = "PODIUM_AGENT_DRY_RUN_EXIT";
/** ProgressWindowMs coalesces progress messages: never two inside this window. */
const ProgressWindowMs = 5_000;
const CancelledText = "cancelled before finishing";
async function main() {
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
    // Where this turn's files go. It starts as the TASK's directory, because this happens
    // before the brief is decoded — deliberately, so that even an unusable brief leaves a
    // summary behind — and a host turn re-points it below once the brief says so. On a host
    // the attempt here fails and says so on stderr, which is accurate and harmless: nothing
    // collects a host turn's files, so there is nothing to leave behind.
    let artifacts = ArtifactsDir;
    try {
        ensureArtifacts(artifacts);
    }
    catch (err) {
        warn(`${artifacts} is not usable, so this turn will leave nothing behind: ${messageOf(err)}`);
    }
    let brief;
    try {
        brief = decodeBrief(process.env);
    }
    catch (err) {
        const why = err instanceof BriefError ? err.message : messageOf(err);
        warn(why);
        await reportTurn(invoke, { sessionID: "", turnID: "", sdkSessionID: "", turns: 0, cost: 0, code: ExitBriefInvalid, startedAt }, `turn brief is invalid: ${why}`);
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
    // The workspace, and the artifacts directory inside it. A host turn has no /workspace:
    // the conductor forked this process in a jail and made that the working directory, and
    // handing the harness a directory that does not exist kills the turn before its first
    // request ("Failed to change directory to /workspace").
    const workspace = workspaceOf(brief);
    if (onHost(brief)) {
        artifacts = artifactsUnder(workspace);
        try {
            ensureArtifacts(artifacts);
        }
        catch (err) {
            warn(`${artifacts} is not usable: ${messageOf(err)}`);
        }
    }
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
    const keyEnv = brief.provider.api_key_env;
    if (!secretFromEnv(keyEnv)) {
        const why = `${keyEnv} is not set, and this turn runs on ${brief.provider.id}, which needs it. It ` +
            `reaches this container only as a Podium secret with target: env, which the conductor ` +
            `attaches from the backend the playbook runs on.`;
        warn(why);
        summary.code = ExitHarnessError;
        await reportTurn(invoke, summary, `I could not start: ${why}`);
        return ExitHarnessError;
    }
    const token = secretFromEnv(TokenEnv);
    if (token !== undefined) {
        // gh reads GH_TOKEN; git reads GITHUB_TOKEN through the credential helper repos.ts
        // installs. Both end up in the harness's environment, so the agent's shell calls work.
        process.env.GH_TOKEN ??= token;
    }
    if (brief.repos && brief.repos.length > 0) {
        try {
            cloneRepos(brief.repos, { token });
        }
        catch (err) {
            const why = err instanceof CloneError ? err.message : redact(messageOf(err), token);
            warn(why);
            summary.code = ExitHarnessError;
            await reportTurn(invoke, summary, `I could not check the repositories out: ${why}`);
            return ExitHarnessError;
        }
    }
    // The playbook's Agent Skills, unpacked where the harness will find them. A bundle that
    // fails any of its checks fails the turn: the alternative is a turn that runs with fewer
    // skills than the playbook describes and says nothing about it.
    const skillRefs = brief.playbook.skills ?? [];
    if (skillRefs.length > 0) {
        try {
            const names = installSkills(skillRefs);
            warn(`installed ${names.length} agent skill(s): ${names.join(", ")}`);
        }
        catch (err) {
            const why = err instanceof SkillError ? err.message : messageOf(err);
            warn(why);
            summary.code = ExitHarnessError;
            await reportTurn(invoke, summary, `I could not install this playbook's skills: ${why}`);
            return ExitHarnessError;
        }
    }
    let finalText = "";
    let held = "";
    let lastProgressAt = 0;
    const tryProgress = async () => {
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
    // The config for this turn: the operating contract, the tool allow-list, the provider and
    // the memory server. It lives in a temporary directory rather than the workspace, because
    // the workspace is a repository the agent may commit and nobody wants a turn's config in
    // a pull request.
    const configDir = mkdtempSync(join(tmpdir(), "podium-turn-"));
    // Resolved before the config is written, because the config is what the harness reads and
    // the name only resolves from inside this container. See resolveBrowserURL.
    const browserCDP = brief.browser ? await oc.resolveBrowserURL(brief.browser.cdp_url) : undefined;
    try {
        oc.writeConfig({
            dir: configDir,
            systemPrompt: buildSystemPrompt(brief),
            tools: brief.playbook.allowed_tools,
            providerID: brief.provider.id,
            baseURL: brief.provider.base_url,
            memory: brief.memory
                ? { url: brief.memory.mcp_url, apiKeyEnv: brief.memory.api_key_env }
                : undefined,
            browser: browserCDP ? { cdpURL: browserCDP } : undefined,
            skills: skillRefs.map((s) => s.name),
            delegation: brief.delegation
                ? { url: brief.delegation.url, entry: mcpEntrypoint() }
                : undefined,
        });
    }
    catch (err) {
        const why = messageOf(err);
        warn(why);
        summary.code = ExitHarnessError;
        await reportTurn(invoke, summary, `I could not start: ${why}`);
        return ExitHarnessError;
    }
    const env = { ...process.env };
    // The brief can be 96 KiB and the harness has no business with it. The
    // skill bundles are the same: they are already on disk where the harness looks, so what
    // is left in the environment is only a copy for the agent's own `env` to print.
    delete env[BriefEnv];
    for (const ref of skillRefs) {
        delete env[ref.bundle_env];
    }
    let steps = 0;
    try {
        const run = oc.start({
            configDir,
            workdir: workspace,
            providerID: brief.provider.id,
            model: brief.profile.model,
            effort: brief.profile.effort,
            instruction: brief.instruction,
            env,
        });
        // A cancel forwards to the harness rather than killing this process, so the turn still
        // reports what it managed and leaves its artifacts.
        const stop = () => run.child.kill("SIGTERM");
        controller.signal.addEventListener("abort", stop, { once: true });
        // stderr is the harness's own diagnostics. It goes to the task log — which is where an
        // operator looks — and never into the answer.
        run.child.stderr.on("data", (chunk) => warn(redact(chunk.toString().trimEnd(), token)));
        for await (const { event, raw } of oc.events(run.child)) {
            appendTranscriptSafely(raw, artifacts);
            switch (event.type) {
                case "step_start":
                    steps += 1;
                    if (steps > brief.playbook.max_turns) {
                        // The harness has no turn cap of its own, so this is the cap: stop it, and say
                        // plainly that the answer is incomplete rather than relaying a half-finished one.
                        summary.code = ExitMaxTurns;
                        finalText =
                            `I ran out of turns. This playbook allows ${brief.playbook.max_turns} and the work ` +
                                `was not finished, so nothing here is a complete answer.`;
                        run.child.kill("SIGTERM");
                    }
                    break;
                case "text": {
                    const text = (event.part?.text ?? "").trim();
                    if (text !== "") {
                        await tryProgress();
                        // The last text of the turn is the answer; the ones before it are progress.
                        // Which is which is only known when the stream ends, so every one is held.
                        held = text;
                        finalText = text;
                    }
                    break;
                }
                case "tool_use":
                    // A tool call is a sign of life rather than something to say, so it only releases
                    // whatever text is already held.
                    await tryProgress();
                    break;
                case "step_finish": {
                    const part = event.part;
                    summary.turns = steps;
                    summary.cost += part?.cost ?? 0;
                    if (event.sessionID) {
                        summary.sdkSessionID = event.sessionID;
                    }
                    break;
                }
                default:
                    break;
            }
        }
        const code = await exitOf(run.child);
        // A non-zero exit with an answer already in hand is a harness that failed after saying
        // something useful; the answer is still the thing to relay, and the code says it ended
        // badly. With no answer at all there is nothing to relay but the failure.
        if (code !== 0 && summary.code === ExitOK && !cancelled) {
            summary.code = ExitHarnessError;
            if (finalText.trim() === "") {
                finalText = `The turn failed before I could answer: the harness exited ${code}.`;
            }
        }
    }
    catch (err) {
        if (!cancelled) {
            summary.code = ExitHarnessError;
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
    await reportTurn(invoke, summary, finalText, matchAttachments(finalText, artifacts), artifacts);
    return summary.code;
}
async function dryRun(brief, invoke, controller, cancelled, summary) {
    summary.sdkSessionID = "dry-run";
    const sleepMs = positiveInt(process.env[DryRunSleepEnv]);
    if (sleepMs > 0) {
        try {
            await sleep(sleepMs, undefined, { signal: controller.signal });
        }
        catch {
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
/**
 * appendTranscriptSafely writes one harness event to the transcript, raw. The line is kept
 * exactly as it arrived rather than re-serialised: the transcript is evidence of what the
 * harness said, and a re-serialisation is evidence of what this runtime understood of it.
 */
function appendTranscriptSafely(raw, dir) {
    try {
        appendTranscript(raw, dir);
    }
    catch (err) {
        warn(`could not record a harness event: ${messageOf(err)}`);
    }
}
/** exitOf resolves with the process's exit code, treating a signalled death as an exit. */
function exitOf(child) {
    return new Promise((resolve) => {
        child.on("close", (code) => resolve(code ?? 0));
    });
}
/** secretFromEnv treats an empty variable as absent: the node injects one or it does not. */
function secretFromEnv(name) {
    const value = process.env[name];
    return value === undefined || value === "" ? undefined : value;
}
function positiveInt(raw) {
    if (raw === undefined) {
        return 0;
    }
    const n = Number.parseInt(raw, 10);
    return Number.isFinite(n) && n > 0 ? n : 0;
}
const code = await main();
process.exit(code);
