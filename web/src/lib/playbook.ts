import type { PlaybookDefinition } from "../gen/podium/agent/v1/agent_pb";
import { parseGoDuration } from "./spec";
import { YamlReader, docToYaml, parseYaml, problemText, type YamlProblem } from "./yaml";

/** PlaybookDraft is what the editor hands back: the request message, in plain fields. */
export type PlaybookDraft = {
  name: string
  image: string
  systemPrompt: string
  allowedTools: string[]
  maxTurns: number
  timeout: string
  model: string
  agent: string
  effort: string
  labels: string[]
  priority: number
  resources: { cpu: number; memoryMb: number; pids: number }
  secrets: { name: string; target: string; key: string }[]
  repos: { name: string; url: string; defaultBranch: string }[]
  /** Undefined inherits the profile's persona, which is what a message field must be to mean that. */
  git?: { name: string; email: string }
  slackChannels: string[]
  linear: boolean
  interactive: boolean
  skills: string[]
  mcpServers: string[]
  env: Record<string, string>
  docker: boolean
  browser: boolean
}

/**
 * The browser's half of a `playbooks/<name>.yaml`.
 *
 * A playbook file is validated with KnownFields(true) on the conductor; this mirrors that so
 * a misspelled `sytem_prompt` is refused here rather than dropped. The name is the file name
 * and is not a field — it stays on the form even when the document is edited as YAML.
 */

export type PlaybookDoc = {
  image?: string
  system_prompt?: string
  allowed_tools?: string[]
  max_turns?: number
  timeout?: string
  model?: string
  agent?: string
  effort?: string
  labels?: string[]
  priority?: number
  resources?: { cpu?: number; memory_mb?: number; pids?: number }
  secrets?: { name?: string; target?: string; key?: string }[]
  repos?: { name?: string; url?: string; default_branch?: string }[]
  git?: { name?: string; email?: string }
  slack_channels?: string[]
  env?: Record<string, string>
  skills?: string[]
  mcp_servers?: string[]
  docker?: boolean
  browser?: boolean
  linear?: boolean
  interactive?: boolean
}

export type ParsedPlaybook = {
  doc?: PlaybookDoc
  problems: string[]
  issues: YamlProblem[]
}

const PLAYBOOK_KEYS = [
  "image",
  "system_prompt",
  "allowed_tools",
  "max_turns",
  "timeout",
  "model",
  "agent",
  "effort",
  "labels",
  "priority",
  "resources",
  "secrets",
  "repos",
  "git",
  "slack_channels",
  "env",
  "skills",
  "mcp_servers",
  "docker",
  "browser",
  "linear",
  "interactive",
]

const RESOURCE_KEYS = ["cpu", "memory_mb", "pids"]
const SECRET_KEYS = ["name", "target", "key"]
const REPO_KEYS = ["name", "url", "default_branch"]
const GIT_KEYS = ["name", "email"]

const TOOLS = [
  "bash",
  "edit",
  "glob",
  "grep",
  "list",
  "patch",
  "read",
  "task",
  "todoread",
  "todowrite",
  "webfetch",
  "write",
]

export function parsePlaybookYaml(text: string): ParsedPlaybook {
  const parsed = parseYaml(text, { emptyMessage: "the playbook is empty" })
  if (parsed.problems.length > 0) {
    return { problems: parsed.problems.map(problemText), issues: parsed.problems }
  }
  return parsePlaybookValue(parsed.value, parsed.lines)
}

