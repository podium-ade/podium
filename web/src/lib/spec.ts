import { parse, stringify } from "yaml";
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { TaskSpec, TaskSpecSchema } from "../gen/podium/v1/common_pb";

/**
 * The browser's half of `podium run --spec file.yaml`.
 *
 * `CreateTask` takes a protobuf `TaskSpec`, not YAML, so the browser has to do what
 * `pkg/spec.ParseTaskSpec` does on the CLI: decode the document, reject fields the schema does
 * not have, and hand over a typed message. Rejecting unknown fields is the part that matters —
 * `yaml.parse` would happily drop a misspelled `privilged: true` on the floor and submit a task
 * that silently does not do what was asked. The CLI's decoder is `KnownFields(true)` for exactly
 * that reason, and this mirrors it.
 *
 * Everything else is left to the server: `pkg/spec.Validate` reports every problem at once and
 * its messages are better than anything guessed at here.
 */

export type SpecInit = MessageInitShape<typeof TaskSpecSchema>;

export interface ParsedSpec {
  spec?: SpecInit;
  /** Every problem found, in document order. Empty means `spec` is set. */
  problems: string[];
}

/** The YAML shape, for the editor and for round-tripping a task back into the form. */
export interface SpecDoc {
  image?: string;
  command?: string[];
  working_dir?: string;
  env?: Record<string, string>;
  secrets?: SecretRefDoc[];
  sidecars?: Record<string, SidecarDoc>;
  resources?: ResourcesDoc;
  hardening?: HardeningDoc;
  labels?: string[];
  timeout?: string;
  max_attempts?: number;
  retry_on_node_loss?: boolean;
}

export interface SecretRefDoc {
  name?: string;
  target?: string;
  key?: string;
}
export interface SidecarDoc {
  image?: string;
  command?: string[];
  env?: Record<string, string>;
  readiness?: ReadinessDoc;
  resources?: ResourcesDoc;
}
export interface ReadinessDoc {
  tcp_port?: number;
  http_path?: string;
  http_port?: number;
  command?: string[];
  timeout?: string;
}
export interface ResourcesDoc {
  cpu?: number;
  memory_mb?: number;
  pids?: number;
}
export interface HardeningDoc {
  read_only_rootfs?: boolean;
  capabilities?: string[];
}

const TASK_KEYS = [
  "image",
  "command",
  "working_dir",
  "env",
  "secrets",
  "sidecars",
  "resources",
  "hardening",
  "labels",
  "timeout",
  "max_attempts",
  "retry_on_node_loss",
];
const SIDECAR_KEYS = ["image", "command", "env", "readiness", "resources"];
const READINESS_KEYS = ["tcp_port", "http_path", "http_port", "command", "timeout"];
const RESOURCE_KEYS = ["cpu", "memory_mb", "pids"];
const HARDENING_KEYS = ["read_only_rootfs", "capabilities"];
const SECRET_KEYS = ["name", "target", "key"];

const NS_PER = new Map<string, number>([
  ["ns", 1],
  ["us", 1e3],
  ["µs", 1e3],
  ["μs", 1e3],
  ["ms", 1e6],
  ["s", 1e9],
  ["m", 60e9],
  ["h", 3600e9],
]);

/** parseGoDuration reads the "1h30m", "500ms" spelling Go and the task spec both use. */
export function parseGoDuration(text: string): number | undefined {
  const s = text.trim();
  if (s === "" || s === "0") return s === "0" ? 0 : undefined;
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)/gy;
  let total = 0;
  let at = 0;
  for (;;) {
    re.lastIndex = at;
    const m = re.exec(s);
    if (!m) return undefined;
    total += Number(m[1]) * NS_PER.get(m[2])!;
    at = re.lastIndex;
    if (at >= s.length) return total;
  }
}

