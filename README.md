# Podium

Podium runs containerised tasks on a fleet of machines you own: a control plane
(`podium-server`) schedules work, a daemon (`podium-node`) on each worker executes it with the
local Docker engine and streams logs back live, and a CLI (`podium`) submits and watches tasks.
It is **pre-alpha and not usable yet** — this repository currently holds a scaffold with four
no-op binaries while the design in [`plans/`](plans/) is implemented step by step; start with
[`plans/steps/00-index.md`](plans/steps/00-index.md).
