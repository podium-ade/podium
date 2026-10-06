import { Badge } from "../Badge";
import { SANDBOX_BACKENDS, SELF_HOSTED } from "../../lib/sandboxBackends";
import { cn } from "../../lib/utils";

/**
 * SandboxBackends is the Settings choice for where a session runs.
 *
 * Self-hosted is selected and is the only option that can be chosen. Modal and Daytona
 * stay in the group so an operator can see them, and they are disabled until a later
 * build can actually start a workspace there. Nothing is stored: the control plane has
 * one backend today, and the selected radio is that fact, not a preference.
 */
export function SandboxBackends() {
  return (
    <div role="radiogroup" aria-label="Backend" className="grid gap-3 md:grid-cols-3">
      {SANDBOX_BACKENDS.map((backend) => {
        const selected = backend.id === SELF_HOSTED;
        return (
          <label
            key={backend.id}
            data-testid={`sandbox-backend-${backend.id}`}
            className={cn(
              "flex items-start gap-3 rounded-xl border p-4 shadow-xs",
              selected ? "border-accent/50 bg-card" : "border-dashed border-border bg-card/40",
              backend.available ? "cursor-pointer" : "cursor-not-allowed",
            )}
          >
            <input
              type="radio"
              name="sandbox-backend"
              value={backend.id}
              checked={selected}
              disabled={!backend.available}
              onChange={() => {
                // Modal and Daytona are disabled. Self-hosted is already selected.
              }}
              className="mt-1 accent-accent disabled:cursor-not-allowed"
            />
            <span className="min-w-0 space-y-1">
              <span className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-semibold tracking-tight text-fg">{backend.name}</span>
                <Badge tone={selected ? "ok" : "idle"}>{selected ? "In use" : "Coming soon"}</Badge>
              </span>
              <span className="block text-xs leading-relaxed text-muted">{backend.detail}</span>
            </span>
          </label>
        );
      })}
    </div>
  );
}