/** formatGoDuration is the inverse, in the spelling Go's Duration.String would produce. */
export function formatGoDuration(seconds: number, nanos = 0): string {
  let ns = Math.round(seconds * 1e9 + nanos);
  if (ns === 0) return "0s";
  const sign = ns < 0 ? "-" : "";
  ns = Math.abs(ns);
  if (ns < 1e9) {
    if (ns % 1e6 === 0) return `${sign}${ns / 1e6}ms`;
    if (ns % 1e3 === 0) return `${sign}${ns / 1e3}us`;
    return `${sign}${ns}ns`;
  }
  const secs = ns / 1e9;
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = Number((secs % 60).toFixed(9));
  if (h > 0) return `${sign}${h}h${m}m${s}s`;
  if (m > 0) return `${sign}${m}m${s}s`;
  return `${sign}${s}s`;
}

class Reader {
  readonly problems: string[] = [];

  bad(path: string, msg: string) {
    this.problems.push(`${path}: ${msg}`);
  }

  /** openObject reads a mapping whose keys are names rather than field names. */
  openObject(path: string, v: unknown): Record<string, unknown> | undefined {
    if (v === undefined || v === null) return undefined;
    if (typeof v !== "object" || Array.isArray(v)) {
      this.bad(path, "must be a mapping");
      return undefined;
    }
    return v as Record<string, unknown>;
  }

  object(path: string, v: unknown, known: string[]): Record<string, unknown> | undefined {
    const obj = this.openObject(path, v);
    if (!obj) return undefined;
    for (const k of Object.keys(obj)) {
      if (!known.includes(k)) {
        this.bad(path === "" ? k : `${path}.${k}`, `is not a task spec field (known: ${known.join(", ")})`);
      }
    }
    return obj;
  }

  string(path: string, v: unknown): string | undefined {
    if (v === undefined || v === null) return undefined;
    if (typeof v === "string") return v;
    if (typeof v === "number" || typeof v === "boolean") return String(v);
    this.bad(path, "must be a string");
    return undefined;
  }

  number(path: string, v: unknown): number | undefined {
    if (v === undefined || v === null) return undefined;
    if (typeof v === "number" && Number.isFinite(v)) return v;
    this.bad(path, "must be a number");
    return undefined;
  }

  int(path: string, v: unknown): number | undefined {
    const n = this.number(path, v);
    if (n === undefined) return undefined;
    if (!Number.isInteger(n)) {
      this.bad(path, "must be a whole number");
      return undefined;
    }
    return n;
  }

  bool(path: string, v: unknown): boolean | undefined {
    if (v === undefined || v === null) return undefined;
    if (typeof v === "boolean") return v;
    this.bad(path, "must be true or false");
    return undefined;
  }

  strings(path: string, v: unknown): string[] | undefined {
    if (v === undefined || v === null) return undefined;
    if (!Array.isArray(v)) {
      this.bad(path, "must be a list");
      return undefined;
    }
    const out: string[] = [];
    v.forEach((item, i) => {
      const s = this.string(`${path}[${i}]`, item);
      if (s !== undefined) out.push(s);
    });
    return out;
  }

  stringMap(path: string, v: unknown): Record<string, string> | undefined {
    if (v === undefined || v === null) return undefined;
    if (typeof v !== "object" || Array.isArray(v)) {
      this.bad(path, "must be a mapping");
      return undefined;
    }
    const out: Record<string, string> = {};
    for (const [k, raw] of Object.entries(v as Record<string, unknown>)) {
      const s = this.string(`${path}.${k}`, raw);
      if (s !== undefined) out[k] = s;
    }
    return out;
  }

  duration(path: string, v: unknown): { seconds: bigint; nanos: number } | undefined {
    const s = this.string(path, v);
    if (s === undefined) return undefined;
    const ns = parseGoDuration(s);
    if (ns === undefined) {
      this.bad(path, `${JSON.stringify(s)} is not a duration like "30s" or "1h30m"`);
      return undefined;
    }
    return { seconds: BigInt(Math.trunc(ns / 1e9)), nanos: Math.round(ns % 1e9) };
  }

