/**
 * enrollCommand is the one line an operator pastes on a new host.
 *
 * Neither credential is echoed into it. `PODIUM_LOCAL_TOKEN` is the operator's own and is already
 * in their shell; `TS_AUTHKEY` is a Tailscale auth key the operator mints in the Tailscale admin
 * console, and Podium never sees it. Only the enrollment token — single-use, Podium's own, and
 * worthless once redeemed — is inlined.
 *
 * The transport is read off the server URL: an https:// control plane is on a tailnet, where the
 * node embeds its own Tailscale device and needs no dev token at all.
 */
export function enrollCommand(server: string, token: string): string {
  const tailnet = server.startsWith("https://");
  return [
    `PODIUM_NODE_SERVER=${server}`,
    `PODIUM_NODE_TRANSPORT=${tailnet ? "tailnet" : "local"}`,
    tailnet ? "PODIUM_NODE_TS_AUTHKEY=$TS_AUTHKEY" : "PODIUM_NODE_LOCAL_TOKEN=$PODIUM_LOCAL_TOKEN",
    `PODIUM_NODE_ENROLL_TOKEN=${token}`,
    "podium-node",
  ].join(" ");
}

/** isTailnetServer reports whether a control plane URL is a tailnet one. */
export function isTailnetServer(server: string): boolean {
  return server.startsWith("https://");
}
