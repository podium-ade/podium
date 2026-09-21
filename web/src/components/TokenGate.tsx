import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { errorMessage, identity, isUnauthenticated } from "../lib/client";
import { fetchAuthStatus, getToken, onRejected, setToken } from "../lib/auth";
import { ViewerContext, viewerFrom } from "../lib/identity";
import { ClaimScreen, GoogleSignIn, SignInShell, TokenForm } from "./SignInControls";

/**
 * TokenGate decides whether this deployment needs a credential from the browser at all.
 *
 * It probes WhoAmI once. Under the tailnet transport the call succeeds with no header: Tailscale
 * named the caller at the transport layer, so there is nothing to ask for and the gate never
 * shows — the answer goes straight into the header. Under the local transport the same probe comes
 * back Unauthenticated. When Google Workspace sign-in is configured, /auth/status says so and
 * the gate offers that; the shared local token is a machine credential for the CLI and workers,
 * not something the UI asks a human to paste. When Google is off, the UI still asks for
 * PODIUM_LOCAL_TOKEN and keeps it in localStorage.
 *
 * The probe is deliberately the same authenticated endpoint as everything else. Relaxing the
 * server's auth for a "who am I" endpoint would publish it to anything that can reach the port,
 * and templating the token into the served HTML would hand it to every reader of the page.
 * /auth/status is the one public exception: it names the sign-in methods, not a person.
 */
export function TokenGate({ children }: { children: ReactNode }) {
  const [attempt, setAttempt] = useState(0);
  const [forced, setForced] = useState(false);
  const [value, setValue] = useState(getToken());

  const probe = useQuery({
    queryKey: ["whoami", attempt],
    queryFn: () => identity.whoAmI({}),
    retry: false,
    staleTime: Infinity,
    gcTime: Infinity,
  });

  const status = useQuery({
    queryKey: ["auth-status"],
    queryFn: fetchAuthStatus,
    retry: false,
    staleTime: Infinity,
    gcTime: Infinity,
  });

  // Any later 401 — an expired session, or a token changed out from under us — re-gates.
  useEffect(() => onRejected(() => setForced(true)), []);

  const viewer = probe.isSuccess ? viewerFrom(probe.data) : undefined;
  // A failed Google callback lands on /?auth_error=… Stay on the sign-in screen so
  // the error is visible instead of vanishing into the app.
  const authError =
    typeof window !== "undefined"
      ? (new URLSearchParams(window.location.search).get("auth_error") ?? "")
      : "";

  if (!forced && probe.isSuccess && viewer && !authError) {
    if (viewer.canClaim) {
      return <ClaimScreen viewer={viewer} onClaimed={() => setAttempt((n) => n + 1)} />;
    }
    return <ViewerContext value={viewer}>{children}</ViewerContext>;
  }

  if (!forced && (probe.isPending || status.isPending)) {
    return (
      <div className="grid h-full place-items-center bg-background">
        <div className="flex items-center gap-2 text-sm text-muted">
          <Loader2 className="size-4 animate-spin" />
          Connecting…
        </div>
      </div>
    );
  }

  const google = status.data?.google === true || viewer?.googleAuthEnabled === true;
  const claimed = status.data?.claimed === true || viewer?.claimed === true;
  const rejected = !google && isUnauthenticated(probe.error) ? getToken() !== "" : false;
  const failure =
    probe.error && !isUnauthenticated(probe.error) ? errorMessage(probe.error) : undefined;

  return (
    <SignInShell>
      {google ? <GoogleSignIn claimed={claimed} /> : null}

      {!google ? (
        <>
          <p className="mb-5 text-center text-xs leading-relaxed text-muted">
            This server is on the <code className="font-mono text-fg">dev</code> transport, which
            authenticates every API call with a shared bearer token.
          </p>
          <TokenForm
            value={value}
            setValue={setValue}
            rejected={rejected}
            failure={failure}
            onSubmit={() => {
              setToken(value);
              setForced(false);
              setAttempt((n) => n + 1);
            }}
          />
        </>
      ) : null}
    </SignInShell>
  );
}
