import type { SandboxProvider } from "../gen/podium/v1/sandbox_pb";

/**
 * The sandbox providers a sandbox node can run on, and what each one is configured with. The
 * names are the ones a node's `sandbox.provider` and the `sandbox.provider=` label use.
 * `secret` is the credential: written once, never read back. The fields are the provider's
 * settings, none of them secret.
 */
export type ProviderField = {
  key: string;
  label: string;
  help: string;
  placeholder?: string;
  /** A fixed set of values, shown as a choice. */
  options?: string[];
};

export type ProviderSpec = {
  name: string;
  label: string;
  blurb: string;
  secretLabel: string;
  secretHelp: string;
  /** What the concurrency cap means for this provider. */
  maxConcurrentHelp: string;
  /** A provider that reports no account limit has nothing else bounding it. */
  requireMaxConcurrent?: boolean;
  fields: ProviderField[];
};

/** The values a playbook may override a provider's machine type and runtime with. */
export const BOAT_TYPES = ["small", "default", "large", "xlarge"];
export const MODAL_RUNTIMES = ["gvisor", "vm"];

/** RUNS_ON_NODES is where a turn runs on this fleet's own machines. */
export const RUNS_ON_NODES = "nodes";

/** RUNS_ON_HOST is where the assistant runs in the conductor's own process. Never a playbook's. */
export const RUNS_ON_HOST = "host";

export const SANDBOX_PROVIDERS: ProviderSpec[] = [
  {
    name: "boat",
    label: "boat.dev",
    blurb: "Every task gets a VM of its own with Docker, from boat's base image or a named snapshot.",
    secretLabel: "API key",
    secretHelp:
      "A key with only what a node uses: boat api-key create podium-node --actions sandbox.create,sandbox.read,sandbox.update,sandbox.delete-own,exec,file.read,file.write,host,account.read,snapshot.read",
    maxConcurrentHelp:
      "How many tasks run on boat at once, across every boat node. Empty holds them to your account's own limit.",
    fields: [
      {
        key: "template",
        label: "Template",
        help: "The named snapshot an image task starts from. Empty is boat's base image.",
        placeholder: "ci-base",
      },
      {
        key: "default_type",
        label: "Default size",
        help: "The machine a task gets when it asks for no resources.",
        options: BOAT_TYPES,
      },
      {
        key: "org",
        label: "Organization",
        help: "Bill sandboxes to this boat organization instead of the key's owner.",
      },
    ],
  },
  {
    name: "modal",
    label: "Modal",
    blurb: "Every task gets a sandbox booted from its own image; the vm runtime adds Docker.",
    secretLabel: "Token secret",
    secretHelp: "The secret half of a Modal token (modal token new). Its ID goes below.",
    maxConcurrentHelp:
      "How many tasks run on Modal at once, across every Modal node. Modal reports no limit of its own, so this is what bounds the bill.",
    requireMaxConcurrent: true,
    fields: [
      { key: "token_id", label: "Token ID", help: "The ID half of the token.", placeholder: "ak-…" },
      { key: "app", label: "App", help: "The Modal App sandboxes are made in. It is created if missing.", placeholder: "podium-sandbox" },
      {
        key: "runtime",
        label: "Runtime",
        help: "gvisor is the default; vm adds Docker and tmpfs, for sidecars.",
        options: MODAL_RUNTIMES,
      },
      { key: "environment", label: "Environment", help: "The Modal environment. Empty is the default one." },
      { key: "cpu", label: "Default CPU", help: "Cores for a task that asks for none.", placeholder: "1" },
      { key: "memory_mb", label: "Default memory (MB)", help: "Memory for a task that asks for none.", placeholder: "1024" },
    ],
  },
];

/** The label a sandbox node of this provider carries. */
export function providerLabel(name: string): string {
  return `sandbox.provider=${name}`;
}

/** capacityText is how many sandboxes are up against what may be. */
export function capacityText(p: SandboxProvider): string {
  const caps = [
    p.maxConcurrent > 0 ? `${p.maxConcurrent} allowed` : "",
    p.accountLimit > 0 ? `account limit ${p.accountLimit}` : "",
  ].filter(Boolean);
  return caps.length > 0 ? `${p.inUse} of ${caps.join(", ")}` : `${p.inUse}, no cap`;
}
