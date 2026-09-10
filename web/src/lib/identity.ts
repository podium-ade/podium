import { createContext, useContext } from "react";
import { IdentityKind, type WhoAmIResponse } from "../gen/podium/v1/identity_pb";

/** Viewer is the answer to WhoAmI, in the shape the shell actually renders. */
export type Viewer = {
  login: string;
  displayName: string;
  kind: IdentityKind;
  /** True when this control plane has a conductor and proxies its API. */
  agentEnabled: boolean;
  serverVersion: string;
};

export function viewerFrom(res: WhoAmIResponse): Viewer {
  return {
    login: res.login,
    displayName: res.displayName,
    kind: res.kind,
    // A server built before this field existed sends nothing, and proto3 reads that as
    // false — which is the right answer: it has no conductor to proxy.
    agentEnabled: res.agentEnabled,
    serverVersion: res.serverVersion,
  };
}

/**
 * How the viewer should be labelled in the header, and what to say about it on hover.
 *
 * Under the tailnet transport this is a real person: Tailscale named them at the transport
 * layer, no login step involved. Under the local transport there is no per-user identity at all —
 * every caller is the shared bearer token — and saying "dev" is the honest rendering of that.
 */
export function viewerLabel(v: Viewer | undefined): { text: string; title: string } {
  if (!v) return { text: "—", title: "identity unknown" };
  switch (v.kind) {
    case IdentityKind.USER:
      return {
        text: v.displayName || v.login,
        title: `signed in as ${v.login} — identified by Tailscale, no login step`,
      };
    case IdentityKind.NODE:
      return { text: v.login, title: "a Podium node, identified by its Tailscale ACL tag" };
    case IdentityKind.LOCAL_TOKEN:
      return {
        text: "local",
        title: "local transport: the shared bearer token has no per-user identity",
      };
    default:
      return { text: v.login || "—", title: "identity unknown" };
  }
}

export const ViewerContext = createContext<Viewer | undefined>(undefined);

export function useViewer(): Viewer | undefined {
  return useContext(ViewerContext);
}
