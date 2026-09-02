# Step 14 — Packaging, installers, and documentation

**Milestone:** M6 ✔ · **Depends on:** 13 · **Design ref:** §3 topology, §11 stack (Release), §12 M6

## Goal
A stranger can turn a fresh box into a Podium host with `docker compose up`, and a fresh VM into a node with one
command, in under two minutes each, following only the docs. This step turns the code into a product.

## In scope

### Release pipeline
- `.goreleaser.yaml` completed: four binaries (`podium`, `podium-server`, `podium-node`, `podium-runner`) for `linux/amd64`, `linux/arm64`,
  `darwin/arm64` (runner linux-only; node embeds both runner arches — build order: runner → node), archives with `LICENSE` + `README`,
  `checksums.txt`, SBOM (syft), cosign keyless signing of checksums and images.
- Multi-arch container images to GHCR: `ghcr.io/<org>/podium-server` (distroless base + UI embedded), `ghcr.io/<org>/podium-node`
  (needs `ca-certificates`; runs as root by necessity of the Docker socket — document), `ghcr.io/<org>/podium` (CLI, for CI usage).
  Tags: `vX.Y.Z`, `vX.Y`, `latest`, `sha-<short>`.
- GitHub Actions `release.yml` on tag push `v*`; `ci.yml` also builds snapshot artifacts on `main` (no publish).
- Version scheme: semver, `v0.x` until playbooks/connectors exist. `podium version` shows client and server versions and warns on skew.

### Host install (`deploy/`)
- `deploy/docker-compose.yml` — server, postgres, minio, optional node (profile `node`), named volumes, healthchecks, restart policies,
  pinned image tags. `deploy/.env.example` with every `PODIUM_*` variable commented. `deploy/README.md` = the quickstart.
- `podium-server` gains `init` subcommand: generates master key, prints a filled `.env` skeleton, and checks Tailscale prerequisites
  (auth key present, HTTPS enabled hint).

### Node install
- `deploy/install-node.sh` (served via `https://podium.<tailnet>.ts.net/install.sh` **and** GitHub raw): detects OS/arch, verifies Docker
  ≥ 24 and cgroup v2, downloads the checksummed binary, writes `/etc/podium/node.yaml` from flags/env
  (`PODIUM_SERVER`, `PODIUM_ENROLL_TOKEN`, `TS_AUTHKEY`, `PODIUM_LABELS`), installs and starts `deploy/systemd/podium-node.service`
  (hardened unit: `ProtectSystem=strict`, `ReadWritePaths=/var/lib/podium-node`, `SupplementaryGroups=docker`), then tails until `online`.
- Container alternative documented: `docker run -d --restart unless-stopped -v /var/run/docker.sock:/var/run/docker.sock -v podium-node:/var/lib/podium-node -e … ghcr.io/<org>/podium-node`.
  **Decision to record here (Alvaro's, currently open):** which of the two is the documented default. Until decided, docs show systemd first.
- `podium-node upgrade`: download new version → verify checksum → `Drain` → wait → replace binary → restart via systemd. Opt-in flag `auto_upgrade` (off).

### Documentation (`docs/`, rendered by a static site later — Markdown is enough now)
- `docs/quickstart.md` — host in 10 commands, first node, first task, first look at the UI.
- `docs/concepts.md` — control plane / node / task / runner; what a task is and isn't in this version.
- `docs/networking.md` — from step 11, plus a **troubleshooting** section (WhoIs 403s, cert issuance failures, key expiry, ACL mistakes).
- `docs/storage.md` — the fast-disk requirement for `data_dir` and volumes; sizing guidance; MinIO backup.
- `docs/security.md` — trust model (node = root-equivalent; task = untrusted), secrets flow, what redaction does and doesn't do, ports.
- `docs/task-spec.md` — every `TaskSpec` field with examples (`examples/*.yaml`: hello, postgres-sidecar, secrets, limits, artifacts).
- `docs/cli.md`, `docs/protocol.md` (updated), `docs/runner-events.md` (updated), `docs/operations.md` (backup/restore Postgres + MinIO,
  upgrades, draining, log retention, metrics list with meaning).
- `CONTRIBUTING.md` (dev setup: Go, Node, Docker, `make e2e`), `CODE_OF_CONDUCT.md`, `SECURITY.md` (report channel), issue/PR templates.
- README rewritten: what/why, 60-second demo (asciinema or GIF placeholder), status table of milestones, link to plans for the roadmap.

### Observability polish
- Metrics documented and namespaced `podium_`: tasks by status, schedule latency, node count by status, assign→provisioning latency,
  log bytes ingested, prune actions, stream reconnects. Grafana dashboard JSON in `deploy/grafana/` (optional but cheap).

## Out of scope
- Homebrew tap, Nix package, Helm chart (issues to file). Docs website theme.

## Acceptance checklist
- [ ] Fresh Ubuntu 24.04 VM (no Docker preinstalled beyond the prerequisite): `install-node.sh` → node `online` in < 2 min, measured.
- [ ] Fresh host: copy `deploy/`, `podium-server init`, fill two values, `docker compose up -d` → UI reachable over the tailnet in < 2 min.
- [ ] Release dry-run (`goreleaser release --snapshot --clean`) produces all artifacts; images run on both amd64 and arm64.
- [ ] Checksums and signatures verify with `cosign verify-blob`.
- [ ] `podium-node upgrade` from the previous snapshot to the current: drains, swaps, comes back `online`, no task lost (test with one running task).
- [ ] Every `PODIUM_*` variable in code appears in `.env.example` (add a test that greps the source for `PODIUM_[A-Z_]+` and compares).
- [ ] Docs reviewed by running the quickstart literally on a machine that has never seen Podium, by someone (or an agent) who did not build it.

## Verification
```sh
goreleaser release --snapshot --clean
docker compose -f deploy/docker-compose.yml config   # validates
shellcheck deploy/install-node.sh
```

## Notes
- Pin every base image and action by digest.
- The LICENSE placeholder from step 01 **must** be resolved before this step's release job is enabled; the release workflow should fail
  if `LICENSE` still contains `TODO`.

## Hand-off notes
_(fill in when done)_