  resources(path: string, v: unknown) {
    const o = this.object(path, v, RESOURCE_KEYS);
    if (!o) return undefined;
    return {
      cpu: this.number(`${path}.cpu`, o.cpu) ?? 0,
      memoryMb: BigInt(this.int(`${path}.memory_mb`, o.memory_mb) ?? 0),
      pids: this.int(`${path}.pids`, o.pids) ?? 0,
    };
  }
}

/**
 * parseSpecYaml decodes the editor's text into the message CreateTask wants.
 *
 * It returns problems rather than throwing so the form can list all of them the way the
 * server's own validator does, instead of stopping at the first.
 */
export function parseSpecYaml(text: string): ParsedSpec {
  if (text.trim() === "") return { problems: ["the spec is empty"] };

  let doc: unknown;
  try {
    doc = parse(text);
  } catch (err) {
    return { problems: [err instanceof Error ? err.message : String(err)] };
  }
  return parseSpecValue(doc);
}

/** parseSpecValue is the same check over an already-decoded document, for the built form. */
export function parseSpecValue(doc: unknown): ParsedSpec {
  if (doc === null || doc === undefined) return { problems: ["the spec is empty"] };

  const r = new Reader();
  const root = r.object("", doc, TASK_KEYS);
  if (!root) return { problems: r.problems };

  const spec: SpecInit = {
    image: r.string("image", root.image) ?? "",
    command: r.strings("command", root.command) ?? [],
    workingDir: r.string("working_dir", root.working_dir) ?? "",
    env: r.stringMap("env", root.env) ?? {},
    labels: r.strings("labels", root.labels) ?? [],
    maxAttempts: r.int("max_attempts", root.max_attempts) ?? 0,
    retryOnNodeLoss: r.bool("retry_on_node_loss", root.retry_on_node_loss) ?? false,
  };

  const timeout = r.duration("timeout", root.timeout);
  if (timeout) spec.timeout = timeout;

  const resources = r.resources("resources", root.resources);
  if (resources) spec.resources = resources;

  const hardening = r.object("hardening", root.hardening, HARDENING_KEYS);
  if (hardening) {
    spec.hardening = {
      readOnlyRootfs: r.bool("hardening.read_only_rootfs", hardening.read_only_rootfs) ?? false,
      capabilities: r.strings("hardening.capabilities", hardening.capabilities) ?? [],
    };
  }

  if (root.secrets !== undefined && root.secrets !== null) {
    if (!Array.isArray(root.secrets)) {
      r.bad("secrets", "must be a list");
    } else {
      spec.secrets = root.secrets.map((item, i) => {
        const path = `secrets[${i}]`;
        const o = r.object(path, item, SECRET_KEYS) ?? {};
        return {
          name: r.string(`${path}.name`, o.name) ?? "",
          target: r.string(`${path}.target`, o.target) ?? "",
          key: r.string(`${path}.key`, o.key) ?? "",
        };
      });
    }
  }

  // A sidecar's key is its DNS name, so the map itself is open; only its contents are checked.
  const sidecars = r.openObject("sidecars", root.sidecars);
  if (sidecars) {
    const out: NonNullable<SpecInit["sidecars"]> = {};
    for (const [name, raw] of Object.entries(sidecars)) {
      const path = `sidecars.${name}`;
      const o = r.object(path, raw, SIDECAR_KEYS) ?? {};
      const sc: NonNullable<SpecInit["sidecars"]>[string] = {
        image: r.string(`${path}.image`, o.image) ?? "",
        command: r.strings(`${path}.command`, o.command) ?? [],
        env: r.stringMap(`${path}.env`, o.env) ?? {},
      };
      const scResources = r.resources(`${path}.resources`, o.resources);
      if (scResources) sc.resources = scResources;
      const readiness = r.object(`${path}.readiness`, o.readiness, READINESS_KEYS);
      if (readiness) {
        sc.readiness = {
          tcpPort: r.int(`${path}.readiness.tcp_port`, readiness.tcp_port) ?? 0,
          httpPath: r.string(`${path}.readiness.http_path`, readiness.http_path) ?? "",
          httpPort: r.int(`${path}.readiness.http_port`, readiness.http_port) ?? 0,
          command: r.strings(`${path}.readiness.command`, readiness.command) ?? [],
        };
        const rt = r.duration(`${path}.readiness.timeout`, readiness.timeout);
        if (rt) sc.readiness.timeout = rt;
      }
      out[name] = sc;
    }
    spec.sidecars = out;
  }

  // The one check worth doing here rather than at the server: an empty image is the mistake
  // people actually make, and saying so before a round trip is kinder.
  if ((spec.image ?? "").trim() === "") r.bad("image", "is required");

  if (r.problems.length > 0) return { problems: r.problems };
  return { spec, problems: [] };
}

