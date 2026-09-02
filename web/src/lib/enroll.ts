/** enrollCommand is the one line an operator pastes on a new host. PODIUM_DEV_TOKEN stays a
 *  shell reference rather than being echoed: it is the operator's own credential and it is
 *  already in their environment. */
export function enrollCommand(server: string, token: string): string {
  return [
    `PODIUM_NODE_SERVER=${server}`,
    "PODIUM_NODE_TRANSPORT=dev",
    "PODIUM_NODE_DEV_TOKEN=$PODIUM_DEV_TOKEN",
    `PODIUM_NODE_ENROLL_TOKEN=${token}`,
    "podium-node",
  ].join(" ");
}
