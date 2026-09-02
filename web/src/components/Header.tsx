import { NavLink } from "react-router";

const link = ({ isActive }: { isActive: boolean }) =>
  `rounded px-2 py-1 text-sm ${isActive ? "bg-raised text-fg" : "text-muted hover:text-fg"}`;

export function Header() {
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
      </nav>
      <div className="ml-auto flex items-center gap-3 text-xs text-muted">
        {/* Dev transport has no per-user identity: every caller is the shared token. The
            tailnet transport (step 11) replaces this with the visiting user's login. */}
        <span title="dev transport: the shared bearer token has no per-user identity">dev</span>
        <span className="font-mono">{__PODIUM_VERSION__}</span>
      </div>
    </header>
  );
}