export function parsePlaybookValue(
  doc: unknown,
  lines = new Map<string, { line: number; col: number }>(),
): ParsedPlaybook {
  if (doc === null || doc === undefined) {
    const issues: YamlProblem[] = [{ path: "", message: "the playbook is empty" }]
    return { problems: issues.map(problemText), issues }
  }

  const r = new YamlReader(lines)
  const root = r.object("", doc, PLAYBOOK_KEYS, "playbook field")
  if (!root) return done(undefined, r.problems)

  const out: PlaybookDoc = {}
  const image = r.string("image", root.image)
  if (image !== undefined) out.image = image
  const prompt = r.string("system_prompt", root.system_prompt)
  if (prompt !== undefined) out.system_prompt = prompt
  const tools = r.strings("allowed_tools", root.allowed_tools)
  if (tools !== undefined) out.allowed_tools = tools
  const maxTurns = r.int("max_turns", root.max_turns)
  if (maxTurns !== undefined) out.max_turns = maxTurns
  const timeout = r.string("timeout", root.timeout)
  if (timeout !== undefined) {
    out.timeout = timeout
    if (timeout.trim() !== "" && parseGoDuration(timeout) === undefined) {
      r.bad("timeout", `${JSON.stringify(timeout)} is not a duration like "30s" or "1h30m"`)
    }
  }
  const model = r.string("model", root.model)
  if (model !== undefined) out.model = model
  const agent = r.string("agent", root.agent)
  if (agent !== undefined) out.agent = agent
  const effort = r.string("effort", root.effort)
  if (effort !== undefined) out.effort = effort
  const labels = r.strings("labels", root.labels)
  if (labels !== undefined) out.labels = labels
  const priority = r.int("priority", root.priority)
  if (priority !== undefined) out.priority = priority

  const resources = r.object("resources", root.resources, RESOURCE_KEYS, "playbook field")
  if (resources) {
    out.resources = {
      cpu: r.number("resources.cpu", resources.cpu),
      memory_mb: r.int("resources.memory_mb", resources.memory_mb),
      pids: r.int("resources.pids", resources.pids),
    }
  }

  if (root.secrets !== undefined && root.secrets !== null) {
    if (!Array.isArray(root.secrets)) {
      r.bad("secrets", "must be a list")
    } else {
      out.secrets = root.secrets.map((item, i) => {
        const path = `secrets[${i}]`
        const o = r.object(path, item, SECRET_KEYS, "playbook field") ?? {}
        return {
          name: r.string(`${path}.name`, o.name) ?? "",
          target: r.string(`${path}.target`, o.target) ?? "",
          key: r.string(`${path}.key`, o.key) ?? "",
        }
      })
    }
  }

  if (root.repos !== undefined && root.repos !== null) {
    if (!Array.isArray(root.repos)) {
      r.bad("repos", "must be a list")
    } else {
      out.repos = root.repos.map((item, i) => {
        const path = `repos[${i}]`
        const o = r.object(path, item, REPO_KEYS, "playbook field") ?? {}
        return {
          name: r.string(`${path}.name`, o.name) ?? "",
          url: r.string(`${path}.url`, o.url) ?? "",
          default_branch: r.string(`${path}.default_branch`, o.default_branch) ?? "",
        }
      })
    }
  }

  const git = r.object("git", root.git, GIT_KEYS, "playbook field")
  if (git) {
    out.git = {
      name: r.string("git.name", git.name) ?? "",
      email: r.string("git.email", git.email) ?? "",
    }
  }

  const channels = r.strings("slack_channels", root.slack_channels)
  if (channels !== undefined) out.slack_channels = channels
  const env = r.stringMap("env", root.env)
  if (env !== undefined) out.env = env
  const skills = r.strings("skills", root.skills)
  if (skills !== undefined) out.skills = skills
  const mcp = r.strings("mcp_servers", root.mcp_servers)
  if (mcp !== undefined) out.mcp_servers = mcp
  const docker = r.bool("docker", root.docker)
  if (docker !== undefined) out.docker = docker
  const browser = r.bool("browser", root.browser)
  if (browser !== undefined) out.browser = browser
  const linear = r.bool("linear", root.linear)
  if (linear !== undefined) out.linear = linear
  const interactive = r.bool("interactive", root.interactive)
  if (interactive !== undefined) out.interactive = interactive

  if ((out.system_prompt ?? "").trim() === "") r.bad("system_prompt", "is required")
  if (!out.allowed_tools || out.allowed_tools.length === 0) {
    r.bad("allowed_tools", "is required and must name at least one tool")
  } else {
    for (const [i, tool] of out.allowed_tools.entries()) {
      if (tool.trim() === "") {
        r.bad(`allowed_tools[${i}]`, "holds an empty entry")
      } else if (!TOOLS.includes(tool)) {
        r.bad(
          `allowed_tools[${i}]`,
          `names ${JSON.stringify(tool)}, which is not a tool this harness has (have ${TOOLS.join(", ")})`,
        )
      }
    }
  }

  if (r.problems.length > 0) return done(undefined, r.problems)
  return done(out, [])
}

function done(doc: PlaybookDoc | undefined, issues: YamlProblem[]): ParsedPlaybook {
  return { doc, problems: issues.map(problemText), issues }
}

/** playbookDocToYaml is the text the YAML tab shows. */
export function playbookDocToYaml(doc: PlaybookDoc): string {
  return docToYaml(doc)
}

/** definitionToDoc renders a GetProfile playbook back into the file shape. */
export function definitionToDoc(playbook: PlaybookDefinition): PlaybookDoc {
  const doc: PlaybookDoc = {}
  if (playbook.image) doc.image = playbook.image
  if (playbook.systemPrompt) doc.system_prompt = playbook.systemPrompt
  const tools = playbook.allowedTools ?? []
  if (tools.length > 0) doc.allowed_tools = [...tools]
  if (playbook.maxTurns) doc.max_turns = playbook.maxTurns
  if (playbook.timeout) doc.timeout = playbook.timeout
  if (playbook.model) doc.model = playbook.model
  if (playbook.agent) doc.agent = playbook.agent
  if (playbook.effort) doc.effort = playbook.effort
  const labels = playbook.labels ?? []
  if (labels.length > 0) doc.labels = [...labels]
  if (playbook.priority) doc.priority = playbook.priority
  if (playbook.resources && (playbook.resources.cpu || playbook.resources.memoryMb || playbook.resources.pids)) {
    doc.resources = {}
    if (playbook.resources.cpu) doc.resources.cpu = playbook.resources.cpu
    if (playbook.resources.memoryMb) doc.resources.memory_mb = playbook.resources.memoryMb
    if (playbook.resources.pids) doc.resources.pids = playbook.resources.pids
  }
  const secrets = playbook.secrets ?? []
  if (secrets.length > 0) {
    doc.secrets = secrets.map((s) => ({ name: s.name, target: s.target, key: s.key }))
  }
  const repos = playbook.repos ?? []
  if (repos.length > 0) {
    doc.repos = repos.map((r) => ({
      name: r.name,
      url: r.url,
      default_branch: r.defaultBranch,
    }))
  }
  if (playbook.git && (playbook.git.name || playbook.git.email)) {
    doc.git = { name: playbook.git.name, email: playbook.git.email }
  }
  const channels = playbook.slackChannels ?? []
  if (channels.length > 0) doc.slack_channels = [...channels]
  const env = playbook.env ?? {}
  if (Object.keys(env).length > 0) doc.env = { ...env }
  const skills = playbook.skills ?? []
  if (skills.length > 0) doc.skills = [...skills]
  const mcp = playbook.mcpServers ?? []
  if (mcp.length > 0) doc.mcp_servers = [...mcp]
  if (playbook.linear) doc.linear = true
  if (playbook.interactive) doc.interactive = true
  if (playbook.docker) doc.docker = true
  if (playbook.browser) doc.browser = true
  return doc
}

