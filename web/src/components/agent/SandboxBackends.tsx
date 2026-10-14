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
    <div role="radiogroup" aria-label="Backend" className="grid gap-2">
      {SANDBOX_BACKENDS.map((backend) => {
        const selected = backend.id === SELF_HOSTED;
        return (
          <label
            key={backend.id}
            data-testid={`sandbox-backend-${backend.id}`}
            className={cn(
              "grid grid-cols-[20px_minmax(0,1fr)] items-start gap-3 rounded-md border border-border bg-panel p-4",
              selected && "border-fg bg-raised",
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
              className="mt-0.5 size-[18px] accent-accent disabled:cursor-not-allowed"
            />
            <span className="grid min-w-0 gap-1">
              <strong className="flex flex-wrap items-center gap-2 text-sm">
                {backend.name}
                <Badge tone={selected ? "ok" : "idle"} dot={false}>
                  {selected ? "In use" : "Coming soon"}
                </Badge>
              </strong>
              <span className="text-sm text-soft">{backend.detail}</span>
            </span>
          </label>
        );
      })}
    </div>
  );
}
