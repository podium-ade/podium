import type { SandboxProvider } from "../gen/podium/v1/sandbox_pb";

/**
 * The cloud sandbox providers podium-server runs tasks on, and what each one is configured
 * with. The names are the ones a task's `sandbox.provider` and a playbook's `runs_on` use.
 * `secret` is the credential: written once, never read back. The fields are the provider's
 * settings, none of them secret.
 */
export type ProviderField = {
  key: string;
  label: string;
  help: string;
  section: SectionKey;
  /** An example, or what an empty field means; it may follow the other settings. */
  placeholder?: string | ((values: Record<string, string>) => string);
  /** A fixed set of values, shown as a choice. */
  options?: string[];
  /** A number the field must hold: any, or a whole one. Text when absent. */
  kind?: "number" | "integer";
  /** The bounds of a number, inclusive. */
  min?: number;
  max?: number;
  /** What the number counts, shown in the field and wherever its value is; unitOne is it for 1. */
  unit?: string;
  unitOne?: string;
  /** The key of the default request this field is the limit of. */
  limitOf?: string;
};

/**
 * The groups a provider's dialog lays its settings out in, in order. The credential heads
 * Access and the concurrency cap heads Capacity; the provider's own fields fill the rest.
 */
export type SectionKey = "access" | "sandboxes" | "resources" | "capacity";

export const SECTIONS: { key: SectionKey; title: string; description?: string }[] = [
  { key: "access", title: "Access" },
  { key: "sandboxes", title: "Sandboxes" },
  {
    key: "resources",
    title: "Resources",
    description:
      "The default is what a task that asks for no resources gets. A limit is the most any sandbox may use; a task that asks for more is refused. Empty is the provider's default, or no limit.",
  },
  { key: "capacity", title: "Capacity" },
];

export type ProviderSpec = {
  name: string;
  label: string;
  blurb: string;
  secretLabel: string;
  secretHelp: string;
  /** What the concurrency cap means for this provider. */
  maxConcurrentHelp: string;
  fields: ProviderField[];
};

/** The values a playbook may override a provider's machine type and runtime with. */
export const BOAT_TYPES = ["small", "default", "large", "xlarge"];
export const MODAL_RUNTIMES = ["gvisor", "vm"];
/** What a Modal sandbox runs in when nothing names an image: modal.DefaultImage and DefaultVMImage. */
export const MODAL_DEFAULT_IMAGE = "ubuntu:24.04";
export const MODAL_DEFAULT_VM_IMAGE = "docker:29-dind";

/** RUNS_ON_NODES is where a turn runs on this fleet's own machines. */
export const RUNS_ON_NODES = "nodes";

/**
 * POOL_FIELDS keep sandboxes ready before any task asks, on every provider. A task whose
 * sandbox would be exactly the pool's — its image, the default size, nothing exposed — takes one.
 */
export const MAX_POOL_SIZE = 20;

const POOL_FIELDS: ProviderField[] = [
  {
    key: "pool_size",
    label: "Ready sandboxes",
    help: "How many to keep made and waiting, so a task starts at once. Each costs what a running one does. Empty is none.",
    placeholder: "0",
    section: "capacity",
    kind: "integer",
    min: 0,
    max: MAX_POOL_SIZE,
  },
  {
    key: "pool_image",
    section: "capacity",
    label: "Ready sandbox images",
    help: "The images they are made from, comma-separated: the ones the tasks you want sped up run in, such as your playbooks' images. Each gets Ready sandboxes of its own. On Modal, modal:<name> is an image published with Modal's SDK.",
    placeholder: "ghcr.io/podium-ade/podium-agent-runtime",
  },
];

