import { LogOut } from "lucide-react";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { useViewer, viewerLabel } from "../lib/identity";
import { Avatar } from "./Avatar";
import { GoogleSignIn } from "./SignInControls";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";

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
    <Card data-testid="identity-card">
      <CardHeader>
        <CardTitle>Google Workspace</CardTitle>
        <CardDescription>
          {claimed && viewer.hostedDomain
            ? `This Podium belongs to the ${viewer.hostedDomain} Workspace.`
            : "The first person to confirm claims this instance for their domain. Later sign-ins from that Workspace join as members."}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex items-center gap-3">
          <Avatar src={viewer.pictureUrl ? "/auth/picture" : undefined} label={label.text} size="md" />
          <div className="min-w-0">
            <div className="truncate text-sm font-medium text-fg">{label.text}</div>
            <div className="truncate text-xs text-muted">
              {isUser ? viewer.login : "Not signed in with Google"}
            </div>
          </div>
        </div>

        {isUser ? (
          <Button asChild variant="outline" className="w-full">
            <a href="/auth/logout">
              <LogOut />
              Sign out
            </a>
          </Button>
        ) : (
          <GoogleSignIn claimed={claimed} />
        )}
      </CardContent>
    </Card>
  );
}
