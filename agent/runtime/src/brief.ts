// The turn brief is the contract between the conductor and this runtime. This file is its
// single source of truth: the Go mirror in internal/agent/conductor/brief.go and the golden
// fixture in testdata/brief.example.json both follow it, not the other way round.

import { z } from "zod";

import { MaxSkillNameLen, SkillNameRE } from "./skills.js";

/** BriefEnv holds base64(JSON) of one turn brief on the task spec. */
export const BriefEnv = "PODIUM_AGENT_TURN";

/**
 * MaxArgStrlen is Linux's cap on ONE environment string — MAX_ARG_STRLEN, which the kernel
 * fixes at 32 * PAGE_SIZE, so 128 KiB wherever the page is 4 KiB. Past it a container cannot
 * exec at all, which is why the brief's cap is not a matter of taste: the failure happens
 * before this file ever runs, so nothing reports it. Bisected against a real container, the
 * largest `PODIUM_AGENT_TURN` a `/bin/sh` will exec with is 131053 bytes — that plus
 * `"PODIUM_AGENT_TURN=".length` plus the NUL is exactly 131072.
 */
export const MaxArgStrlen = 32 * 4096;

/**
 * MaxBriefBytes caps the *encoded* brief. The conductor is what truncates a transcript that
 * does not fit — oldest entries first, `transcript_truncated: true` — and the runtime only
 * refuses, because a runtime that silently dropped context would answer the wrong question.
 *
 * It mirrors internal/agent/conductor.MaxBriefBytes and has to: a runtime that accepted more
 * than the conductor emits, or less, is a bug of its own. That file carries the reasoning for
 * the number — a quarter of MaxArgStrlen left spare, and still 72 KiB of JSON once base64 is
 * paid for.
 */
export const MaxBriefBytes = 96 * 1024;

/** ExitBriefInvalid is the runtime's exit code for anything wrong with the brief. */
export const ExitBriefInvalid = 2;

/** ReservedRepoName is the workspace directory Podium itself owns. */
export const ReservedRepoName = ".podium";

/**
 * McpNameRE constrains an MCP server's name. It is the conductor's own rule
 * (internal/agent/mcp.NameRE) and it is re-stated rather than trusted, because the name is
 * written into the harness config as a key and as a tool prefix — `linear` is what
 * `mcp__linear__*` comes from.
 */
export const McpNameRE = /^[a-z][a-z0-9-]{0,31}$/;

/** RepoNameRE constrains repos[].name: it becomes a directory under /workspace. */
export const RepoNameRE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

/**
 * EffortLevels are the levels a model may be asked to think at. The conductor has already
 * refused one the chosen model does not accept, so anything that arrives here is passed
 * straight through to the harness.
 */
export const EffortLevels = ["low", "medium", "high", "xhigh", "max"] as const;

/** BriefError is every way a brief can be unusable. It always means exit 2. */
export class BriefError extends Error {
  readonly exitCode = ExitBriefInvalid;

  constructor(message: string) {
    super(message);
    this.name = "BriefError";
  }
}

const transcriptEntrySchema = z.strictObject({
  role: z.enum(["user", "assistant"]),
  author: z.string(),
  ts: z.string(),
  text: z.string(),
});

const repoSchema = z.strictObject({
  name: z
    .string()
    .regex(RepoNameRE, `must match ${RepoNameRE.source}`)
    // Unreachable while the pattern above rejects a leading dot, and kept anyway: the name
    // becomes a directory next to Podium's own, and that must never be an accident.
    .refine((n) => n !== ReservedRepoName, `may not be ${ReservedRepoName}`),
  url: z.string().min(1),
  default_branch: z.string().min(1),
});