export const SANDBOX_PROVIDERS: ProviderSpec[] = [
  {
    name: "boat",
    label: "boat.dev",
    blurb: "Every task gets a VM of its own with Docker, from boat's base image or a named snapshot.",
    secretLabel: "API key",
    secretHelp:
      "A key with only what Podium uses: boat api-key create podium --actions sandbox.create,sandbox.read,sandbox.update,sandbox.delete-own,exec,file.read,file.write,host,account.read,snapshot.read",
    maxConcurrentHelp:
      "How many tasks run on boat at once. Empty holds them to your account's own limit.",
    fields: [
      {
        key: "template",
        label: "Template",
        help: "The named snapshot an image task starts from. Empty is boat's base image.",
        placeholder: "ci-base",
        section: "sandboxes",
      },
      {
        key: "default_type",
        label: "Default size",
        help: "The machine a task gets when it asks for no resources.",
        options: BOAT_TYPES,
        section: "resources",
      },
      {
        key: "org",
        label: "Organization",
        help: "Bill sandboxes to this boat organization instead of the key's owner.",
        section: "access",
      },
      ...POOL_FIELDS,
    ],
  },
  {
    name: "modal",
    label: "Modal",
    blurb: "Every task gets a sandbox booted from its own image; the vm runtime adds Docker.",
    secretLabel: "Token secret",
    secretHelp: "The secret half of a Modal token (modal token new). Its ID goes below.",
    maxConcurrentHelp:
      "How many tasks run on Modal at once. Modal reports no limit of its own, so this is what bounds the bill. Empty is no cap.",
    fields: [
      { key: "token_id", label: "Token ID", help: "The ID half of the token.", placeholder: "ak-…", section: "access" },
      {
        key: "environment",
        label: "Environment",
        help: "The Modal environment. Empty is the default one.",
        placeholder: "main",
        section: "access",
      },
      {
        key: "app",
        label: "App",
        help: "The Modal App sandboxes are made in. It is created if missing.",
        placeholder: "podium-sandbox",
        section: "access",
      },
      {
        key: "runtime",
        label: "Runtime",
        help: "gvisor is the default; vm adds Docker and tmpfs, for sidecars.",
        options: MODAL_RUNTIMES,
        section: "sandboxes",
      },
      {
        key: "privileged_sidecars",
        label: "Privileged sidecars",
        help: "Under the vm runtime, let a task have a privileged sidecar, such as a Docker daemon for `docker: true`. It is root on the sandbox, which is a VM of the task's own holding no Modal credential. Empty is off.",
        options: ["true"],
        section: "sandboxes",
      },
      {
        key: "image",
        label: "Base image",
        help: "What a task that names no image or snapshot runs in. It needs sh, date and sleep; on the vm runtime, one with dockerd also gets Docker. Empty is Modal's default for the runtime, shown in the field.",
        placeholder: (values) => (values.runtime === "vm" ? MODAL_DEFAULT_VM_IMAGE : MODAL_DEFAULT_IMAGE),
        section: "sandboxes",
      },
      {
        key: "cpu",
        label: "Default CPU",
        help: "For a task that asks for none.",
        placeholder: "1",
        section: "resources",
        kind: "number",
        min: 0,
        unit: "cores",
        unitOne: "core",
      },
      {
        key: "cpu_limit",
        label: "CPU limit",
        help: "Past it, a sandbox is throttled.",
        placeholder: "none",
        section: "resources",
        kind: "number",
        min: 0,
        unit: "cores",
        unitOne: "core",
        limitOf: "cpu",
      },
      {
        key: "memory_mb",
        label: "Default memory",
        help: "For a task that asks for none.",
        placeholder: "1024",
        section: "resources",
        kind: "integer",
        min: 0,
        unit: "MB",
      },
      {
        key: "memory_limit_mb",
        label: "Memory limit",
        help: "Past it, a sandbox is stopped.",
        placeholder: "none",
        section: "resources",
        kind: "integer",
        min: 0,
        unit: "MB",
        limitOf: "memory_mb",
      },
      ...POOL_FIELDS,
    ],
  },
];

/** placeholderOf is what an empty field shows, given what the others hold. */
export function placeholderOf(f: ProviderField, values: Record<string, string>): string | undefined {
  return typeof f.placeholder === "function" ? f.placeholder(values) : f.placeholder;
}

/** withUnit is a stored value as it reads: "2 cores", "4096 MB". */
export function withUnit(f: ProviderField, value: string): string {
  if (!f.unit) return value;
  return `${value} ${Number(value) === 1 && f.unitOne ? f.unitOne : f.unit}`;
}

