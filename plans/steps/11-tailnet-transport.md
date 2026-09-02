# Step 11 — Tailnet transport: tsnet, WhoIs identity, enrollment, HTTPS

**Milestone:** M3 ✔ · **Depends on:** 07 · **Design ref:** §4 Networking (4.1–4.4), §5 enrollment

## Goal
Server and nodes join the tailnet as their own devices, nodes are authenticated by device tag + node key,
humans are authenticated by Tailscale identity with no login page, and the server serves HTTPS on its
MagicDNS name. After this step a node on another machine, behind NAT, with no open ports, runs tasks.

## In scope

### `internal/transport/tailnet` — server side
- `tsnet.Server{Hostname: PODIUM_TS_HOSTNAME, Dir: PODIUM_TS_STATE_DIR, AuthKey: TS_AUTHKEY, Ephemeral: false}`.
  Start, wait `Up(ctx)`, log the MagicDNS FQDN and tailnet IPs.
- Listener: `ts.ListenTLS("tcp", ":443")` (certs via Tailscale's cert API — requires HTTPS enabled on the tailnet; fail with a
  message linking to docs if not). Also `ts.Listen("tcp", ":80")` → redirect to https.
- `Identify(r)`: `lc.WhoIs(ctx, r.RemoteAddr)`:
  - `Node.Tags` contains `tag:podium-node` → `Identity{Kind: Node, NodeTags}`; the stream handler then requires a valid node key.
  - `Node.Tags` contains `tag:podium-server` → reject (servers don't call servers).
  - Otherwise `UserProfile.LoginName` set → `Identity{Kind: User, Login, DisplayName}`; `store.UpsertUser` on first sight.
  - Tagged device with neither tag → 403.
- Config knobs: `PODIUM_TS_REQUIRED_NODE_TAG` (default `tag:podium-node`), `PODIUM_TS_ALLOW_UNTAGGED_NODES=false` (dev escape hatch, warns).

### `--tailscale=host` mode (both daemons)
- `PODIUM_TRANSPORT=host` (server) / `transport: host` (node): use the local `tailscaled` via `tailscale.com/client/tailscale.LocalClient`
  for `WhoIs`, and bind to the machine's tailnet IP (`Status().Self.TailscaleIPs`). Certs via `LocalClient.CertPair`.
  Nodes in host mode simply dial the server URL; identity comes from the host's device tag.

### `internal/transport/tailnet` — node side
- Node runs its own `tsnet.Server` (hostname `podium-node-<short host>`; state in `data_dir/ts`), dials
  `https://podium.<tailnet>.ts.net` using `ts.HTTPClient()`. No listener at all.
- Auth key for nodes: `PODIUM_NODE_TS_AUTHKEY` (or `TS_AUTHKEY`), expected to be a **pre-authorized, tagged, reusable** key for
  `tag:podium-node`. The enrollment token (Podium-level, single-use) is separate from the Tailscale auth key (network-level, reusable) —
  document the two-key model plainly.

### Enrollment over tailnet (`internal/server/nodes`)
- `Enroll` requires `Identity.Kind == Node` (tag present) **and** a valid enrollment token. Record the Tailscale node ID
  (`WhoIs().Node.StableID`) on the `nodes` row (migration `0004_nodes_ts_stable_id.sql`) and, on later `Hello`s, require the same
  StableID unless an admin runs `podium node rekey`. This binds a Podium node identity to one Tailscale device.
- `podium node enroll --server URL --token T [--labels …]` convenience: writes `node.yaml` and runs the first `Enroll`.

### HTTP hardening
- Security headers, `SameSite=Strict` not needed (no cookies — identity is per-connection), CORS disabled by default (UI is same-origin).
- Rate limit `Enroll` per remote IP (5/min).

### Docs & deploy
- `deploy/tailscale-acl.example.json` (from design §4.2) and `docs/networking.md`: two-key model, ACL, host vs tsnet mode, HTTPS
  prerequisite, how WhoIs login works, why no public ingress is needed, and the storage-placement warning.
- Compose: server `volumes: server-state:/var/lib/podium` for tsnet state; node example service with its auth key.

## Out of scope
- `mtls` transport (deferred, no step). Per-task tailnet attachment. Funnel for webhooks (connectors, later).

## Acceptance checklist
- [ ] Server starts with `PODIUM_TRANSPORT=tailnet`, appears in the Tailscale admin console as `podium` tagged `tag:podium-server`,
      serves `https://podium.<tailnet>.ts.net/healthz` with a valid cert (curl from another tailnet device, no `-k`).
- [ ] Browser hit on `/podium.v1.TaskService/ListTasks` from a user device is accepted and `requested_by` of a created task equals that
      user's login; a `users` row appears.
- [ ] Node on a **different machine behind NAT** (or a VM with no port forwards) enrolls with a single-use token, shows `online`,
      runs the M1 acceptance task; `ss -ltnp` on the node shows nothing listening except the local metrics port on loopback.
- [ ] Applying the example ACL (in a test tailnet) keeps everything working; adding a rule test that server→node is denied is documented
      as a manual check (`tailscale ping` from server device to node device fails).
- [ ] An untagged device presenting a valid enrollment token is rejected; a tagged device with a used token is rejected; a different
      Tailscale device presenting a stolen `identity.json` is rejected (StableID mismatch).
- [ ] `host` mode works on a machine that already runs `tailscaled` (manual test, documented).
- [ ] Dev transport still passes the e2e suite (no regression).

## Verification
```sh
PODIUM_TRANSPORT=tailnet TS_AUTHKEY=tskey-auth-… PODIUM_TS_HOSTNAME=podium ./bin/podium-server
curl -sf https://podium.<tailnet>.ts.net/readyz          # from another tailnet device
./bin/podium --server https://podium.<tailnet>.ts.net nodes
```

## Notes
- `tsnet` state dir must persist or the device re-registers with a new identity each restart (and the admin console fills with ghosts).
- Tagged devices don't key-expire by default; still surface `KeyExpiry` from `Status()` in `/readyz` details and warn < 30 days.
- The CLI over tailnet needs no token: WhoIs identifies the human. Keep `--token` for dev only.

## Hand-off notes
_(fill in when done)_
