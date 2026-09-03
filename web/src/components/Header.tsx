import { NavLink } from "react-router";
import { useViewer, viewerLabel } from "../lib/identity";

const link = ({ isActive }: { isActive: boolean }) =>
  `rounded px-2 py-1 text-sm ${isActive ? "bg-raised text-fg" : "text-muted hover:text-fg"}`;

export function Header() {
  const viewer = viewerLabel(useViewer());
  return (
    <header className="flex items-center gap-4 border-b border-border bg-panel px-4 py-2">
      <span className="font-mono text-sm font-semibold tracking-tight">
        podium<span className="text-accent">.</span>
      </span>
      <nav className="flex gap-1">
        <NavLink to="/" end className={link}>
          Tasks
        </NavLink>
        <NavLink to="/nodes" className={link}>
          Nodes
        </NavLink>
        <NavLink to="/secrets" className={link}>
          Secrets
        </NavLink>
      </nav>
      <div className="ml-auto flex items-center gap-3 text-xs text-muted">
        {/* Whoever WhoAmI says is looking: a Tailscale login on a tailnet, "dev" on the dev
            transport, which has no per-user identity at all. */}
        <span title={viewer.title}>{viewer.text}</span>
        <span className="font-mono">{__PODIUM_VERSION__}</span>
      </div>
    </header>
  );
}
