import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { useViewer, viewerLabel } from "../lib/identity";
import { canonicalRole, rbacEnforced, roleLabel } from "../lib/rbac";
import { Avatar } from "./Avatar";
import { Badge } from "./Badge";
import { GoogleSignIn } from "./SignInControls";
import { Button } from "./ui/button";

/**
 * IdentityCard is the Account section of Settings: who this browser is, and Google
 * Workspace sign-in / sign-out. The local token is a machine credential for the CLI
 * and workers, not something this screen pastes.
 */
export function IdentityCard() {
  const viewer = useViewer();
  if (!viewer?.googleAuthEnabled) return null;

  const claimed = viewer.claimed;
  const isUser = viewer.kind === IdentityKind.USER;
  const label = viewerLabel(viewer);

  return (
    <article
      data-testid="identity-card"
      className="grid gap-3 rounded-md border border-border bg-panel p-4"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-bold">Google Workspace</h2>
        <Badge tone={isUser ? "ok" : "idle"} dot={false}>
          {isUser ? "Signed in" : "Signed out"}
        </Badge>
      </header>
      <p className="max-w-[68ch] text-sm text-soft">
        {claimed && viewer.hostedDomain
          ? `This Podium instance belongs to the ${viewer.hostedDomain} Workspace.`
          : "The first person to confirm claims this instance for their domain. Later sign-ins from that Workspace join as members."}
      </p>
      <div className="flex min-w-0 items-center gap-3">
        <Avatar src={viewer.pictureUrl ? "/auth/picture" : undefined} label={label.text} size="md" />
        <div className="grid min-w-0">
          <strong className="truncate text-sm">{isUser ? label.text : "Not signed in with Google"}</strong>
          <span className="truncate text-xs text-muted">
            {isUser
              ? `${viewer.login}${rbacEnforced(viewer) ? ` · ${roleLabel(canonicalRole(viewer.roles))}` : ""}`
              : "The local token is unchanged"}
          </span>
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {isUser ? (
          <Button asChild variant="outline">
            <a href="/auth/logout">Sign out</a>
          </Button>
        ) : (
          <GoogleSignIn claimed={claimed} bare />
        )}
      </div>
    </article>
  );
}
