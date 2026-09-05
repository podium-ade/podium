// The turn brief is the contract between the conductor and this runtime. This file is its
// single source of truth: the Go mirror in internal/agent/conductor/brief.go and the golden
// fixture in testdata/brief.example.json both follow it, not the other way round.

import { z } from "zod";

/** BriefEnv holds base64(JSON) of one turn brief on the task spec. */
export const BriefEnv = "PODIUM_AGENT_TURN";

/**
 * MaxBriefBytes caps the *encoded* brief. The conductor is what truncates a transcript that
 * does not fit — oldest entries first, `transcript_truncated: true` — and the runtime only
 * refuses, because a runtime that silently dropped context would answer the wrong question.
 */
export const MaxBriefBytes = 256 * 1024;

/** ExitBriefInvalid is the runtime's exit code for anything wrong with the brief. */
export const ExitBriefInvalid = 2;

/** ReservedRepoName is the workspace directory Podium itself owns. */
export const ReservedRepoName = ".podium";

/** RepoNameRE constrains repos[].name: it becomes a directory under /workspace. */
export const RepoNameRE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

/**
 * AgentKinds are the backends a turn can run on. They mirror internal/agent/profiles'
 * AgentClaude and AgentGrok, and both are THIS runtime driving the Claude Agent SDK — what
 * differs is the endpoint and the credential, which arrive in `provider`.
 */
export const AgentKinds = ["claude", "grok"] as const;

/**
 * EffortLevels are the SDK's own vocabulary. The conductor has already refused a level the
 * chosen model does not accept, so anything that arrives here is passed straight through.
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

// TODO(step 17+): a skill may want its own MCP servers. That is a `mcp_servers` list on
// `skill`, mirrored here and merged into the runtime's own memory server in main.ts.
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
    // The backend this turn runs on. The conductor resolves it — skill, then profile, then
    // its own default — so it is always present and this runtime never has to.
    agent: z.enum(AgentKinds),
    // Absent means the model's own default effort, which is the provider's choice.
    effort: z.enum(EffortLevels).optional(),
  }),
  skill: z.strictObject({
    name: z.string().min(1),
    system_prompt: z.string(),
    allowed_tools: z.array(z.string()),
    max_turns: z.number().int().positive(),
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
  // Where to send the agent SDK's requests, and which environment variable holds the
  // credential for it. Absent means the SDK's own defaults, which is what a Claude turn
  // gets. It carries the NAME of a secret and never a value, exactly as memory does.
  provider: z
    .strictObject({
      base_url: z.string().min(1),
      api_key_env: z.string().min(1),
    })
    .optional(),
});

export type TurnBrief = z.infer<typeof briefSchema>;
export type TranscriptEntry = z.infer<typeof transcriptEntrySchema>;
export type RepoRef = z.infer<typeof repoSchema>;
export type SourceKind = TurnBrief["source"]["kind"];
export type AgentKind = TurnBrief["profile"]["agent"];

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
