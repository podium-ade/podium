import {
  LineCounter,
  isMap,
  isScalar,
  isSeq,
  parseDocument,
  stringify,
  type ParsedNode,
  type Pair,
  type YAMLError,
} from "yaml";

/**
 * One problem found while reading a YAML document, with a document path and — when the CST
 * still has a range — the line it was written on.
 *
 * The string form (`image: is required`) is what existing screens list. The line is what the
 * editor paints in the gutter, so an unknown field is not just named but pointed at.
 */
export type YamlProblem = {
  path: string
  message: string
  /** 1-based, when the source still has this node. */
  line?: number
  /** 1-based. */
  col?: number
}

export type LinePos = { line: number; col: number }

/** path → the line the key (or seq item) was written on. */
export type LineMap = Map<string, LinePos>

/**
 * The listed form every screen already speaks.
 *
 * A Reader problem is always `path: message`, even at the root (`: must be a mapping`) —
 * that is how Go's decoder writes it and how the spec tests pin it. A syntax error and the
 * empty-document sentence have no path and are the message itself.
 */
export function problemText(p: YamlProblem): string {
  if (p.path === "" && !p.message.startsWith("must ")) return p.message
  return `${p.path}: ${p.message}`
}

export function problemsText(problems: YamlProblem[]): string[] {
  return problems.map(problemText)
}

/**
 * parseYaml is the one decoder every editor goes through: syntax first, then a line map of
 * every key so a later KnownFields walk can point at the source.
 *
 * It does not interpret the document. Callers that know the schema (a task spec, a playbook)
 * walk the value and attach these lines to whatever they refuse.
 */
export function parseYaml(
  text: string,
  opts: { emptyMessage?: string } = {},
): { value?: unknown; problems: YamlProblem[]; lines: LineMap } {
  const emptyMessage = opts.emptyMessage ?? "the document is empty"
  const lines: LineMap = new Map()
  if (text.trim() === "") return { problems: [{ path: "", message: emptyMessage }], lines }

  const lineCounter = new LineCounter()
  const doc = parseDocument(text, { lineCounter, prettyErrors: true, uniqueKeys: true })
  if (doc.errors.length > 0) {
    return { problems: doc.errors.map(yamlError), lines }
  }

  const value = doc.toJS()
  if (value === null || value === undefined) {
    return { problems: [{ path: "", message: emptyMessage }], lines }
  }
  indexLines(doc.contents, lineCounter, "", lines)
  return { value, problems: [], lines }
}

/** docToYaml is the text the editor shows: block style, no wrap, so a diff stays readable. */
export function docToYaml(doc: unknown): string {
  if (doc === undefined) return ""
  return stringify(doc, { lineWidth: 0 })
}

function yamlError(err: YAMLError): YamlProblem {
  const pos = err.linePos?.[0]
  return {
    path: "",
    message: firstLine(err.message),
    line: pos?.line,
    col: pos?.col,
  }
}

function firstLine(message: string): string {
  const line = message.split("\n")[0]?.trim() ?? message
  return line.replace(/^YAML(?:Parse|Semantic)?Error:\s*/i, "")
}

function indexLines(
  node: ParsedNode | null | undefined,
  lineCounter: LineCounter,
  path: string,
  out: LineMap,
): void {
  if (!node) return
  mark(out, lineCounter, path, node.range)
  if (isMap(node)) {
    for (const item of node.items) {
      const key = pairKey(item)
      if (key === undefined) continue
      const child = path === "" ? key : `${path}.${key}`
      mark(out, lineCounter, child, rangeOf(item.key))
      indexLines(item.value as ParsedNode | null | undefined, lineCounter, child, out)
    }
    return
  }
  if (isSeq(node)) {
    node.items.forEach((item, i) => {
      const child = `${path}[${i}]`
      indexLines(item as ParsedNode | null | undefined, lineCounter, child, out)
    })
  }
}

function pairKey(item: Pair): string | undefined {
  if (isScalar(item.key)) return String(item.key.value)
  return undefined
}

function rangeOf(node: unknown): [number, number, number] | undefined {
  if (node && typeof node === "object" && "range" in node) {
    const range = (node as { range?: [number, number, number] | null }).range
    return range ?? undefined
  }
  return undefined
}