/** specToDoc renders a server-echoed TaskSpec back into the YAML shape, defaults and all. */
export function specToDoc(spec?: TaskSpec): SpecDoc {
  if (!spec) return {};
  const doc: SpecDoc = { image: spec.image };
  if (spec.command.length > 0) doc.command = [...spec.command];
  if (spec.workingDir) doc.working_dir = spec.workingDir;
  if (Object.keys(spec.env).length > 0) {
    doc.env = Object.fromEntries(Object.keys(spec.env).sort().map((k) => [k, spec.env[k]]));
  }
  if (spec.secrets.length > 0) {
    doc.secrets = spec.secrets.map((s) => ({ name: s.name, target: s.target, key: s.key }));
  }
  const sidecarNames = Object.keys(spec.sidecars).sort();
  if (sidecarNames.length > 0) {
    doc.sidecars = {};
    for (const name of sidecarNames) {
      const sc = spec.sidecars[name];
      const out: SidecarDoc = { image: sc.image };
      if (sc.command.length > 0) out.command = [...sc.command];
      if (Object.keys(sc.env).length > 0) {
        out.env = Object.fromEntries(Object.keys(sc.env).sort().map((k) => [k, sc.env[k]]));
      }
      if (sc.readiness) {
        const rd: ReadinessDoc = {};
        if (sc.readiness.tcpPort) rd.tcp_port = sc.readiness.tcpPort;
        if (sc.readiness.httpPath) rd.http_path = sc.readiness.httpPath;
        if (sc.readiness.httpPort) rd.http_port = sc.readiness.httpPort;
        if (sc.readiness.command.length > 0) rd.command = [...sc.readiness.command];
        if (sc.readiness.timeout) {
          rd.timeout = formatGoDuration(Number(sc.readiness.timeout.seconds), sc.readiness.timeout.nanos);
        }
        if (Object.keys(rd).length > 0) out.readiness = rd;
      }
      const res = resourcesDoc(sc.resources);
      if (res) out.resources = res;
      doc.sidecars[name] = out;
    }
  }
  const res = resourcesDoc(spec.resources);
  if (res) doc.resources = res;
  if (spec.hardening && (spec.hardening.readOnlyRootfs || spec.hardening.capabilities.length > 0)) {
    doc.hardening = {};
    if (spec.hardening.readOnlyRootfs) doc.hardening.read_only_rootfs = true;
    if (spec.hardening.capabilities.length > 0) {
      doc.hardening.capabilities = [...spec.hardening.capabilities];
    }
  }
  if (spec.labels.length > 0) doc.labels = [...spec.labels];
  if (spec.timeout) doc.timeout = formatGoDuration(Number(spec.timeout.seconds), spec.timeout.nanos);
  if (spec.maxAttempts) doc.max_attempts = spec.maxAttempts;
  if (spec.retryOnNodeLoss) doc.retry_on_node_loss = true;
  return doc;
}

function resourcesDoc(r?: { cpu: number; memoryMb: bigint; pids: number }): ResourcesDoc | undefined {
  if (!r) return undefined;
  const out: ResourcesDoc = {};
  if (r.cpu) out.cpu = r.cpu;
  if (r.memoryMb) out.memory_mb = Number(r.memoryMb);
  if (r.pids) out.pids = r.pids;
  return Object.keys(out).length > 0 ? out : undefined;
}