// One Agent Skill the turn may use. It carries a name and a digest and never the bytes: the
// bundle rides in its own environment variable, exactly as a credential does. See skills.ts
// for why, and internal/agent/conductor/brief.go for the other half of the contract.
const skillRefSchema = z.strictObject({
  name: z.string().regex(SkillNameRE, `must match ${SkillNameRE.source}`).max(MaxSkillNameLen),
  // Hex, lower case, 64 characters: sha256 of the bundle document.
  sha256: z.string().regex(/^[0-9a-f]{64}$/, "must be a hex sha256 digest"),
  bundle_env: z.string().min(1),
});

// One playbook a host turn may delegate to. It says what the playbook can REACH rather than
// describing it, because that is what a model choosing between playbooks needs: which one
// has a Docker daemon, which one has the repository checked out.
const delegablePlaybookSchema = z.strictObject({
  name: z.string().min(1),
  summary: z.string().optional(),
  docker: z.boolean().optional(),
  browser: z.boolean().optional(),
  repos: z.array(z.string()).optional(),
});

// One MCP server the turn may use. An address and, when the server needs one, the NAME of
// the environment variable its bearer token arrives in — never the token. How it is
// presented is not carried: the MCP authorization specification says `Authorization: Bearer`
// and that is what writeConfig writes, so there is nothing per-server to describe.
const mcpServerSchema = z.strictObject({
  name: z.string().regex(McpNameRE, `must match ${McpNameRE.source}`),
  url: z.string().min(1),
  token_env: z.string().min(1).optional(),
});

const briefSchema = z.strictObject({
  version: z.literal(1),
  session_id: z.string().min(1),
  turn_id: z.string().min(1),
  source: z.strictObject({
    kind: z.enum(["slack", "linear", "chat"]),
    ref: z.string().min(1),
    url: z.string().optional(),
  }),
  profile: z.strictObject({
    name: z.string().min(1),
    display_name: z.string().min(1),
    system_prompt: z.string(),
    model: z.string().min(1),
    // Absent means the model's own default effort, which is the provider's choice.
    effort: z.enum(EffortLevels).optional(),
  }),
  playbook: z.strictObject({
    name: z.string().min(1),
    system_prompt: z.string(),
    allowed_tools: z.array(z.string()),
    // Absent means NO CAP, which is what the assistant runs with: it answers a conversation
    // and delegates, so the thing worth bounding is the container it starts and not the
    // relay that started it. A cap that killed a conversation mid-answer produced "I ran out
    // of turns" — a failure message for a turn that had not failed.
    //
    // A task always carries one: it runs unattended on a node, where nobody is watching a
    // cursor and the only other bound is the playbook's timeout.
    max_turns: z.number().int().positive().optional(),
    // The Agent Skills this turn may use. Absent means none — and the harness is handed a
    // permission map that denies every skill either way, so "no skills" is a decision
    // this runtime states rather than one it leaves to a default.
    skills: z.array(skillRefSchema).optional(),
    // The MCP servers this turn may use. Absent means none, and none is what the harness
    // gets: writeConfig writes one entry per server here beside the ones the conductor
    // wires up itself, and no others.
    mcp_servers: z.array(mcpServerSchema).optional(),
    // Absent means the turn cannot wait for a human: the default, and what every brief
    // before interactive playbooks described.
    interactive: z.boolean().optional(),
  }),
  transcript: z.array(transcriptEntrySchema),
  transcript_truncated: z.boolean(),
  instruction: z.string().min(1),
  repos: z.array(repoSchema).optional(),
  memory: z
    .strictObject({
      mcp_url: z.string().min(1),
      api_key_env: z.string().min(1),
    })
    .optional(),
  // The headless browser running beside this turn, when the playbook asked for one. An
  // address and nothing else: it is reached over the task's own private network, so unlike
  // memory there is no credential to name.
  browser: z
    .strictObject({
      cdp_url: z.string().min(1),
    })
    .optional(),
  // Which model API serves this turn. It is REQUIRED and it names no vendor in this file:
  // `id` is whatever the harness calls that provider, and together with profile.model it is
  // the whole of "what runs this turn".
  //
  // It carries the NAME of a secret and never a value, exactly as memory does — a brief is
  // an environment variable on a task spec, readable by anything that can read the spec.
  provider: z.strictObject({
    // id is the harness's provider id: "anthropic", "xai". Paired with profile.model it
    // becomes the harness's `provider/model`.
    id: z.string().min(1),
    // api_key_env names the environment variable the conductor put the credential in.
    api_key_env: z.string().min(1),
    // base_url overrides where that provider is reached — an egress proxy, or a test seam.
    // Absent means the harness's own default for the provider.
    base_url: z.string().optional(),
  }),
  // How this turn reaches a container, present only on a HOST turn: the conductor's own
  // address, the variable holding the token that authorises this turn to use it, and the
  // playbooks it may ask for.
  //
  // A TASK's brief never carries this, which is what stops a delegated task delegating
  // again. And `playbooks` is the allow-list as well as the menu: the conductor refuses a
  // name that is not on it, so the two cannot drift apart.
  delegation: z
    .strictObject({
      url: z.string().min(1),
      token_env: z.string().min(1),
      playbooks: z.array(delegablePlaybookSchema).min(1),
    })
    .optional(),
  // Where this turn is running. Absent means a task container, which is what every brief
  // before host turns described; "host" means a child process of the conductor, with no
  // container around it, no repository checked out and nothing collecting its files. The
  // prompt says different things in the two cases and must not have to guess which it is.
  runs_on: z.enum(["task", "host"]).optional(),
});