function mark(
  out: LineMap,
  lineCounter: LineCounter,
  path: string,
  range?: [number, number, number] | null,
): void {
  if (!range || path === "") return
  if (out.has(path)) return
  out.set(path, lineCounter.linePos(range[0]))
}

/**
 * YamlReader walks an already-decoded mapping the way Go's KnownFields decoder does: every
 * unknown key is a problem, types are checked, and nothing is dropped on the floor.
 *
 * It is the shared half of every YAML-backed editor. The spec decoder and the playbook
 * decoder supply the known keys and the messages; this supplies the walk.
 */
export class YamlReader {
  readonly problems: YamlProblem[] = []

  constructor(private readonly lines: LineMap = new Map()) {}

  bad(path: string, msg: string) {
    const loc = this.lines.get(path)
    this.problems.push({ path, message: msg, line: loc?.line, col: loc?.col })
  }

  /** openObject reads a mapping whose keys are names rather than field names. */
  openObject(path: string, v: unknown): Record<string, unknown> | undefined {
    if (v === undefined || v === null) return undefined
    if (typeof v !== "object" || Array.isArray(v)) {
      this.bad(path, "must be a mapping")
      return undefined
    }
    return v as Record<string, unknown>
  }

  object(
    path: string,
    v: unknown,
    known: string[],
    kind = "field",
  ): Record<string, unknown> | undefined {
    const obj = this.openObject(path, v)
    if (!obj) return undefined
    for (const k of Object.keys(obj)) {
      if (!known.includes(k)) {
        const at = path === "" ? k : `${path}.${k}`
        this.bad(at, `is not a ${kind} (known: ${known.join(", ")})`)
      }
    }
    return obj
  }

  string(path: string, v: unknown): string | undefined {
    if (v === undefined || v === null) return undefined
    if (typeof v === "string") return v
    if (typeof v === "number" || typeof v === "boolean") return String(v)
    this.bad(path, "must be a string")
    return undefined
  }

  number(path: string, v: unknown): number | undefined {
    if (v === undefined || v === null) return undefined
    if (typeof v === "number" && Number.isFinite(v)) return v
    this.bad(path, "must be a number")
    return undefined
  }

  int(path: string, v: unknown): number | undefined {
    const n = this.number(path, v)
    if (n === undefined) return undefined
    if (!Number.isInteger(n)) {
      this.bad(path, "must be a whole number")
      return undefined
    }
    return n
  }

  bool(path: string, v: unknown): boolean | undefined {
    if (v === undefined || v === null) return undefined
    if (typeof v === "boolean") return v
    this.bad(path, "must be true or false")
    return undefined
  }

  strings(path: string, v: unknown): string[] | undefined {
    if (v === undefined || v === null) return undefined
    if (!Array.isArray(v)) {
      this.bad(path, "must be a list")
      return undefined
    }
    const out: string[] = []
    v.forEach((item, i) => {
      const s = this.string(`${path}[${i}]`, item)
      if (s !== undefined) out.push(s)
    })
    return out
  }

  stringMap(path: string, v: unknown): Record<string, string> | undefined {
    if (v === undefined || v === null) return undefined
    if (typeof v !== "object" || Array.isArray(v)) {
      this.bad(path, "must be a mapping")
      return undefined
    }
    const out: Record<string, string> = {}
    for (const [k, raw] of Object.entries(v as Record<string, unknown>)) {
      const s = this.string(`${path}.${k}`, raw)
      if (s !== undefined) out[k] = s
    }
    return out
  }
}

/** Split a listed problem back into a YamlProblem when the decoder only kept the string. */
export function parseProblemText(text: string): YamlProblem {
  const idx = text.indexOf(": ")
  if (idx <= 0) return { path: "", message: text }
  return { path: text.slice(0, idx), message: text.slice(idx + 2) }
}

export function toIssues(problems: string[], lines: LineMap = new Map()): YamlProblem[] {
  return problems.map((p) => {
    const issue = parseProblemText(p)
    const loc = issue.path ? lines.get(issue.path) : undefined
    return { ...issue, line: loc?.line, col: loc?.col }
  })
}
