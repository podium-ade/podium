/**
 * Where a session's workspace runs. Self-hosted is the only backend this build can use.
 * Modal and Daytona are shown so the choice is visible before either one is connected.
 *
 * The constant lives beside the components rather than in them, for the same reason the
 * provider list does: a constant and a component exported from one module break fast refresh.
 */

/** SandboxBackend is one choice on the Settings backend tab. */
export type SandboxBackend = {
  /** id is the stable value. "node" is a podium-node enrolled with this control plane. */
  id: "node" | "modal" | "daytona";
  name: string;
  /** detail is one sentence under the name. */
  detail: string;
  /** available is false for a backend this build cannot select. */
  available: boolean;
};

/** SELF_HOSTED is the backend every session uses until another one can be selected. */
export const SELF_HOSTED = "node";

/** The backends Settings shows, in order. */
export const SANDBOX_BACKENDS: SandboxBackend[] = [
  {
    id: "node",
    name: "Self-hosted",
    detail: "Sessions run on the podium-node workers enrolled with this control plane.",
    available: true,
  },
  {
    id: "modal",
    name: "Modal",
    detail: "Sessions run in Modal sandboxes, restored from a filesystem snapshot.",
    available: false,
  },
  {
    id: "daytona",
    name: "Daytona",
    detail: "Sessions run in Daytona sandboxes, restored from a snapshot.",
    available: false,
  },
];
