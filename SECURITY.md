# Security policy

## Status

Podium has never had a security review and has no released version. This policy is here so
that the process exists before it is needed.

**There are no supported versions.** Nothing has been released, so nothing receives security
fixes. When the first `v0.x` is tagged, only the newest one will.

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting on this repository:
**Security → Report a vulnerability**. It creates a private advisory that only the maintainers
can see, and it is the right channel even for something you are not sure counts.

If private reporting is not available to you, contact the repository owner directly through
their GitHub profile and ask for a private channel before sending details.

Please include:

- what you did, and what happened;
- which component — `podium-server`, `podium-node`, `podium-runner`, the CLI, the web UI;
- the transport (`dev`, `tailnet`, `host`) and whether it matters;
- a proof of concept, if you have one;
- what you think the impact is.

There is no bug bounty and no SLA. This is one person's project.

## What is already known, and is therefore not a finding

The following are documented in [`docs/security.md`](docs/security.md) and are current, deliberate
limitations rather than vulnerabilities. Reporting them tells us nothing we have not written down:

- **There is no RBAC.** Anyone who can reach the API can do everything, including running code as
  root on every worker.
- **A `podium-node` is root-equivalent on its host** — it holds the Docker socket. Running it as a
  non-root user in the `docker` group would be the same power with a longer name.
- **The `local` transport is unencrypted**, and resolved secret values cross it. It is loopback-only
  and the server refuses to bind anywhere else.
- **There is no egress policy for task containers.** Assume a task can reach whatever its worker
  can reach.
- **Log redaction is best-effort string matching**, does not survive a node restart, and is not a
  control you should rely on.
- **The runner's event socket is world-writable inside the task container**, so a task can forge
  `step` and `artifact` events.
- **`/healthz`, `/readyz` and `/metrics` are unauthenticated** on both daemons.

What *is* worth reporting: anything that crosses one of the trust boundaries in
[`docs/security.md`](docs/security.md#trust-model) in a way that document does not already
describe. A task escaping its container into the node, a node reading another node's work, an
unauthenticated caller reaching an authenticated RPC, a secret reaching a place the secrets flow
says it never goes, or a way to make the control plane execute something.

## Supply chain

Releases are built by [`.github/workflows/release.yml`](.github/workflows/release.yml):

- every archive gets an SBOM from `syft`;
- `checksums.txt` is signed with **keyless cosign**, so the signature names the workflow identity
  that produced it rather than a key somebody holds;
- container images are multi-arch, based on `gcr.io/distroless/static-debian12` **pinned by
  digest**, and their manifests are signed the same way;
- every GitHub Action in the release workflow is pinned to a commit SHA.

Verifying a release:

```sh
cosign verify-blob \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/alvaroibarguen/podium/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --check --ignore-missing checksums.txt
```

**None of this has been exercised.** There has been no release, so no signature has ever been
produced or verified. Treat the above as the intended process, not as a description of something
that has happened.