/**
 * fieldErrors is what is wrong with the settings as typed, by key, in words for beside the
 * field. It holds the server's and the provider's own rules, so a provider is never saved with
 * settings it would refuse to start with.
 */
export function fieldErrors(spec: ProviderSpec, values: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {};
  const num = (key: string): number | undefined => {
    const t = (values[key] ?? "").trim();
    return t === "" ? undefined : Number(t);
  };
  const byKey = new Map(spec.fields.map((f) => [f.key, f]));
  for (const f of spec.fields) {
    if (!f.kind) continue;
    const t = (values[f.key] ?? "").trim();
    if (t === "") continue;
    const n = Number(t);
    const whole = f.kind === "integer";
    if (!/^\d*\.?\d+$|^\d+\.$/.test(t) || !Number.isFinite(n) || (whole && !Number.isInteger(n))) {
      out[f.key] = whole ? "A whole number." : "A number, such as 0.5 or 2.";
    } else if (f.min !== undefined && n < f.min) {
      out[f.key] = `At least ${f.min}.`;
    } else if (f.max !== undefined && n > f.max) {
      out[f.key] = `At most ${f.max}.`;
    }
  }
  for (const f of spec.fields) {
    if (!f.limitOf || out[f.key]) continue;
    const limit = num(f.key);
    if (limit === undefined || limit === 0) continue;
    const def = byKey.get(f.limitOf);
    const request = num(f.limitOf);
    if (!def || out[f.limitOf]) continue;
    if (request === undefined || request === 0) {
      out[f.limitOf] = `Set a default too: ${spec.label} takes no limit without one.`;
    } else if (request > limit) {
      out[f.limitOf] = `Above its limit of ${withUnit(f, String(limit))}.`;
    }
  }
  return out;
}

/** The label a task placed on this provider carries. */
export function providerLabel(name: string): string {
  return `sandbox.provider=${name}`;
}

/**
 * capacityOf is how many sandboxes are up against the lower of the operator's cap and the
 * account's own limit, and which of the two that is. A limit of 0 is no cap at all.
 */
export function capacityOf(p: SandboxProvider): { inUse: number; limit: number; byAccount: boolean } {
  const cap = p.maxConcurrent > 0 ? p.maxConcurrent : Infinity;
  const account = p.accountLimit > 0 ? p.accountLimit : Infinity;
  const limit = Math.min(cap, account);
  return { inUse: p.inUse, limit: Number.isFinite(limit) ? limit : 0, byAccount: account < cap };
}

/** capacityText is how many sandboxes are up against what may be. */
export function capacityText(p: SandboxProvider): string {
  const caps = [
    p.maxConcurrent > 0 ? `${p.maxConcurrent} allowed` : "",
    p.accountLimit > 0 ? `account limit ${p.accountLimit}` : "",
  ].filter(Boolean);
  return caps.length > 0 ? `${p.inUse} of ${caps.join(", ")}` : `${p.inUse}, no cap`;
}

/**
 * sidecarProblem is why a turn with these sidecars cannot run on provider, or "" when it can.
 * A Docker daemon is a privileged sidecar, which only Modal's vm runtime takes, and only when
 * the operator allowed it; a browser needs Docker in the sandbox, which boat has and Modal
 * has under vm. On Modal the turn's image must also carry dockerd, which no setting shows.
 */
export function sidecarProblem(
  provider: string,
  settings: Record<string, string> | undefined,
  modalRuntime: string,
  want: { docker: boolean; browser: boolean },
): string {
  if (provider === "" || provider === RUNS_ON_NODES || (!want.docker && !want.browser)) return "";
  if (provider === "modal") {
    const vm = (modalRuntime || settings?.runtime || "gvisor") === "vm";
    if (!vm) return "Modal runs sidecars only under the vm runtime. Set it on the provider or on the playbook.";
    if (want.docker && settings?.privileged_sidecars !== "true") {
      return "A Docker daemon is a privileged sidecar, which the Modal provider allows only with Privileged sidecars on.";
    }
    return "";
  }
  if (want.docker) return `A Docker daemon is a privileged sidecar, which ${provider} sandboxes do not run.`;
  return "";
}