/** docToYaml is the text the editor shows. */
export function docToYaml(doc: SpecDoc): string {
  return stringify(doc, { lineWidth: 0 });
}

/** specToYaml renders a task's spec for display and for the re-run editor. */
export function specToYaml(spec?: TaskSpec): string {
  if (!spec) return "# no spec";
  return docToYaml(specToDoc(spec));
}

/** hasAdvancedFields says whether a spec carries anything the simple form cannot express. */
export function hasAdvancedFields(spec?: TaskSpec): boolean {
  if (!spec) return false;
  return (
    spec.secrets.length > 0 ||
    Object.keys(spec.sidecars).length > 0 ||
    (spec.hardening?.readOnlyRootfs ?? false) ||
    (spec.hardening?.capabilities.length ?? 0) > 0
  );
}

/**
 * splitServerProblems unpacks the server's InvalidArgument message.
 *
 * pkg/spec.Validate joins every problem with errors.Join, which renders one per line, so a
 * multi-problem spec comes back as one string with newlines in it. Showing that as a single
 * blob buries all but the first.
 */
export function splitServerProblems(message: string): string[] {
  return message
    .replace(/^invalid task spec:\s*/, "")
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

/** Fields is the simple form's state. Anything not here is edited as YAML. */
export interface Fields {
  image: string;
  command: string;
  workingDir: string;
  env: string;
  labels: string;
  timeout: string;
  maxAttempts: string;
  cpu: string;
  memoryMb: string;
  retryOnNodeLoss: boolean;
}

export const EMPTY_FIELDS: Fields = {
  image: "",
  command: "",
  workingDir: "",
  env: "",
  labels: "",
  timeout: "",
  maxAttempts: "",
  cpu: "",
  memoryMb: "",
  retryOnNodeLoss: false,
};

/**
 * fieldsToDoc turns the form into the same document `--spec file.yaml` carries, so both routes
 * end up in one decoder and one set of error messages.
 *
 * Problems it can find itself — a command line that is not KEY=VALUE, a number that is not a
 * number — are reported alongside the decoder's, because the point of listing problems at all
 * is to list all of them.
 */
export function fieldsToDoc(f: Fields): { doc: SpecDoc; problems: string[] } {
  const problems: string[] = [];
  const doc: SpecDoc = { image: f.image.trim() };

  const command = f.command
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
  if (command.length > 0) doc.command = command;

  if (f.workingDir.trim() !== "") doc.working_dir = f.workingDir.trim();

  const env: Record<string, string> = {};
  for (const raw of f.env.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    const eq = line.indexOf("=");
    if (eq <= 0) {
      problems.push(`env: ${JSON.stringify(line)} is not KEY=VALUE`);
      continue;
    }
    env[line.slice(0, eq).trim()] = line.slice(eq + 1);
  }
  if (Object.keys(env).length > 0) doc.env = env;

  const labels = f.labels
    .split(",")
    .map((l) => l.trim())
    .filter((l) => l !== "");
  if (labels.length > 0) doc.labels = labels;

  if (f.timeout.trim() !== "") doc.timeout = f.timeout.trim();

  const num = (name: string, text: string): number | undefined => {
    if (text.trim() === "") return undefined;
    const n = Number(text);
    if (!Number.isFinite(n)) {
      problems.push(`${name}: ${JSON.stringify(text)} is not a number`);
      return undefined;
    }
    return n;
  };

  const maxAttempts = num("max_attempts", f.maxAttempts);
  if (maxAttempts !== undefined) doc.max_attempts = maxAttempts;

  const cpu = num("resources.cpu", f.cpu);
  const memoryMb = num("resources.memory_mb", f.memoryMb);
  if (cpu !== undefined || memoryMb !== undefined) {
    doc.resources = {};
    if (cpu !== undefined) doc.resources.cpu = cpu;
    if (memoryMb !== undefined) doc.resources.memory_mb = memoryMb;
  }

  if (f.retryOnNodeLoss) doc.retry_on_node_loss = true;
  return { doc, problems };
}