export function definitionToYaml(playbook: PlaybookDefinition): string {
  return playbookDocToYaml(definitionToDoc(playbook))
}

/** draftToDoc turns the form into the same document a playbooks/<name>.yaml carries. */
export function draftToDoc(draft: PlaybookDraft): PlaybookDoc {
  const doc: PlaybookDoc = {
    image: draft.image,
    system_prompt: draft.systemPrompt,
    allowed_tools: draft.allowedTools,
  }
  if (draft.maxTurns) doc.max_turns = draft.maxTurns
  if (draft.timeout) doc.timeout = draft.timeout
  if (draft.model) doc.model = draft.model
  if (draft.agent) doc.agent = draft.agent
  if (draft.effort) doc.effort = draft.effort
  if (draft.labels.length > 0) doc.labels = [...draft.labels]
  if (draft.priority) doc.priority = draft.priority
  if (draft.resources.cpu || draft.resources.memoryMb || draft.resources.pids) {
    doc.resources = {}
    if (draft.resources.cpu) doc.resources.cpu = draft.resources.cpu
    if (draft.resources.memoryMb) doc.resources.memory_mb = draft.resources.memoryMb
    if (draft.resources.pids) doc.resources.pids = draft.resources.pids
  }
  if (draft.secrets.length > 0) doc.secrets = draft.secrets.map((s) => ({ ...s }))
  if (draft.repos.length > 0) {
    doc.repos = draft.repos.map((r) => ({
      name: r.name,
      url: r.url,
      default_branch: r.defaultBranch,
    }))
  }
  if (draft.git) doc.git = { ...draft.git }
  if (draft.slackChannels.length > 0) doc.slack_channels = [...draft.slackChannels]
  if (Object.keys(draft.env).length > 0) doc.env = { ...draft.env }
  if (draft.skills.length > 0) doc.skills = [...draft.skills]
  if (draft.mcpServers.length > 0) doc.mcp_servers = [...draft.mcpServers]
  if (draft.linear) doc.linear = true
  if (draft.interactive) doc.interactive = true
  if (draft.docker) doc.docker = true
  if (draft.browser) doc.browser = true
  return doc
}

export function draftToYaml(draft: PlaybookDraft): string {
  return playbookDocToYaml(draftToDoc(draft))
}

/** docToDraft is the inverse, for submitting a YAML-edited playbook through the same RPC. */
export function docToDraft(name: string, doc: PlaybookDoc): PlaybookDraft {
  return {
    name,
    image: (doc.image ?? "").trim(),
    systemPrompt: doc.system_prompt ?? "",
    allowedTools: doc.allowed_tools ?? [],
    maxTurns: doc.max_turns ?? 0,
    timeout: (doc.timeout ?? "").trim(),
    model: (doc.model ?? "").trim(),
    agent: (doc.agent ?? "").trim(),
    effort: (doc.effort ?? "").trim(),
    labels: doc.labels ?? [],
    priority: doc.priority ?? 0,
    resources: {
      cpu: doc.resources?.cpu ?? 0,
      memoryMb: doc.resources?.memory_mb ?? 0,
      pids: doc.resources?.pids ?? 0,
    },
    secrets: (doc.secrets ?? []).map((s) => ({
      name: s.name ?? "",
      target: s.target || "env",
      key: s.key ?? "",
    })),
    repos: (doc.repos ?? []).map((r) => ({
      name: r.name ?? "",
      url: r.url ?? "",
      defaultBranch: r.default_branch ?? "",
    })),
    git:
      doc.git && (doc.git.name || doc.git.email)
        ? { name: (doc.git.name ?? "").trim(), email: (doc.git.email ?? "").trim() }
        : undefined,
    slackChannels: doc.slack_channels ?? [],
    linear: doc.linear ?? false,
    interactive: doc.interactive ?? false,
    skills: doc.skills ?? [],
    mcpServers: doc.mcp_servers ?? [],
    env: doc.env ?? {},
    docker: doc.docker ?? false,
    browser: doc.browser ?? false,
  }
}