export type TurnBrief = z.infer<typeof briefSchema>;
export type TranscriptEntry = z.infer<typeof transcriptEntrySchema>;
export type RepoRef = z.infer<typeof repoSchema>;
export type SkillRef = z.infer<typeof skillRefSchema>;
export type McpServerRef = z.infer<typeof mcpServerSchema>;
export type SourceKind = TurnBrief["source"]["kind"];
export type ProviderRef = TurnBrief["provider"];
export type DelegationRef = NonNullable<TurnBrief["delegation"]>;

/** onHost reports whether this turn is a child of the conductor rather than a container. */
export function onHost(brief: TurnBrief): boolean {
  return brief.runs_on === "host";
}

export type DelegablePlaybook = z.infer<typeof delegablePlaybookSchema>;

/**
 * decodeBrief reads, decodes and validates the brief. Every failure is a BriefError, and
 * every BriefError is exit 2: a misspelt field must not silently change a turn, which is
 * why the schema is strict about unknown keys the way pkg/spec is about unknown YAML.
 */
export function decodeBrief(env: NodeJS.ProcessEnv): TurnBrief {
  const encoded = env[BriefEnv];
  if (encoded === undefined || encoded === "") {
    throw new BriefError(`${BriefEnv} is not set`);
  }

  const size = Buffer.byteLength(encoded, "utf8");
  if (size > MaxBriefBytes) {
    throw new BriefError(`turn brief is ${size} bytes; the limit is ${MaxBriefBytes}`);
  }

  const text = Buffer.from(encoded, "base64").toString("utf8");
  let raw: unknown;
  try {
    raw = JSON.parse(text) as unknown;
  } catch (err) {
    throw new BriefError(`${BriefEnv} is not base64-encoded JSON: ${messageOf(err)}`);
  }

  // Checked before the schema so the message can name what arrived: a version this runtime
  // does not speak is the one brief failure an operator can act on without reading a schema.
  const version = (raw as { version?: unknown } | null)?.version;
  if (version !== 1) {
    throw new BriefError(
      `turn brief version is ${JSON.stringify(version) ?? "absent"}; this runtime speaks version 1`,
    );
  }

  const parsed = briefSchema.safeParse(raw);
  if (!parsed.success) {
    throw new BriefError(`turn brief does not validate: ${formatIssues(parsed.error)}`);
  }
  return parsed.data;
}

function formatIssues(error: z.ZodError): string {
  return error.issues
    .map((issue) => {
      const path = issue.path.map((p) => String(p)).join(".");
      return path === "" ? issue.message : `${path}: ${issue.message}`;
    })
    .join("; ");
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
