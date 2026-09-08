import { readFileSync } from "node:fs";

import { describe, expect, it } from "vitest";

import { BriefEnv, decodeBrief, type TurnBrief } from "./brief.js";
import { buildSystemPrompt } from "./prompt.js";

function golden(): TurnBrief {
  const raw = readFileSync(new URL("../testdata/brief.example.json", import.meta.url), "utf8");
  return decodeBrief({ [BriefEnv]: Buffer.from(raw, "utf8").toString("base64") });
}

describe("buildSystemPrompt", () => {
  it("renders the whole prompt for a brief with memory and repos", () => {
    expect(buildSystemPrompt(golden())).toMatchSnapshot();
  });

  it("is deterministic", () => {
    const brief = golden();
    expect(buildSystemPrompt(brief)).toBe(buildSystemPrompt(brief));
  });

  it("leaves the memory block out when the brief has no memory", () => {
    const brief = golden();
    expect(buildSystemPrompt(brief)).toContain("# Shared memory");
    const { memory: _memory, ...rest } = brief;
    const prompt = buildSystemPrompt(rest as TurnBrief);
    expect(prompt).not.toContain("# Shared memory");
    expect(prompt).not.toContain("mcp__memory__recall");
  });

  it("leaves the repositories block out when the brief has no repos", () => {
    const brief = { ...golden(), repos: [] };
    expect(buildSystemPrompt(brief)).not.toContain("# Repositories");
  });

  it("marks a truncated transcript and not a whole one", () => {
    const brief = golden();
    expect(buildSystemPrompt(brief)).toContain("(earlier messages omitted)");
    expect(buildSystemPrompt({ ...brief, transcript_truncated: false })).not.toContain(
      "(earlier messages omitted)",
    );
  });

  it("says so when there is no transcript at all", () => {
    const brief = { ...golden(), transcript: [], transcript_truncated: false };
    expect(buildSystemPrompt(brief)).toContain("(no earlier messages; this is the first turn)");
  });

  it("names the source the answer is posted back to", () => {
    const brief = golden();
    expect(buildSystemPrompt(brief)).toContain("the Slack thread this came from");
    expect(buildSystemPrompt({ ...brief, source: { kind: "chat", ref: "chat_1" } })).toContain(
      "the web chat this came from",
    );
    expect(buildSystemPrompt({ ...brief, source: { kind: "linear", ref: "ENG-1" } })).toContain(
      "the Linear issue this came from",
    );
  });

  it("asks a first chat turn to name the conversation, and not a later one", () => {
    const first = {
      ...golden(),
      source: { kind: "chat" as const, ref: "chat_1" },
      transcript: [{ role: "user" as const, author: "alice", ts: "2026-09-03T10:00:00Z", text: "hi" }],
    };
    expect(buildSystemPrompt(first)).toContain("chat-title.txt");
    const later = {
      ...first,
      transcript: [
        ...first.transcript,
        { role: "assistant" as const, author: "Podium", ts: "2026-09-03T10:00:01Z", text: "hello" },
      ],
    };
    expect(buildSystemPrompt(later)).not.toContain("chat-title.txt");
    expect(buildSystemPrompt(golden())).not.toContain("chat-title.txt");
  });

  it("does not tell a host turn it has a container, because it has not got one", () => {
    const brief = golden();
    const task = buildSystemPrompt(brief);
    const host = buildSystemPrompt({ ...brief, runs_on: "host", repos: undefined });

    expect(task).toContain("disposable Linux container");
    expect(host).not.toContain("disposable Linux container");
    expect(host).toContain("NOT in a container");
    // The three promises a host turn cannot keep: a workspace, files that get collected,
    // and a shell.
    expect(host).not.toContain("/workspace/.podium/artifacts");
    expect(host.replace(/\s+/g, " ")).toContain("a file you write goes nowhere");
    expect(host).toContain("What you do NOT have");
  });

  it("renders the delegation menu from the brief, and nothing else", () => {
    const brief = {
      ...golden(),
      runs_on: "host" as const,
      delegation: {
        url: "http://127.0.0.1:8090",
        token_env: "PODIUM_TURN_TOKEN",
        playbooks: [
          { name: "podium", summary: "develops Podium itself", docker: true, repos: ["podium"] },
          { name: "general", browser: true },
        ],
      },
    };
    const prompt = buildSystemPrompt(brief);
    expect(prompt).toContain("# Delegating work");
    expect(prompt).toContain("`podium`");
    expect(prompt).toContain("repositories: podium");
    expect(prompt).toContain("a Docker daemon");
    expect(prompt).toContain("develops Podium itself");
    expect(prompt).toContain("`general`");
    expect(prompt).toContain("a browser");
    // The address and the token's variable are the runtime's business, not the model's:
    // a prompt that names them is a prompt that invites the model to use them directly.
    expect(prompt).not.toContain("http://127.0.0.1:8090");
    expect(prompt).not.toContain("PODIUM_TURN_TOKEN");
  });

  it("tells a delegating turn not to repeat what the task already said", () => {
    // This is the behaviour that reads worst in a conversation: the task's answer arrives
    // on its own, and then the agent paraphrases it as if it had done the work.
    const prompt = buildSystemPrompt({
      ...golden(),
      runs_on: "host" as const,
      delegation: {
        url: "http://h",
        token_env: "T",
        playbooks: [{ name: "podium" }],
      },
    });
    // Whitespace-normalised, because the prompt is wrapped for a human to read and the
    // sentence being asserted spans a line break.
    const flat = prompt.replace(/\s+/g, " ");
    expect(flat).toContain("appear in this conversation **on their own**");
    expect(flat).toContain("do not repeat what it says");
  });

  // Polling is what used to exhaust the turn cap: the prompt told the model to poll for the
  // outcome, so a turn that delegated once spent the rest of its steps watching and then
  // died with "I ran out of turns" while the task was still working perfectly well.
  it("tells a delegating turn to stop rather than poll", () => {
    const prompt = buildSystemPrompt({
      ...golden(),
      runs_on: "host" as const,
      delegation: { url: "http://h", token_env: "T", playbooks: [{ name: "podium" }] },
    });
    const flat = prompt.replace(/\s+/g, " ");
    expect(flat).toContain("Delegate, then stop");
    expect(flat).toContain("Do not poll");
    expect(flat).not.toContain("Poll `podium_check_delegation` for the outcome");
  });

  // The assistant has no repository and no shell, so working anything out about code here is
  // a guess that costs a turn and gets redone by the task.
  it("tells a host turn to delegate rather than investigate", () => {
    const prompt = buildSystemPrompt({ ...golden(), runs_on: "host" as const });
    const flat = prompt.replace(/\s+/g, " ");
    expect(flat).toContain("Do not investigate first");
    expect(flat).toContain("Hand it the question, not your answer to it");
  });

  it("tells a delegating turn not to deny an answer the reader can already see", () => {
    // Observed on a live stack: the delegated task answered, its answer was relayed into the
    // chat, and the host turn's own last message then said it "did not receive the result".
    // The reader saw the answer and a denial of it, one after the other, with nothing having
    // actually failed.
    const prompt = buildSystemPrompt({
      ...golden(),
      runs_on: "host" as const,
      delegation: {
        url: "http://h",
        token_env: "T",
        playbooks: [{ name: "podium" }],
      },
    });
    const flat = prompt.replace(/\s+/g, " ");
    expect(flat).toContain("never end a turn by apologising for not having it");
    expect(flat).toContain("say that it is running and that its answer will follow");
  });

  it("says nothing about delegating on a task's turn, which cannot", () => {
    expect(buildSystemPrompt(golden())).not.toContain("# Delegating work");
  });

  it("puts the profile before the playbook before the runtime block", () => {
    const prompt = buildSystemPrompt(golden());
    const profile = prompt.indexOf("You are Podium, the engineering team's agent.");
    const playbook = prompt.indexOf("You write code, verify it");
    const runtime = prompt.indexOf("# This turn");
    const transcript = prompt.indexOf("# The conversation so far");
    expect(profile).toBe(0);
    expect(playbook).toBeGreaterThan(profile);
    expect(runtime).toBeGreaterThan(playbook);
    expect(transcript).toBeGreaterThan(runtime);
  });
});
