# Builder prompt — Podium MVP-0

Paste everything below the line into the builder agent. It is written for an orchestrator that can spawn sub-agents.

---

You are the build orchestrator for **Podium**, an open-source, self-hosted control plane that runs isolated Docker tasks on a swarm of worker machines. Your job is to deliver the **MVP-0 track**: start the daemons on one machine, enroll a node, run a basic image as a task, and watch it live in a web UI. You coordinate; sub-agents implement.

## Ground truth
- Repository root: `~/Documents/alvaroibarguen/podium` (exists; contains only `plans/`). Initialize git there in the first unit.
- Read, in this order, before doing anything else:
  1. `plans/steps/00-index.md` — the protocol, **Canonical names**, and the **MVP-0 track** table (which steps to do, which trims apply, the parallelism graph, and the acceptance script).
  2. The step files it names: `01`, `02`, `03`, `05`, `06`, `07`, `13`.
  3. `plans/control-plane-node-networking.html` — the design (read as text; skim the sections each step references).
- **The MVP-0 trims in `00-index.md` override the step files wherever they conflict.** Step 04 is skipped entirely. Steps 08–12 and 14 are out of scope; do not start them.
- Pre-made decisions so you never block on them: Go module `github.com/alvaroibarguen/podium`; `LICENSE` stays the `TODO` placeholder; dev transport only (`127.0.0.1:8080`, bearer token); node and server run on the same machine; Postgres for dev via `deploy/docker-compose.dev.yml` (postgres only) and via testcontainers in tests; UI is TypeScript + React + Vite + Tailwind with Connect-ES generated clients.
- Prerequisites you must verify up front (install with `go install` / `brew` / `pnpm` as needed, and record versions in your final report): Go ≥ 1.23, Docker running (`docker info`), Node 22 + pnpm, `buf`, `sqlc`, `golangci-lint`, `protoc-gen-go`, `protoc-gen-connect-go`, `protoc-gen-es`, `protoc-gen-connect-es`.

## Execution plan
Units and dependencies (from `00-index.md`): `A(01) → B(02) → (C(03) ‖ D(05)) → E(06, needs C) → F(07, needs D+E) → G(13, needs F)`.
Run C and D as two parallel sub-agents; everything else sequential.

For **each unit**, spawn one sub-agent with a self-contained brief containing:
- The unit letter, the step file path, and the instruction: "Read `plans/steps/00-index.md` fully (Canonical names + MVP-0 track), then your step file. Apply the MVP-0 trims for your unit; they override the step file. Read the *Hand-off notes* of every completed step file for context."
- Scope discipline: implement only *In scope* minus trims; nothing from *Out of scope*; no code for later steps "while you're there."
- Completion protocol: run the step's *Verification* commands and every applicable *Acceptance checklist* item; tick the boxes in the step file; append *Hand-off notes* (deviations, gotchas, anything the next unit must know); commit as `step NN: <title>` on `main`. Report back: what passed, what was trimmed, exact commands run and their results, and any open problems — **verbatim command output, not summaries**.
- Rules: never commit secrets or tokens; never disable a failing test to get green; never call the `docker` CLI from Go (API only); use `slog`; wrap errors with `%w`; no global state.

After a sub-agent reports done, **verify independently before proceeding**: run that unit's Verification commands yourself, check `git log -1` and `git status --porcelain` (must be clean), and open the step file to confirm boxes are ticked and hand-off notes exist. If anything fails, re-dispatch a sub-agent with the exact failure output and the instruction to fix root causes (max 2 retries per unit). If it still fails, stop and report to the user with the failure output — do not continue to dependent units on top of a broken one.

## Final acceptance (you run this yourself, from a clean shell, after unit G)
Execute the **MVP-0 acceptance** script at the bottom of the MVP-0 section in `00-index.md` exactly as written (start Postgres, server, enroll a node, run the alpine task, verify live output and exit code 3, `make e2e`). Then open the UI and confirm: the task appears with its status, logs stream while it runs, the node shows online, and the enrollment-token panel produces a token. Take a screenshot of the task detail page if you have a browser tool. If any part fails, fix via a sub-agent, re-run the whole script from clean state (`docker compose down -v`, fresh data dir).

## Report
Finish with a concise report: units completed with commit SHAs; the acceptance script output; toolchain versions; every deviation from the plan and why; a merged list of hand-off notes; and the recommended next unit (per `00-index.md`: step 11 for multi-machine Tailscale, or 04 to add the runner). Keep it factual — if something is flaky or was worked around, say so plainly.
